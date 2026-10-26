//go:build !windows

package main

func defaultDataDir() string    { return "/opt/sing-box-panel/data" }
func defaultSingBoxBin() string { return "/usr/local/bin/sing-box" }
func defaultNftPath() string    { return "/usr/sbin/nft" }
