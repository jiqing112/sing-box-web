//go:build windows

package api

import "syscall"

// TUN 需要创建 wintun 虚拟网卡；面板是 sing-box 的父进程，
// 子进程继承它的令牌，所以只看面板自身是否提权即可。
var shell32IsUserAnAdmin = syscall.NewLazyDLL("shell32.dll").NewProc("IsUserAnAdmin")

func tunSupported() bool {
	ret, _, _ := shell32IsUserAnAdmin.Call()
	return ret != 0
}

func tunUnsupportedReason() string {
	return "面板未以管理员身份运行，无法创建 TUN 虚拟网卡（请右键“以管理员身份运行”）"
}
