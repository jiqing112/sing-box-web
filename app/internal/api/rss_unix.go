//go:build !windows

package api

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// procRSS 读取 /proc/<pid>/status 的 VmRSS，返回字节数。
func procRSS(pid int) int64 {
	if pid == 0 {
		return 0
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}
