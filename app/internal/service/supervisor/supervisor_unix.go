//go:build !windows

package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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

// SweepOrphans 清理上次面板强退后残留、exe 路径与本面板一致的 sing-box。
func SweepOrphans(bin string) {
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
		_ = killProcessTree(pid, true)
	}
}
