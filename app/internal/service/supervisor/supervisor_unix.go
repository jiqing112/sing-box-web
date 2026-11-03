//go:build !windows

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// setupProcessGroup 让子进程自成进程组，方便 Stop 时整组杀掉。
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree 向整个进程组发信号；force=false 发 SIGTERM，true 发 SIGKILL。
func killProcessTree(pid int, force bool) error {
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	return syscall.Kill(-pid, sig)
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// SweepOrphans 清理上次面板强退后残留的内核进程。
//
// 只按可执行文件路径匹配是不够的：用户完全可能拿同一份二进制跑别的东西
// （例如手动起一个 ss 服务端做测试、或者调试另一个配置），面板一启动就把它
// 顺手杀了，现象是「我什么都没动，测试进程自己没了」。
//
// 所以再加一条：命令行里必须引用了本面板的数据目录。面板拉起内核永远是
//   sing-box: run -c <dataDir>/config.json
//   mihomo:   -d <dataDir>/mihomo -f <dataDir>/config.yaml
// 这两者都在数据目录下，而用户自己的测试配置通常在别处。
// 读不到命令行时（权限/内核裁剪）按「是孤儿」处理，宁可多杀一个也不能漏：
// 漏掉的孤儿会一直占着 cache.db 和 Clash API 端口，让新核心进入崩溃循环。
func SweepOrphans(bin, dataDir string) {
	self, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		exe, err := os.Readlink("/proc/" + e.Name() + "/exe")
		if err != nil || exe != self {
			continue
		}
		if dataDir != "" {
			if argv, ok := cmdline(pid); ok && !argvTouches(argv, dataDir) {
				continue
			}
		}
		_ = killProcessTree(pid, true)
	}
}

// cmdline 读 /proc/<pid>/cmdline（NUL 分隔）。
// ok=false 表示读不到或为空——调用方按「无法判断」处理。
func cmdline(pid int) ([]string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(b) == 0 {
		return nil, false
	}
	parts := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	return parts, true
}

// argvTouches 判断参数里是否有落在 dataDir 下的路径。
func argvTouches(argv []string, dataDir string) bool {
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return true
	}
	abs = filepath.Clean(abs)
	for _, a := range argv {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		if !filepath.IsAbs(a) {
			continue
		}
		p := filepath.Clean(a)
		if p == abs || strings.HasPrefix(p, abs+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
