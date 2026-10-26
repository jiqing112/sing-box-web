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

// SweepOrphans 清理上次面板强退后残留的 sing-box（父子关系已断，Stop 管不到）。
// 孤儿会一直占着 cache.db 和 9091，导致新核心 cache-file 超时崩溃循环。
// 只按可执行文件路径精确匹配，不动用户自装的其它 sing-box。
func SweepOrphans(bin string) {
	self, err := filepath.Abs(bin)
	if err != nil {
		return
	}
	for _, p := range listOrphans() {
		if !strings.EqualFold(p.Path, self) {
			continue
		}
		_ = killProcessTree(p.PID, true)
		fmt.Fprintf(os.Stderr, "[panel] 清理残留核心进程 PID %d\n", p.PID)
	}
}

type orphanProc struct {
	Path string
	PID  int
}

// listOrphans 枚举 sing-box.exe。优先 wmic（老系统），
// 新版 Windows 已移除 wmic，退回 PowerShell CIM。
func listOrphans() []orphanProc {
	out, err := exec.Command("wmic", "process", "where", "name='sing-box.exe'",
		"get", "processid,executablepath", "/FORMAT:CSV").Output()
	if err == nil && len(bytes.TrimSpace(out)) > 0 {
		return parseWMIC(out)
	}
	out, err = exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-CimInstance Win32_Process -Filter \"Name='sing-box.exe'\" | ForEach-Object { '{0}|{1}' -f $_.ProcessId, $_.ExecutablePath }").Output()
	if err != nil {
		return nil
	}
	return parsePowerShell(out)
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

// parsePowerShell 解析 "PID|ExecutablePath" 行。
func parsePowerShell(out []byte) []orphanProc {
	var procs []orphanProc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
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
		procs = append(procs, orphanProc{Path: path, PID: pid})
	}
	return procs
}
