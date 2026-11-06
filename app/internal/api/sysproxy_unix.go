//go:build !windows

// 非 Windows：系统代理功能不支持（前端随 supported:false 隐藏开关）。

package api

import "fmt"

// SysProxyStatus 系统代理当前状态
type SysProxyStatus struct {
	Enabled bool
	Server  string
}

func sysProxySupported() bool { return false }

func sysProxyStatus() SysProxyStatus { return SysProxyStatus{} }

func sysProxySet(on bool, port int, dataDir string) error {
	return fmt.Errorf("系统代理仅 Windows 支持")
}
