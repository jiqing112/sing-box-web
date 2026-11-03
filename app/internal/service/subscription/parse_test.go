package subscription

import (
	"testing"

	"singbox-panel/internal/model"
)

func TestParseURIListReservedTags(t *testing.T) {
	body := []byte("vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?security=tls#US-01\n" +
		"trojan://pass@5.6.7.8:8443#US-01\n")
	nodes, err := Parse(body, "s1", nil, map[string]bool{"US-01": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	if nodes[0].Tag != "US-01 #2" || nodes[1].Tag != "US-01 #3" {
		t.Fatalf("tags = %q, %q", nodes[0].Tag, nodes[1].Tag)
	}
	if !nodes[0].Enabled || !nodes[1].Enabled {
		t.Fatal("new nodes should default to enabled")
	}
}

func TestParseKeepsPreviousEnabledState(t *testing.T) {
	body := []byte("vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?security=tls#NodeA\n")
	first, err := Parse(body, "s1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Enabled = false

	second, err := Parse(body, "s1", first, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].ID != first[0].ID {
		t.Fatalf("id changed: %q -> %q", first[0].ID, second[0].ID)
	}
	if second[0].Enabled {
		t.Fatal("disabled state was not preserved")
	}
}

func TestParseEmptyBody(t *testing.T) {
	if _, err := Parse([]byte("   "), "s1", nil, nil); err == nil {
		t.Fatal("expected error for empty subscription")
	}
}

// ss:// 的 scheme 是 ss，但 sing-box 的 outbound 类型叫 shadowsocks。
// 直接把 scheme 当 type 会让 `sing-box check` 报
// "unknown outbound type: ss" 并拒绝启动 —— 整条链路直接不可用。
func TestParseSSURIUsesSingBoxOutboundType(t *testing.T) {
	// ss://base64(aes-128-gcm:pass)@1.2.3.4:8388#SS-01
	body := []byte("ss://YWVzLTEyOC1nY206cGFzcw==@1.2.3.4:8388#SS-01\n")
	nodes, err := Parse(body, "s1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	ob := nodes[0].Outbound
	if ob["type"] != "shadowsocks" {
		t.Fatalf("outbound type = %v, want shadowsocks", ob["type"])
	}
	if ob["method"] != "aes-128-gcm" || ob["password"] != "pass" {
		t.Fatalf("ss 凭据没解出来: %#v", ob)
	}
	// 展示用的 Node.Type 保留用户熟悉的简写
	if nodes[0].Type != "ss" {
		t.Fatalf("node.Type = %q, want ss（列表里显示用）", nodes[0].Type)
	}
}

func TestNormalizeOutboundType(t *testing.T) {
	cases := map[string]string{
		"ss": "shadowsocks", "SS": "shadowsocks", "shadowsocks": "shadowsocks",
		"hy2": "hysteria2", "hysteria2": "hysteria2",
		"socks5": "socks", "vless": "vless", "vmess": "vmess", "trojan": "trojan",
	}
	for in, want := range cases {
		if got := model.NormalizeOutboundType(in); got != want {
			t.Errorf("NormalizeOutboundType(%q) = %q, want %q", in, got, want)
		}
	}
}
