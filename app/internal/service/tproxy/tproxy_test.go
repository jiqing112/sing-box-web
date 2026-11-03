package tproxy

import (
	"strconv"
	"strings"
	"testing"
)

func TestNFTMajorVersion(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"nftables v0.9.3 (Topsy)", 0},
		{"nftables v1.0.9 (Old Doc Yak #3)", 1},
		{"nftables v1.1.3 (Commodore Bullmoose #3)", 1},
		{"", 0},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := nftMajorVersion(c.in); got != c.want {
			t.Errorf("nftMajorVersion(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// nft < 1.0（麒麟 0.9.3）没有 socket cgroupv2 语法，本机接管要退化成 fwmark
func TestBuild_LocalUsesMarkWithoutCgroupV2(t *testing.T) {
	m := &Manager{nft: "/usr/sbin/nft", port: 12345, cgPath: "user.slice/session.scope", cgLevel: 2, cgOK: false}
	script := m.build([]string{"192.168.1.0/24"}, true, false)

	if !strings.Contains(script, "meta mark "+strconv.Itoa(Mark)+" return") {
		t.Fatalf("缺少 fwmark 放行规则:\n%s", script)
	}
	if strings.Contains(script, "cgroupv2") {
		t.Fatalf("不该出现 cgroupv2（老 nft 不认）:\n%s", script)
	}
	if !strings.Contains(script, "meta l4proto tcp redirect to :12345") {
		t.Fatalf("缺少 output 重定向:\n%s", script)
	}
}

// nft >= 1.0 时仍走 cgroupv2，精确定位到面板自己的 cgroup
func TestBuild_LocalUsesCgroupWhenSupported(t *testing.T) {
	m := &Manager{nft: "/usr/sbin/nft", port: 12345, cgPath: "user.slice/session.scope", cgLevel: 2, cgOK: true}
	script := m.build([]string{"192.168.1.0/24"}, true, false)

	if !strings.Contains(script, `socket cgroupv2 level 2 "user.slice/session.scope" return`) {
		t.Fatalf("缺少 cgroupv2 放行规则:\n%s", script)
	}
	if strings.Contains(script, "meta mark") {
		t.Fatalf("支持 cgroup 时不该用 fwmark:\n%s", script)
	}
}

// 不接管本机时不应该有 output 链
func TestBuild_NoOutputChainWhenLanOnly(t *testing.T) {
	m := &Manager{nft: "/usr/sbin/nft", port: 12345, cgPath: "x", cgLevel: 1, cgOK: false}
	script := m.build([]string{"192.168.1.0/24"}, false, false)

	if strings.Contains(script, "hook output") {
		t.Fatalf("仅局域网模式不该生成 output 链:\n%s", script)
	}
	if !strings.Contains(script, "ip saddr { 192.168.1.0/24 } meta l4proto tcp redirect to :12345") {
		t.Fatalf("缺少 prerouting 重定向:\n%s", script)
	}
}

// DNS 劫持规则只在显式开启时下发
func TestBuild_DNSHijack(t *testing.T) {
	m := &Manager{nft: "/usr/sbin/nft", port: 12345}
	with := m.build([]string{"192.168.1.0/24"}, false, true)
	if !strings.Contains(with, "udp dport 53 redirect to :5354") ||
		!strings.Contains(with, "tcp dport 53 redirect to :5354") {
		t.Fatalf("开启 DNS 劫持时缺少 53 重定向:\n%s", with)
	}
	without := m.build([]string{"192.168.1.0/24"}, false, false)
	if strings.Contains(without, "dport 53") {
		t.Fatalf("关闭 DNS 劫持时不该有 53 规则:\n%s", without)
	}
}

func TestLocalSupported(t *testing.T) {
	// 有 cgroupv2 且 nft 支持 → 可以
	if m := (&Manager{nft: "/usr/sbin/nft", cgPath: "a/b", cgLevel: 2, cgOK: true}); !m.LocalSupported() {
		t.Error("cgroupv2 可用时应支持本机接管")
	}
	// 老 nft 但能用 fwmark → 也可以
	if m := (&Manager{nft: "/usr/sbin/nft", cgPath: "a/b", cgLevel: 2, cgOK: false}); !m.LocalSupported() {
		t.Error("nft 可用时应退化成 fwmark 并支持本机接管")
	}
	// 没有 nft → 不行
	if m := (&Manager{nft: ""}); m.LocalSupported() {
		t.Error("没有 nft 时不该支持")
	}
}
