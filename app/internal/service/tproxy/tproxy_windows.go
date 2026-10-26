//go:build windows

package tproxy

import "errors"

var errUnsupported = errors.New("透明代理依赖 nftables，仅支持 Linux；Windows 上请使用 mixed(:7890) 或局域网代理端口")

func (m *Manager) Supported() bool { return false }
