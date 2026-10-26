package config

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"singbox-panel/internal/model"
)

func testNode(tag string, enabled bool) model.Node {
	return model.Node{
		Tag:     tag,
		Enabled: enabled,
		Outbound: map[string]any{
			"type":        "shadowsocks",
			"server":      "1.2.3.4",
			"server_port": 8388,
			"method":      "aes-128-gcm",
			"password":    "secret",
		},
	}
}

func testTemplate() map[string]any {
	return map[string]any{
		"inbounds": []any{
			map[string]any{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 7890},
		},
		"outbounds": []any{},
		"route":     map[string]any{},
		"experimental": map[string]any{
			"cache_file": map[string]any{"enabled": true, "path": "data/cache.db"},
		},
	}
}

func renderDoc(t *testing.T, opts RenderOpts, nodes ...model.Node) map[string]any {
	t.Helper()
	raw, err := Render(testTemplate(), nodes, opts)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not a map: %T", v)
	}
	return m
}

func asList(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("not a list: %T", v)
	}
	return l
}

func findInbound(t *testing.T, doc map[string]any, tag string) map[string]any {
	t.Helper()
	for _, it := range asList(t, doc["inbounds"]) {
		m := asMap(t, it)
		if m["tag"] == tag {
			return m
		}
	}
	t.Fatalf("inbound %q not found", tag)
	return nil
}

func TestRenderRequiresEnabledNode(t *testing.T) {
	_, err := Render(testTemplate(), []model.Node{testNode("off", false)}, RenderOpts{})
	if err == nil {
		t.Fatal("expected error when no node is enabled")
	}
}

func TestRenderBuildsOutboundsAndDefaults(t *testing.T) {
	dir := t.TempDir()
	doc := renderDoc(t, RenderOpts{DataDir: dir}, testNode("a", true), testNode("b", false))

	outbounds := asList(t, doc["outbounds"])
	if len(outbounds) != 4 {
		t.Fatalf("outbounds = %d, want 4 (selector+auto+node+direct)", len(outbounds))
	}
	sel := asMap(t, outbounds[0])
	if sel["type"] != "selector" || sel["tag"] != "proxy" {
		t.Fatalf("selector = %v", sel)
	}
	tags := asList(t, sel["outbounds"])
	if len(tags) != 3 || tags[0] != "auto" || tags[1] != "direct" || tags[2] != "a" {
		t.Fatalf("selector tags = %v", tags)
	}
	if got := asMap(t, outbounds[2]); got["tag"] != "a" {
		t.Fatalf("node outbound = %v", got)
	}

	route := asMap(t, doc["route"])
	if route["final"] != "proxy" {
		t.Fatalf("route.final = %v", route["final"])
	}
	if route["auto_detect_interface"] != true {
		t.Fatalf("route.auto_detect_interface = %v", route["auto_detect_interface"])
	}

	exp := asMap(t, doc["experimental"])
	api := asMap(t, exp["clash_api"])
	if api["external_controller"] != "127.0.0.1:9091" {
		t.Fatalf("clash_api = %v", api)
	}
	cache := asMap(t, exp["cache_file"])
	want := filepath.Join(dir, "cache.db")
	if cache["path"] != want {
		t.Fatalf("cache path = %v, want %v", cache["path"], want)
	}
}

func TestRenderLanProxyAndTun(t *testing.T) {
	doc := renderDoc(t, RenderOpts{
		DataDir:    t.TempDir(),
		LanProxy:   true,
		SocksPort:  1080,
		HTTPPort:   8118,
		User:       "u",
		Pass:       "12345678",
		TunEnabled: true,
	}, testNode("a", true))

	socks := findInbound(t, doc, "socks-lan")
	if socks["listen"] != "0.0.0.0" || socks["listen_port"] != float64(1080) {
		t.Fatalf("socks-lan = %v", socks)
	}
	users := asList(t, socks["users"])
	if len(users) != 1 || asMap(t, users[0])["username"] != "u" {
		t.Fatalf("socks users = %v", users)
	}
	http := findInbound(t, doc, "http-lan")
	if http["listen_port"] != float64(8118) {
		t.Fatalf("http-lan = %v", http)
	}
	tun := findInbound(t, doc, "tun-in")
	if tun["auto_route"] != true {
		t.Fatalf("tun-in = %v", tun)
	}
}

func TestRenderRoutingModes(t *testing.T) {
	tpl := func() map[string]any {
		m := testTemplate()
		m["dns"] = map[string]any{
			"servers": []any{map[string]any{"tag": "local", "type": "udp", "server": "223.5.5.5"}},
			"rules":   []any{map[string]any{"rule_set": []any{"geosite-cn"}, "server": "local"}},
			"final":   "remote",
		}
		m["route"] = map[string]any{
			"rules": []any{
				map[string]any{"action": "sniff"},
				map[string]any{"rule_set": []any{"geosite-cn"}, "outbound": "direct"},
			},
			"final": "proxy",
		}
		return m
	}

	cases := []struct {
		mode     string
		final    string
		rules    int
		dnsFinal string
		dnsRules int
	}{
		{"rule", "proxy", 2, "remote", 1},
		{"global", "proxy", 3, "remote", 0},
		{"direct", "direct", 2, "local", 0},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			raw, err := Render(tpl(), []model.Node{testNode("a", true)}, RenderOpts{RoutingMode: tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			route := asMap(t, doc["route"])
			if route["final"] != tc.final {
				t.Fatalf("route.final = %v, want %v", route["final"], tc.final)
			}
			if got := len(asList(t, route["rules"])); got != tc.rules {
				t.Fatalf("route.rules = %d, want %d", got, tc.rules)
			}
			dns := asMap(t, doc["dns"])
			if dns["final"] != tc.dnsFinal {
				t.Fatalf("dns.final = %v, want %v", dns["final"], tc.dnsFinal)
			}
			if got := len(asList(t, dns["rules"])); got != tc.dnsRules {
				t.Fatalf("dns.rules = %d, want %d", got, tc.dnsRules)
			}
			if tc.mode == "global" {
				last := asMap(t, asList(t, route["rules"])[2])
				if last["ip_is_private"] != true || last["outbound"] != "direct" {
					t.Fatalf("global should keep private direct rule: %v", last)
				}
			}
		})
	}
}

func TestRenderLanProxyWithoutAuth(t *testing.T) {
	doc := renderDoc(t, RenderOpts{
		DataDir:   t.TempDir(),
		LanProxy:  true,
		SocksPort: 1080,
		HTTPPort:  8118,
	}, testNode("a", true))

	socks := findInbound(t, doc, "socks-lan")
	if _, ok := socks["users"]; ok {
		t.Fatalf("open proxy should not have users: %v", socks)
	}
}

func TestRenderMigratesRuleSetDownloadDetour(t *testing.T) {
	tpl := testTemplate()
	tpl["route"] = map[string]any{
		"rule_set": []any{
			map[string]any{
				"tag": "geosite-cn", "type": "remote", "format": "binary",
				"url": "https://example.com/geosite.srs", "download_detour": "proxy",
			},
			// 已经是新写法的不应被改动
			map[string]any{
				"tag": "geoip-cn", "type": "remote", "format": "binary",
				"url": "https://example.com/geoip.srs", "http_client": map[string]any{"detour": "direct"},
			},
		},
	}
	raw, err := Render(tpl, []model.Node{testNode("a", true)}, RenderOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	sets := asList(t, asMap(t, doc["route"])["rule_set"])
	if len(sets) != 2 {
		t.Fatalf("rule_set = %d, want 2", len(sets))
	}
	first := asMap(t, sets[0])
	if _, ok := first["download_detour"]; ok {
		t.Fatal("download_detour 应被移除（1.16 会删除该字段）")
	}
	if hc := asMap(t, first["http_client"]); hc["detour"] != "proxy" {
		t.Fatalf("http_client = %v, want detour=proxy", hc)
	}
	second := asMap(t, sets[1])
	if hc := asMap(t, second["http_client"]); hc["detour"] != "direct" {
		t.Fatalf("已有的 http_client 被覆盖: %v", hc)
	}
}

func TestRenderMixedInboundOverride(t *testing.T) {
	doc := renderDoc(t, RenderOpts{
		DataDir:     t.TempDir(),
		MixedPort:   7891,
		MixedListen: "0.0.0.0",
	}, testNode("a", true))

	mixed := findInbound(t, doc, "mixed-in")
	if mixed["listen_port"] != float64(7891) {
		t.Fatalf("mixed listen_port = %v, want 7891", mixed["listen_port"])
	}
	if mixed["listen"] != "0.0.0.0" {
		t.Fatalf("mixed listen = %v, want 0.0.0.0", mixed["listen"])
	}
}

func TestRenderMixedInboundKeepsTemplateWhenUnset(t *testing.T) {
	doc := renderDoc(t, RenderOpts{DataDir: t.TempDir()}, testNode("a", true))
	mixed := findInbound(t, doc, "mixed-in")
	if mixed["listen_port"] != float64(7890) || mixed["listen"] != "127.0.0.1" {
		t.Fatalf("mixed = %v:%v, want 127.0.0.1:7890", mixed["listen"], mixed["listen_port"])
	}
}

func TestFromSettingsCarriesMixed(t *testing.T) {
	opts := FromSettings(model.Settings{
		LanProxy: true, LanSocksPort: 1080, LanHTTPPort: 8118,
		LanUser: "u", LanPass: "p", TunEnabled: true, RoutingMode: "global",
		MixedPort: 7895, MixedListen: "::1",
	}, "/data")
	if opts.DataDir != "/data" || !opts.LanProxy || opts.SocksPort != 1080 || opts.HTTPPort != 8118 {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.User != "u" || opts.Pass != "p" || !opts.TunEnabled || opts.RoutingMode != "global" {
		t.Fatalf("opts = %+v", opts)
	}
	if opts.MixedPort != 7895 || opts.MixedListen != "::1" {
		t.Fatalf("mixed = %d/%q", opts.MixedPort, opts.MixedListen)
	}
}
