package config

import (
	"encoding/json"
	"net"
	"strconv"
	"testing"
)

func cfgWithDNSIn(port int) []byte {
	b, _ := json.Marshal(map[string]any{
		"inbounds": []any{
			map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890},
			map[string]any{"type": "direct", "tag": LocalDNSTag, "listen": "127.0.0.1", "listen_port": port},
		},
		"outbounds": []any{},
	})
	return b
}

// freePort 拿一个当前空闲的端口（监听后立刻释放）
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

// occupy 占住一个端口（模拟 dnsmasq），返回释放函数
func occupy(t *testing.T, port int) func() {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("occupy %d: %v", port, err)
	}
	return func() { _ = ln.Close() }
}

func dnsInPort(t *testing.T, raw []byte) (int, bool) {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, it := range cfg["inbounds"].([]any) {
		in := it.(map[string]any)
		if in["tag"] == LocalDNSTag {
			return int(in["listen_port"].(float64)), true
		}
	}
	return 0, false
}

// 端口被别的进程占着 → 自动降级到候选端口
func TestRepairLocalDNS_Occupied(t *testing.T) {
	port := freePort(t)
	release := occupy(t, port)
	defer release()

	raw := cfgWithDNSIn(port)
	out, note := RepairLocalDNS(raw, 0)
	if note == "" {
		t.Fatal("端口被占用时应返回提示")
	}
	got, ok := dnsInPort(t, out)
	if !ok {
		t.Fatalf("入站被意外删除，note=%q", note)
	}
	if got == port {
		t.Fatalf("端口未被降级，仍是 %d", got)
	}
	found := false
	for _, p := range dnsPortFallbacks {
		if p == got {
			found = true
		}
	}
	if !found {
		t.Fatalf("降级端口 %d 不在候选列表里", got)
	}
}

// 端口空闲 → 原样返回
func TestRepairLocalDNS_FreePortKeeps(t *testing.T) {
	port := freePort(t)
	raw := cfgWithDNSIn(port)
	out, note := RepairLocalDNS(raw, 0)
	if note != "" {
		t.Fatalf("端口可用时不该改动: %q", note)
	}
	if string(out) != string(raw) {
		t.Fatal("端口可用时配置应原样返回")
	}
}

// 端口是自家核心占着的（ownPort）→ 不能当成冲突
func TestRepairLocalDNS_OwnPortKeeps(t *testing.T) {
	port := freePort(t)
	release := occupy(t, port)
	defer release()

	raw := cfgWithDNSIn(port)
	out, note := RepairLocalDNS(raw, port)
	if note != "" {
		t.Fatalf("自家核心占用的端口不该改动: %q", note)
	}
	if string(out) != string(raw) {
		t.Fatal("自家核心占用时配置应原样返回")
	}
}

// 渲染时模板写的是 53，但核心实际跑在别的端口 → 应复用它，不要每次换个端口
func TestRepairLocalDNS_ReuseOwnPort(t *testing.T) {
	busy53 := freePort(t)
	release53 := occupy(t, busy53)
	defer release53()
	own := freePort(t)
	releaseOwn := occupy(t, own)
	defer releaseOwn()

	raw := cfgWithDNSIn(busy53)
	out, note := RepairLocalDNS(raw, own)
	if note == "" {
		t.Fatal("53 被占用时应返回提示")
	}
	got, ok := dnsInPort(t, out)
	if !ok {
		t.Fatal("入站被意外删除")
	}
	if got != own {
		t.Fatalf("应复用核心当前端口 %d，实际 %d", own, got)
	}
}

func TestRepairLocalDNS_NoDNSIn(t *testing.T) {
	raw := []byte(`{"inbounds":[{"type":"mixed","tag":"mixed-in","listen_port":7890}]}`)
	out, note := RepairLocalDNS(raw, 0)
	if note != "" || string(out) != string(raw) {
		t.Fatal("没有 dns-in 入站时应原样返回")
	}
}

func TestRepairLocalDNS_NonJSON(t *testing.T) {
	raw := []byte("mixed-port: 7890\ndns:\n  listen: 0.0.0.0:5354\n")
	out, note := RepairLocalDNS(raw, 0)
	if note != "" || string(out) != string(raw) {
		t.Fatal("非 JSON（mihomo YAML）应原样返回")
	}
}

func TestRepairLocalDNS_AllBusyDropsInbound(t *testing.T) {
	var releases []func()
	defer func() {
		for _, r := range releases {
			r()
		}
	}()
	port := freePort(t)
	releases = append(releases, occupy(t, port))
	busy := map[int]bool{port: true}
	for _, p := range dnsPortFallbacks {
		if busy[p] {
			continue
		}
		releases = append(releases, occupy(t, p))
		busy[p] = true
	}
	raw := cfgWithDNSIn(port)
	out, note := RepairLocalDNS(raw, 0)
	if note == "" {
		t.Fatal("全部端口不可用时应返回提示")
	}
	if _, ok := dnsInPort(t, out); ok {
		t.Fatal("全部端口不可用时应摘掉 dns-in 入站")
	}
}

func TestLocalDNSPortOf(t *testing.T) {
	if got := LocalDNSPortOf(cfgWithDNSIn(5335)); got != 5335 {
		t.Fatalf("应读出 5335，实际 %d", got)
	}
	if got := LocalDNSPortOf([]byte("not json")); got != 0 {
		t.Fatalf("非 JSON 应返回 0，实际 %d", got)
	}
	if got := LocalDNSPortOf(nil); got != 0 {
		t.Fatalf("空配置应返回 0，实际 %d", got)
	}
}
