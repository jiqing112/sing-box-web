//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

func defaultDataDir() string { return "/opt/sing-box-panel/data" }
func defaultNftPath() string { return "/usr/sbin/nft" }

// defaultSingBoxBin 依次尝试常见安装位置，找不到就交给 PATH 解析。
// install.sh 会把内核装进 /usr/local/bin，所以那里排在最前；
// 末尾再看一眼面板自己所在的目录，这样直接解压 tar.gz 原地跑也能用。
func defaultSingBoxBin() string {
	return pickBin("sing-box", "/usr/local/bin/sing-box")
}

// defaultMihomoBin 与 sing-box 同一套查找顺序。
// mihomo 是可选的第二个内核，没装也不影响面板启动，只是切换时报「不可用」。
func defaultMihomoBin() string {
	return pickBin("mihomo", "/usr/local/bin/mihomo")
}

// pickBin 按「标准安装路径 → 面板同目录 → 兜底路径」的顺序找可执行文件。
// 面板同目录排在标准路径之后，避免旧版本内核盖掉系统里已升级的那个。
func pickBin(name, fallback string) string {
	for _, p := range []string{
		fallback,
		"/usr/bin/" + name,
		"/opt/sing-box-panel/bin/" + name,
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return fallback
}
