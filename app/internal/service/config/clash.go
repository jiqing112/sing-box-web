package config

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"singbox-panel/internal/model"
)

// LanDNSPort 是局域网 DNS 接管的目标端口，与 nft 规则里的 redirect 目标保持一致。
// mihomo 用 dns.listen 表达，sing-box 用独立的 dns-lan 入站表达。
const LanDNSPort = 5354

// hostOS 是当前编译目标平台。抽成变量只为让测试能在任意平台验证
// Linux 分支（redir-port / dns.listen）——那是最容易写错、又最不能出错的一段：
// 端口写错就是整网断掉。
var hostOS = runtime.GOOS

// RenderClash 把模板与节点渲染成 mihomo 的 Clash YAML。
//
// 与 sing-box 那套的分工一致：模板负责「除了节点以外的一切」，
// 这里只注入 proxies / proxy-groups，并按面板设置覆盖端口、TUN、路由规则。
// 节点存的是 sing-box 风格的 outbound 片段，需要逐字段翻译成 Clash proxy，
// 这样订阅解析只有一份实现，两个内核共用同一份节点数据。
func RenderClash(tpl map[string]any, nodes []model.Node, opts RenderOpts) ([]byte, error) {
	var enabled []model.Node
	for _, n := range nodes {
		if n.Enabled {
			enabled = append(enabled, n)
		}
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("没有启用的节点")
	}

	proxies := make([]any, 0, len(enabled))
	tags := make([]string, 0, len(enabled))
	for _, n := range enabled {
		p, ok := clashProxyFromNode(n)
		if !ok {
			continue
		}
		p["name"] = n.Tag
		proxies = append(proxies, p)
		tags = append(tags, n.Tag)
	}
	if len(proxies) == 0 {
		return nil, fmt.Errorf("没有可转换的节点：mihomo 不支持订阅里的这些协议")
	}

	if tpl == nil {
		tpl = map[string]any{}
	}
	tpl["proxies"] = proxies

	// 策略组名字与 sing-box 那套保持一致（proxy / auto），
	// 面板的「当前出站」与测速都按这两个名字找，切换内核时不用改前端。
	autoMembers := make([]any, 0, len(tags))
	for _, t := range tags {
		autoMembers = append(autoMembers, t)
	}
	selectMembers := append([]any{"auto", "DIRECT"}, autoMembers...)
	tpl["proxy-groups"] = []any{
		map[string]any{
			"name":    "proxy",
			"type":    "select",
			"proxies": selectMembers,
		},
		map[string]any{
			"name":      "auto",
			"type":      "url-test",
			"url":       "https://www.gstatic.com/generate_204",
			"interval":  600,
			"tolerance": 50,
			"proxies":   autoMembers,
		},
	}

	// 面板靠这个地址读状态、测速、拉连接，必须与 -clash 参数保持一致
	tpl["external-controller"] = opts.ClashController()
	applyClashRouting(tpl, opts.RoutingMode)
	applyClashPorts(tpl, opts)
	applyClashListeners(tpl, opts)
	applyClashTun(tpl, opts)
	applyClashSniffer(tpl)

	out, err := yaml.Marshal(tpl)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml: %w", err)
	}
	return out, nil
}

// clashPrivateRules 是「内网直连」规则。用显式 IP-CIDR 而不是 GEOIP,private，
// 这样即使 geo 数据库没下载完，内网流量也不会被送去节点（私有地址本来就转不出去）。
var clashPrivateRules = []string{
	"IP-CIDR,127.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
	"IP-CIDR,172.16.0.0/12,DIRECT,no-resolve",
	"IP-CIDR,192.168.0.0/16,DIRECT,no-resolve",
	"IP-CIDR,100.64.0.0/10,DIRECT,no-resolve",
	"IP-CIDR,169.254.0.0/16,DIRECT,no-resolve",
}

// applyClashRouting 用面板设置覆盖模板里的分流策略。
// rule 保持模板原样（只补齐兜底的 MATCH）；global/direct 直接换成一整套规则，
// 避免模板里遗留的 CN 直连规则让「全局代理」名不副实。
func applyClashRouting(tpl map[string]any, mode string) {
	switch mode {
	case "global":
		tpl["rules"] = append(append([]string{}, clashPrivateRules...), "MATCH,proxy")
		tpl["mode"] = "global"
	case "direct":
		tpl["rules"] = append(append([]string{}, clashPrivateRules...), "MATCH,DIRECT")
		tpl["mode"] = "direct"
	default:
		// mihomo 要求规则表里有兜底项，否则未命中的流量行为不确定。
		rules := toStringList(tpl["rules"])
		hasMatch := false
		for _, r := range rules {
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(r)), "MATCH") {
				hasMatch = true
				break
			}
		}
		if !hasMatch {
			rules = append(rules, "MATCH,proxy")
		}
		tpl["rules"] = rules
		tpl["mode"] = "rule"
	}
}

// clashRedirListenerName 是透明代理入站在 listeners 里的固定名字。
// 名字固定下来，日志与 `ss -ltnp` 里一眼就能认出是面板下发的那个入站。
const clashRedirListenerName = "redir-in"

// applyClashPorts 注入本机 mixed 入口与局域网 DNS 接管端口。
func applyClashPorts(tpl map[string]any, opts RenderOpts) {
	listen := opts.MixedListen
	if listen == "" {
		listen = "127.0.0.1"
	}
	if opts.MixedPort > 0 {
		tpl["mixed-port"] = opts.MixedPort
	}
	tpl["bind-address"] = listen
	// allow-lan 与 sing-box 的语义对齐：只有监听非回环地址时才对外开放。
	tpl["allow-lan"] = listen != "127.0.0.1" && listen != "::1"

	// 透明代理不再用顶层 redir-port。
	//
	// redir-port 的监听地址受 bind-address / allow-lan 支配：默认配置是
	// bind-address: 127.0.0.1 + allow-lan: false，于是 redir-port 被绑到
	// 127.0.0.1:12345。而 nft 的 redirect 目标是「入接口地址」——局域网
	// 客户端连的是 192.168.x.y:12345，那里没人监听，表现是规则命中却
	// Connection refused，看着像「透明代理开了但没用」。
	// 删掉 bind-address 也没用，allow-lan: false 一样锁回环。
	//
	// 所以改由 applyClashListeners 用 listeners 显式声明 listen: 0.0.0.0，
	// 与 sing-box 那边 redirect 入站的 0.0.0.0 写法保持一致。
	// 这里顺手删掉模板里可能残留的 redir-port（多开一个回环端口没有意义）。
	delete(tpl, "redir-port")

	// 局域网 DNS 接管的目标：nft 把 LAN 的 53 端口 redirect 到这里。
	// mihomo 的 redir 入站只在 Linux 上存在，Windows/macOS 上写了会启动失败。
	if hostOS == "linux" {
		dns, _ := tpl["dns"].(map[string]any)
		if dns == nil {
			dns = map[string]any{}
			tpl["dns"] = dns
		}
		dns["enable"] = true
		dns["listen"] = fmt.Sprintf("0.0.0.0:%d", LanDNSPort)
	}
}

// applyClashListeners 维护 listeners 段：Linux 的透明代理入站 + 可选的局域网 socks/http。
//
// 三件事都写同一个字段，所以必须在同一处拼装。曾经拆成两个函数分别写，
// 后调用的那个把 listeners 整个覆盖掉，redir 入站直接消失。
func applyClashListeners(tpl map[string]any, opts RenderOpts) {
	var out []any

	// 透明代理入站常驻：没有 nft 规则时不会有流量进来，
	// 打开透明代理只需要下发 nft，不必改配置重启内核。
	// udp 显式关掉：redir 只能还原 TCP 的原始目标，UDP 需要 TUN。
	if hostOS == "linux" {
		port := opts.TproxyPort
		if port <= 0 {
			port = 12345
		}
		out = append(out, map[string]any{
			"name":   clashRedirListenerName,
			"type":   "redir",
			"port":   port,
			"listen": "0.0.0.0",
			"udp":    false,
		})
	}

	if opts.LanProxy {
		withAuth := opts.User != "" && opts.Pass != ""
		var users []any
		if withAuth {
			users = []any{map[string]any{"username": opts.User, "password": opts.Pass}}
		}
		socks := map[string]any{
			"name": "socks-lan", "type": "socks",
			"port": opts.SocksPort, "listen": "0.0.0.0", "udp": true,
		}
		httpL := map[string]any{
			"name": "http-lan", "type": "http",
			"port": opts.HTTPPort, "listen": "0.0.0.0",
		}
		if withAuth {
			socks["users"] = users
			httpL["users"] = users
		}
		out = append(out, socks, httpL)
	}

	if len(out) == 0 {
		// 关掉开关就把 listeners 整段删掉，避免上次的开放代理留在配置里
		delete(tpl, "listeners")
		return
	}
	tpl["listeners"] = out
}

// applyClashTun 生成 TUN 入站。mihomo 的 tun 是顶层对象（不是 listeners 里的一项）。
// applyClashSniffer 保证 redirect/tproxy 接管的纯 IP 连接能按域名分流。
// mihomo 必须开 sniffer（TLS SNI / HTTP Host），且 override-destination 要把
// 嗅探到的域名覆盖连接目标，规则才能按域名命中；否则国外域名会被路由到
// 污染 IP 上直连失败。sing-box 侧由路由规则的 {"action":"sniff"} 承担。
func applyClashSniffer(tpl map[string]any) {
	if cur, ok := tpl["sniffer"].(map[string]any); ok {
		cur["enable"] = true
		if _, ok := cur["parse-pure-ip"]; !ok {
			cur["parse-pure-ip"] = true
		}
		if _, ok := cur["force-dns-mapping"]; !ok {
			cur["force-dns-mapping"] = true
		}
		if _, ok := cur["override-destination"]; !ok {
			cur["override-destination"] = true
		}
		return
	}
	tpl["sniffer"] = map[string]any{
		"enable": true,
		"sniff": map[string]any{
			"TLS":  map[string]any{"ports": []any{443, 8443}, "override-destination": true},
			"HTTP": map[string]any{"ports": []any{80, "8080-8880"}, "override-destination": true},
			"QUIC": map[string]any{"ports": []any{443}, "override-destination": true},
		},
		"force-dns-mapping":    true,
		"parse-pure-ip":        true,
		"override-destination": true,
	}
}

func applyClashTun(tpl map[string]any, opts RenderOpts) {
	if !opts.TunEnabled {
		if tun, ok := tpl["tun"].(map[string]any); ok {
			tun["enable"] = false
		}
		return
	}
	tpl["tun"] = map[string]any{
		"enable":                true,
		"stack":                 "mixed",
		"device":                "mihomo-tun",
		"auto-route":            true,
		"auto-detect-interface": true,
		"dns-hijack":            []any{"any:53"},
		"mtu":                   9000,
	}
}

/* ------------------------------------------------------------ node -> clash proxy */

// clashProxyFromNode 把一条 sing-box outbound 片段翻译成 Clash proxy。
// 返回 false 表示该协议 mihomo 不支持（或字段不全），调用方跳过这条节点。
func clashProxyFromNode(n model.Node) (map[string]any, bool) {
	ob := n.Outbound
	if ob == nil {
		return nil, false
	}
	server := strVal(ob["server"])
	port := intVal(ob["server_port"])
	if server == "" || port <= 0 {
		return nil, false
	}

	p := map[string]any{"server": server, "port": port}
	typ := strings.ToLower(strVal(ob["type"]))

	switch typ {
	case "shadowsocks", "ss":
		p["type"] = "ss"
		p["cipher"] = strVal(ob["method"])
		p["password"] = strVal(ob["password"])
		p["udp"] = true
	case "vmess":
		p["type"] = "vmess"
		p["uuid"] = strVal(ob["uuid"])
		p["alterId"] = intVal(ob["alter_id"])
		p["cipher"] = "auto"
		p["udp"] = true
	case "vless":
		p["type"] = "vless"
		p["uuid"] = strVal(ob["uuid"])
		if f := strVal(ob["flow"]); f != "" {
			p["flow"] = f
		}
		p["udp"] = true
	case "trojan":
		p["type"] = "trojan"
		p["password"] = strVal(ob["password"])
		p["udp"] = true
	case "hysteria2", "hy2":
		p["type"] = "hysteria2"
		p["password"] = strVal(ob["password"])
		if obfs, ok := ob["obfs"].(map[string]any); ok {
			if t := strVal(obfs["type"]); t != "" {
				p["obfs"] = t
				p["obfs-password"] = strVal(obfs["password"])
			}
		}
	case "tuic":
		p["type"] = "tuic"
		p["uuid"] = strVal(ob["uuid"])
		p["password"] = strVal(ob["password"])
	case "http":
		p["type"] = "http"
		if u := strVal(ob["username"]); u != "" {
			p["username"] = u
		}
		if pw := strVal(ob["password"]); pw != "" {
			p["password"] = pw
		}
	case "socks":
		p["type"] = "socks5"
		if u := strVal(ob["username"]); u != "" {
			p["username"] = u
		}
		if pw := strVal(ob["password"]); pw != "" {
			p["password"] = pw
		}
		p["udp"] = true
	default:
		return nil, false
	}

	// TLS：sing-box 的 tls 对象与 Clash 的扁平字段一一对应，
	// 差别只在 sni / servername 的字段名（hysteria2/tuic/trojan 用 sni）。
	if tls, ok := ob["tls"].(map[string]any); ok {
		if on, _ := tls["enabled"].(bool); on {
			p["tls"] = true
			sn := strVal(tls["server_name"])
			if sn == "" {
				sn = server
			}
			switch p["type"] {
			case "trojan", "hysteria2", "tuic":
				p["sni"] = sn
			default:
				p["servername"] = sn
			}
			if in, _ := tls["insecure"].(bool); in {
				p["skip-cert-verify"] = true
			}
			if utls, ok := tls["utls"].(map[string]any); ok {
				if fp := strVal(utls["fingerprint"]); fp != "" {
					p["client-fingerprint"] = fp
				}
			}
			if r, ok := tls["reality"].(map[string]any); ok {
				if pk := strVal(r["public_key"]); pk != "" {
					p["reality-opts"] = map[string]any{
						"public-key": pk,
						"short-id":   strVal(r["short_id"]),
					}
					// mihomo 的 REALITY 必须指定指纹，缺省用 chrome
					if _, ok := p["client-fingerprint"]; !ok {
						p["client-fingerprint"] = "chrome"
					}
				}
			}
		}
	}

	// 传输层
	if tr, ok := ob["transport"].(map[string]any); ok {
		switch strings.ToLower(strVal(tr["type"])) {
		case "ws":
			p["network"] = "ws"
			wsOpts := map[string]any{"path": firstNonEmptyStr(strVal(tr["path"]), "/")}
			if hd, ok := tr["headers"].(map[string]any); ok {
				if host := strVal(hd["Host"]); host != "" {
					wsOpts["headers"] = map[string]any{"Host": host}
				}
			}
			p["ws-opts"] = wsOpts
		case "grpc":
			p["network"] = "grpc"
			p["grpc-opts"] = map[string]any{"grpc-service-name": strVal(tr["service_name"])}
		case "http", "h2":
			p["network"] = "http"
			h2 := map[string]any{}
			if h := strVal(tr["host"]); h != "" {
				h2["host"] = []any{h}
			}
			if pth := strVal(tr["path"]); pth != "" {
				h2["path"] = pth
			}
			if len(h2) > 0 {
				p["h2-opts"] = h2
			}
		}
	}

	return p, true
}

/* ------------------------------------------------------------ small helpers */

func strVal(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case bool:
		return strconv.FormatBool(t)
	}
	return fmt.Sprint(v)
}

func intVal(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// toStringList 把模板里的规则表（可能是 []any 或 []string）统一成 []string。
func toStringList(v any) []string {
	switch t := v.(type) {
	case []string:
		return append([]string{}, t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, it := range t {
			out = append(out, strVal(it))
		}
		return out
	}
	return nil
}
