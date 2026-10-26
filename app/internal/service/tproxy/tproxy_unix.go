//go:build !windows

package tproxy

import "errors"

var errUnsupported = errors.New("透明代理依赖 nftables，仅支持 Linux")

func (m *Manager) Supported() bool { return true }
