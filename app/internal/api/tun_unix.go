//go:build !windows

package api

import "os"

func tunSupported() bool {
	return os.Geteuid() == 0
}

func tunUnsupportedReason() string {
	return "面板未以 root 身份运行，无法创建 TUN 设备"
}
