package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"singbox-panel/internal/model"
	"singbox-panel/internal/service/engine"
)

// clashTemplate 是一份最小的 mihomo 模板，字段名与内置模板保持一致，
// 但去掉了 DNS/日志等与本次断言无关的部分。
func clashTemplate() map[string]any {
	return map[string]any{
		"mixed-port":   7890,
		"mode":         "rule",
		"proxies":      []any{},
		"proxy-groups": []any{},
		"dns":          map[string]any{"enable": true, "nameserver": []any{"223.5.5.5"}},
		"rules":        []any{"MATCH,DIRECT"},
	}
}

func renderClashDoc(t *testing.T, opts RenderOpts, nodes ...model.Node) map[string]any {
	t.Helper()
	opts.Engine = engine.Mihomo
	raw, err := Render(clashTemplate(), nodes, opts)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("渲染结果不是合法 YAML: %v", err)
	}
	return doc
}

func clashNode(tag, typ string, ob map[string]any) model.Node {
	base := map[string]any{"type": typ, "server": "1.2.3.4", "server_port": 8388}
	for k, v := range ob {
		base[k] = v
	}
	return model.Node{Tag: tag, Name: tag, Type: typ, Server: "1.2.3.4", Port: 8388, Enabled: true, Outbound: base}
}

func TestRenderClashProxiesAndGroups(t *testing.T) {
	nodes := []model.Node{
		clashNode("ss-1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p1"}),
		clashNode("vmess-ws", "vmess", map[string]any{
			"uuid": "u1", "alter_id": 0,
			"tls":       map[string]any{"enabled": true, "server_name": "example.com"},
			"transport": map[string]any{"type": "ws", "path": "/ray", "headers": map[string]any{"Host": "cdn.example.com"}},
		}),
		clashNode("vless-reality", "vless", map[string]any{
			"uuid": "u2", "flow": "xtls-rprx-vision",
			"tls": map[string]any{
				"enabled": true, "server_name": "www.microsoft.com",
				"utls":    map[string]any{"enabled": true, "fingerprint": "chrome"},
				"reality": map[string]any{"enabled": true, "public_key": "PUBKEY", "short_id": "abcd"},
			},
		}),
	}

	doc := renderClashDoc(t, RenderOpts{}, nodes...)

	proxies, _ := doc["proxies"].([]any)
	if len(proxies) != 3 {
		t.Fatalf("proxies = %d, want 3", len(proxies))
	}

	ss := proxies[0].(map[string]any)
	if ss["type"] != "ss" || ss["cipher"] != "aes-128-gcm" || ss["password"] != "p1" {
		t.Fatalf("ss 转换错误: %#v", ss)
	}
	if ss["name"] != "ss-1" {
		t.Fatalf("ss name = %v", ss["name"])
	}

	vm := proxies[1].(map[string]any)
	if vm["type"] != "vmess" || vm["uuid"] != "u1" || vm["network"] != "ws" {
		t.Fatalf("vmess 转换错误: %#v", vm)
	}
	if vm["tls"] != true || vm["servername"] != "example.com" {
		t.Fatalf("vmess tls 转换错误: %#v", vm)
	}
	ws := vm["ws-opts"].(map[string]any)
	if ws["path"] != "/ray" {
		t.Fatalf("ws path = %v", ws["path"])
	}
	if host := ws["headers"].(map[string]any)["Host"]; host != "cdn.example.com" {
		t.Fatalf("ws host = %v", host)
	}

	vl := proxies[2].(map[string]any)
	if vl["type"] != "vless" || vl["flow"] != "xtls-rprx-vision" {
		t.Fatalf("vless 转换错误: %#v", vl)
	}
	if vl["client-fingerprint"] != "chrome" {
		t.Fatalf("reality 必须带指纹，实际 %v", vl["client-fingerprint"])
	}
	r := vl["reality-opts"].(map[string]any)
	if r["public-key"] != "PUBKEY" || r["short-id"] != "abcd" {
		t.Fatalf("reality-opts = %#v", r)
	}

	groups, _ := doc["proxy-groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("proxy-groups = %d, want 2", len(groups))
	}
	sel := groups[0].(map[string]any)
	if sel["name"] != "proxy" || sel["type"] != "select" {
		t.Fatalf("selector 组 = %#v", sel)
	}
	members, _ := sel["proxies"].([]any)
	if len(members) != 5 || members[0] != "auto" || members[1] != "DIRECT" {
		t.Fatalf("selector 成员 = %v", members)
	}
	auto := groups[1].(map[string]any)
	if auto["name"] != "auto" || auto["type"] != "url-test" {
		t.Fatalf("urltest 组 = %#v", auto)
	}
}

func TestRenderClashSkipsUnsupportedType(t *testing.T) {
	nodes := []model.Node{
		clashNode("wg", "wireguard", map[string]any{"private_key": "x"}),
		clashNode("ss-ok", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"}),
	}
	doc := renderClashDoc(t, RenderOpts{}, nodes...)
	proxies, _ := doc["proxies"].([]any)
	if len(proxies) != 1 {
		t.Fatalf("不支持的协议应被跳过，proxies = %d", len(proxies))
	}
}

func TestRenderClashNoNodes(t *testing.T) {
	opts := RenderOpts{Engine: engine.Mihomo}
	if _, err := Render(clashTemplate(), nil, opts); err == nil {
		t.Fatal("没有启用节点时应报错")
	}
}

func TestRenderClashPortsAndListeners(t *testing.T) {
	// 固定成非 Linux，避免把「Linux 额外有 redir 入站」算进来；
	// Linux 的完整组合见 TestRenderClashLinuxListenersAll。
	withOS(t, "windows")
	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}

	doc := renderClashDoc(t, RenderOpts{
		MixedPort: 7891, MixedListen: "0.0.0.0",
		LanProxy: true, SocksPort: 1080, HTTPPort: 8118, User: "u", Pass: "p12345678",
	}, nodes...)

	if doc["mixed-port"] != 7891 {
		t.Fatalf("mixed-port = %v", doc["mixed-port"])
	}
	if doc["bind-address"] != "0.0.0.0" || doc["allow-lan"] != true {
		t.Fatalf("监听设置错误: bind-address=%v allow-lan=%v", doc["bind-address"], doc["allow-lan"])
	}

	listeners, _ := doc["listeners"].([]any)
	if len(listeners) != 2 {
		t.Fatalf("listeners = %d, want 2", len(listeners))
	}
	socks := listeners[0].(map[string]any)
	if socks["type"] != "socks" || socks["port"] != 1080 || socks["listen"] != "0.0.0.0" {
		t.Fatalf("socks listener = %#v", socks)
	}
	users, _ := socks["users"].([]any)
	if len(users) != 1 || users[0].(map[string]any)["username"] != "u" {
		t.Fatalf("socks users = %#v", users)
	}
}

func TestRenderClashNoLanListenersWhenDisabled(t *testing.T) {
	withOS(t, "windows")
	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}
	tpl := clashTemplate()
	tpl["listeners"] = []any{map[string]any{"name": "stale", "type": "http", "port": 8118}}
	raw, err := Render(tpl, nodes, RenderOpts{Engine: engine.Mihomo})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["listeners"]; ok {
		t.Fatal("关闭局域网代理后不应残留 listeners")
	}
}

// Linux 上关掉局域网代理只删 socks/http，透明代理入站必须留下：
// 它是常驻的，否则每次开透明代理都要改配置重启内核。
func TestRenderClashLinuxKeepsRedirListenerWhenLanOff(t *testing.T) {
	withOS(t, "linux")
	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}
	tpl := clashTemplate()
	tpl["listeners"] = []any{map[string]any{"name": "stale", "type": "http", "port": 8118}}
	raw, err := Render(tpl, nodes, RenderOpts{Engine: engine.Mihomo, TproxyPort: 12345})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ls, _ := doc["listeners"].([]any)
	if len(ls) != 1 {
		t.Fatalf("listeners = %d, want 1（只留 redir 入站）: %#v", len(ls), doc["listeners"])
	}
	redir := ls[0].(map[string]any)
	if redir["type"] != "redir" || redir["port"] != 12345 {
		t.Fatalf("redir 入站 = %#v", redir)
	}
}

func TestRenderClashRoutingModes(t *testing.T) {
	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}

	global := renderClashDoc(t, RenderOpts{RoutingMode: "global"}, nodes...)
	rules, _ := global["rules"].([]any)
	last := rules[len(rules)-1]
	if last != "MATCH,proxy" {
		t.Fatalf("全局代理的兜底规则 = %v", last)
	}

	direct := renderClashDoc(t, RenderOpts{RoutingMode: "direct"}, nodes...)
	rules, _ = direct["rules"].([]any)
	last = rules[len(rules)-1]
	if last != "MATCH,DIRECT" {
		t.Fatalf("全局直连的兜底规则 = %v", last)
	}

	// rule 模式保留模板规则，但必须补齐兜底项
	rule := renderClashDoc(t, RenderOpts{RoutingMode: "rule"}, nodes...)
	rules, _ = rule["rules"].([]any)
	if rules[len(rules)-1] != "MATCH,DIRECT" {
		t.Fatalf("模板里的 MATCH 应被保留，实际 %v", rules[len(rules)-1])
	}

	noMatch := map[string]any{"rules": []any{"GEOSITE,cn,DIRECT"}}
	raw, err := Render(noMatch, nodes, RenderOpts{Engine: engine.Mihomo, RoutingMode: "rule"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "MATCH,proxy") {
		t.Fatalf("缺少 MATCH 时应自动补 MATCH,proxy:\n%s", raw)
	}
}

func TestRenderClashTun(t *testing.T) {
	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}
	doc := renderClashDoc(t, RenderOpts{TunEnabled: true}, nodes...)
	tun, ok := doc["tun"].(map[string]any)
	if !ok {
		t.Fatal("开启 TUN 后应有 tun 段")
	}
	if tun["enable"] != true || tun["stack"] != "mixed" || tun["auto-route"] != true {
		t.Fatalf("tun 配置 = %#v", tun)
	}
}

func TestInspectMihomo(t *testing.T) {
	withOS(t, "linux")
	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}
	raw, err := Render(clashTemplate(), nodes, RenderOpts{
		Engine: engine.Mihomo, MixedPort: 7892, TproxyPort: 12345, TunEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p := MixedPort(raw, engine.Mihomo); p != 7892 {
		t.Fatalf("MixedPort = %d", p)
	}
	if !TunRunning(raw, engine.Mihomo) {
		t.Fatal("TunRunning 应为 true")
	}
	if n := RuleCount(raw, engine.Mihomo); n == 0 {
		t.Fatal("RuleCount 不应为 0")
	}
	if err := VerifyTproxy(raw, engine.Mihomo, 12345, false); err != nil {
		t.Fatalf("Linux 渲染结果应通过 VerifyTproxy: %v", err)
	}
}

func TestVerifyTproxyMihomo(t *testing.T) {
	// 新写法：listeners 里的 redir 入站
	viaListeners := []byte(`
listeners:
  - name: redir-in
    type: redir
    port: 12345
    listen: 0.0.0.0
    udp: false
dns:
  enable: true
  listen: 0.0.0.0:5354
`)
	if err := VerifyTproxy(viaListeners, engine.Mihomo, 12345, true); err != nil {
		t.Fatalf("listeners 写法的合法配置被拒绝: %v", err)
	}
	if err := VerifyTproxy(viaListeners, engine.Mihomo, 12346, false); err == nil {
		t.Fatal("listeners 端口不匹配时应报错")
	}

	// 老写法：顶层 redir-port，兼容手写配置
	cfg := []byte(`
redir-port: 12345
dns:
  enable: true
  listen: 0.0.0.0:5354
`)
	if err := VerifyTproxy(cfg, engine.Mihomo, 12345, true); err != nil {
		t.Fatalf("合法配置被拒绝: %v", err)
	}
	if err := VerifyTproxy(cfg, engine.Mihomo, 12346, false); err == nil {
		t.Fatal("redir-port 不匹配时应报错")
	}
	noDNS := []byte("redir-port: 12345\ndns:\n  enable: true\n")
	if err := VerifyTproxy(noDNS, engine.Mihomo, 12345, true); err == nil {
		t.Fatal("dns.listen 缺失时应报错")
	}
}

// 只绑回环的 redir 入站必须被认出来：规则会命中，但局域网客户端连不上。
func TestRedirReachableFromLAN(t *testing.T) {
	loopback := []byte(`
listeners:
  - name: redir-in
    type: redir
    port: 12345
    listen: 127.0.0.1
`)
	if RedirReachableFromLAN(loopback, engine.Mihomo, 12345) {
		t.Fatal("绑 127.0.0.1 的 redir 入站应判定为局域网不可达")
	}
	all := []byte(`
listeners:
  - name: redir-in
    type: redir
    port: 12345
    listen: 0.0.0.0
`)
	if !RedirReachableFromLAN(all, engine.Mihomo, 12345) {
		t.Fatal("绑 0.0.0.0 的 redir 入站应判定为可达")
	}
	// 老写法无法从配置推断，不制造假警告
	if !RedirReachableFromLAN([]byte("redir-port: 12345\n"), engine.Mihomo, 12345) {
		t.Fatal("redir-port 写法不应判定为不可达")
	}
	// sing-box：redirect 入站写回环时同样不可达
	sbLoop := []byte(`{"inbounds":[{"type":"redirect","listen":"127.0.0.1","listen_port":12345}]}`)
	if RedirReachableFromLAN(sbLoop, engine.SingBox, 12345) {
		t.Fatal("sing-box redirect 入站绑回环时应判定为不可达")
	}
	sbAll := []byte(`{"inbounds":[{"type":"redirect","listen":"0.0.0.0","listen_port":12345}]}`)
	if !RedirReachableFromLAN(sbAll, engine.SingBox, 12345) {
		t.Fatal("sing-box redirect 入站绑 0.0.0.0 时应判定为可达")
	}
}

func TestDecodeTemplateRejectsBadYAML(t *testing.T) {
	if _, err := DecodeTemplate([]byte("a: [1, 2"), engine.Mihomo); err == nil {
		t.Fatal("非法 YAML 应报错")
	}
	if _, err := DecodeTemplate([]byte("{bad json"), engine.SingBox); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

// withOS 临时把 hostOS 切到指定平台，用来在任意开发机上验证 Linux 分支。
// redir-port / dns.listen 写错就是整网断掉，不能只靠「在 Linux 上手动点一下」来保证。
func withOS(t *testing.T, osName string) {
	t.Helper()
	prev := hostOS
	hostOS = osName
	t.Cleanup(func() { hostOS = prev })
}

// Linux 上 mihomo 的透明代理靠 listeners 里的 redir 入站 + dns.listen 两个字段：
// nft 把 LAN 的 TCP 重定向到 redir 入站，把 53 重定向到 dns.listen。
// 少一个，规则就指向没人监听的端口。
func TestRenderClashLinuxRedirAndDNS(t *testing.T) {
	withOS(t, "linux")

	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}
	doc := renderClashDoc(t, RenderOpts{MixedPort: 7890, TproxyPort: 12345}, nodes...)

	// 顶层 redir-port 会被 bind-address/allow-lan 绑到回环地址，局域网连不上，
	// 所以渲染层不该再写它。
	if _, ok := doc["redir-port"]; ok {
		t.Fatalf("不应再写顶层 redir-port（它会被 bind-address 锁到回环）: %v", doc["redir-port"])
	}

	redir := findRedirListener(t, doc)
	if redir == nil {
		t.Fatal("Linux 上必须有 redir 入站，否则 nft redirect 没有落点")
	}
	if redir["port"] != 12345 {
		t.Fatalf("redir 入站端口 = %v, want 12345", redir["port"])
	}
	// 关键：必须是 0.0.0.0。nft redirect 的目标是入接口地址，
	// 绑回环时局域网客户端连过来是 Connection refused。
	if redir["listen"] != "0.0.0.0" {
		t.Fatalf("redir 入站必须监听 0.0.0.0，实际 %v", redir["listen"])
	}

	dns, ok := doc["dns"].(map[string]any)
	if !ok {
		t.Fatal("缺 dns 段")
	}
	if got, _ := dns["listen"].(string); got != "0.0.0.0:5354" {
		t.Fatalf("dns.listen = %v, want 0.0.0.0:5354（与 tproxy 的 lanDNSPort 一致）", dns["listen"])
	}
	if on, _ := dns["enable"].(bool); !on {
		t.Fatal("dns.enable 必须为 true，否则 dns.listen 不生效")
	}
}

// 端口没配时用默认 12345：端口 0 会让 mihomo 随机挑端口，
// nft 规则就永远对不上。
func TestRenderClashLinuxRedirDefaultPort(t *testing.T) {
	withOS(t, "linux")
	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}
	doc := renderClashDoc(t, RenderOpts{}, nodes...)
	redir := findRedirListener(t, doc)
	if redir == nil {
		t.Fatal("缺 redir 入站")
	}
	if redir["port"] != 12345 {
		t.Fatalf("redir 端口 = %v, want 默认 12345", redir["port"])
	}
}

// 透明代理 + 局域网 socks/http 同时开启时，listeners 必须三个都在。
// 曾经拆成两个函数分别写 listeners，后写的把前一个覆盖掉，redir 入站直接消失。
func TestRenderClashLinuxListenersAll(t *testing.T) {
	withOS(t, "linux")
	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}
	doc := renderClashDoc(t, RenderOpts{
		TproxyPort: 12345, LanProxy: true, SocksPort: 1080, HTTPPort: 8118,
	}, nodes...)
	ls, _ := doc["listeners"].([]any)
	if len(ls) != 3 {
		t.Fatalf("listeners = %d, want 3（redir + socks + http）: %#v", len(ls), doc["listeners"])
	}
	types := []string{}
	for _, it := range ls {
		m, _ := it.(map[string]any)
		types = append(types, m["type"].(string))
	}
	if types[0] != "redir" || types[1] != "socks" || types[2] != "http" {
		t.Fatalf("listeners 类型 = %v", types)
	}
}

func findRedirListener(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	ls, _ := doc["listeners"].([]any)
	for _, it := range ls {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if typ, _ := m["type"].(string); typ == "redir" {
			return m
		}
	}
	return nil
}

// 渲染结果必须能通过 VerifyTproxy 自检，否则切内核时面板会误判
// 「透明代理不成立」并把它关掉。
func TestRenderClashLinuxVerifyTproxy(t *testing.T) {
	withOS(t, "linux")
	nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}
	raw, err := Render(clashTemplate(), nodes, RenderOpts{Engine: engine.Mihomo, TproxyPort: 12345})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTproxy(raw, engine.Mihomo, 12345, true); err != nil {
		t.Fatalf("自渲染的配置没通过透明代理自检: %v", err)
	}
	if !RedirReachableFromLAN(raw, engine.Mihomo, 12345) {
		t.Fatal("自渲染的 redir 入站应可从局域网访问")
	}
}

// 非 Linux 平台必须把 redir-port 删掉：mihomo 在 Windows/macOS 上
// 看到 redir-port 会直接启动失败。
func TestRenderClashNonLinuxDropsRedir(t *testing.T) {
	for _, osName := range []string{"windows", "darwin"} {
		withOS(t, osName)
		tpl := clashTemplate()
		tpl["redir-port"] = 9999 // 模拟用户从 Linux 那边抄来的模板
		nodes := []model.Node{clashNode("n1", "shadowsocks", map[string]any{"method": "aes-128-gcm", "password": "p"})}
		raw, err := Render(tpl, nodes, RenderOpts{Engine: engine.Mihomo, TproxyPort: 12345})
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		if _, ok := doc["redir-port"]; ok {
			t.Fatalf("%s 上不应保留 redir-port", osName)
		}
		if findRedirListener(t, doc) != nil {
			t.Fatalf("%s 上不应有 redir 入站", osName)
		}
	}
}
