//go:build windows

package main

import (
	"os"
	"path/filepath"
)

// defaultDataDir 默认放在 exe 同目录的 panel-data，整个目录拷走即完成迁移。
func defaultDataDir() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "panel-data")
	}
	return "panel-data"
}

// defaultSingBoxBin 优先使用 exe 同目录的 sing-box.exe，否则交给 PATH 解析。
func defaultSingBoxBin() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "sing-box.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "sing-box.exe"
}

func defaultNftPath() string { return "nft" }
