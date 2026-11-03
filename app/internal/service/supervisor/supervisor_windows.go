//go:build windows

package supervisor

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const createNewProcessGroup = 0x00000200

// setupProcessGroup：Windows 上让 sing-box 自成进程组（不随面板收到 Ctrl+C），
// 并隐藏子进程控制台窗口，避免每次启动核心都闪黑框。
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNewProcessGroup,
	}
}

// killProcessTree 用 taskkill /T /F 终止整棵进程树。
// 不带 /F 的「温和」终止依赖窗口消息，对控制台程序无效只会白等，所以统一强杀。
func killProcessTree(pid int, force bool) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

func processAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), fmt.Sprintf(`"%d"`, pid))
}

// SweepOrphans 清理上次面板强退后残留的内核（父子关系已断，Stop 管不到）。
// 孤儿会一直占着 cache.db 和 9091，导致新核心 cache-file 超时崩溃循环。
//
// 匹配条件：可执行文件完整路径一致，且命令行里引用了本面板的数据目录。
// 只按路径匹配会误杀用户手动起的同款二进制（例如拿 sing-box.exe 跑个测试
// 服务端），现象是「我什么都没动，测试进程自己没了」。
// 读不到命令行时（wmic 回退路径）按「是孤儿」处理：宁可多杀一个也不能漏，
// 漏掉的孤儿会让新核心起不来。
func SweepOrphans(bin, dataDir string) {
	self, err := filepath.Abs(bin)
	if err != nil {
		return
	}
	name := filepath.Base(self)
	for _, p := range listOrphans(name) {
		if !strings.EqualFold(p.Path, self) {
			continue
		}
		if p.Cmd != "" && dataDir != "" && !cmdTouches(p.Cmd, dataDir) {
			continue
		}
		_ = killProcessTree(p.PID, true)
		fmt.Fprintf(os.Stderr, "[panel] 清理残留核心进程 PID %d\n", p.PID)
	}
}

// cmdTouches 判断命令行里是否出现了数据目录。Windows 路径大小写不敏感，
// 且同一条命令行里可能混用 / 和 \，两种写法都查一遍。
func cmdTouches(cmd, dataDir string) bool {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return true
	}
	low := strings.ToLower(cmd)
	for _, d := range []string{abs, filepath.ToSlash(abs)} {
		if strings.Contains(low, strings.ToLower(d)) {
			return true
		}
	}
	return false
}

type orphanProc struct {
	Path string
	PID  int
	Cmd  string // 空表示这条记录没有命令行信息（wmic 回退）
}

// listOrphans 枚举指定名字的进程。新版 Windows（11 24H2 起）已移除 wmic，
// 所以先走 PowerShell CIM，失败再退回 wmic（老系统 / 无 PowerShell 的精简镜像）。
func listOrphans(name string) []orphanProc {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-CimInstance Win32_Process -Filter \"Name='"+name+"'\" | ForEach-Object { '{0}|{1}|{2}' -f $_.ProcessId, $_.ExecutablePath, $_.CommandLine }").Output()
	if err == nil && len(bytes.TrimSpace(out)) > 0 {
		return parsePowerShell(out)
	}
	out, err = exec.Command("wmic", "process", "where", "name='"+name+"'",
		"get", "processid,executablepath", "/FORMAT:CSV").Output()
	if err != nil {
		return nil
	}
	return parseWMIC(out)
}

// parseWMIC 解析 /FORMAT:CSV 输出。表头行因 PID 不是数字会被跳过；
// 用 csv.Reader 而不是 Split(",")，避免路径含逗号时列错位。
func parseWMIC(out []byte) []orphanProc {
	rd := csv.NewReader(bytes.NewReader(out))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	var procs []orphanProc
	for {
		rec, err := rd.Read()
		if err != nil {
			break
		}
		if len(rec) < 3 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rec[len(rec)-1]))
		if err != nil || pid <= 0 {
			continue
		}
		path := strings.TrimSpace(rec[len(rec)-2])
		if path == "" {
			continue
		}
		procs = append(procs, orphanProc{Path: path, PID: pid})
	}
	return procs
}

// parsePowerShell 解析 "PID|ExecutablePath|CommandLine" 行。
// 用 SplitN(...,3)：命令行本身可能含 '|'，必须整段保留。
func parsePowerShell(out []byte) []orphanProc {
	var procs []orphanProc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil || pid <= 0 {
			continue
		}
		path := strings.TrimSpace(parts[1])
		if path == "" {
			continue
		}
		cmd := ""
		if len(parts) == 3 {
			cmd = strings.TrimSpace(parts[2])
		}
		procs = append(procs, orphanProc{Path: path, PID: pid, Cmd: cmd})
	}
	return procs
}
