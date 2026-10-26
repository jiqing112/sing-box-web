package subscription

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"singbox-panel/internal/model"
)

/* ------------------------------------------------------------ entry */

// Parse 自动识别 Clash YAML / Base64(URI 列表) / sing-box JSON 三种订阅格式。
// prev 用于在刷新时保留节点的启用状态。
func Parse(body []byte, subID string, prev []model.Node, reserved map[string]bool) ([]model.Node, error) {
	prevEnabled := map[string]bool{}
	for _, n := range prev {
		prevEnabled[n.ID] = n.Enabled
	}

	var nodes []model.Node
	var err error

	switch {
	case strings.Contains(string(body), "proxies:"):
		nodes, err = parseClash(body)
	case looksLikeJSON(body):
		nodes, err = parseSingboxJSON(body)
	default:
		nodes, err = parseURIList(body)
	}
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no node parsed from subscription")
	}

	if reserved == nil {
		reserved = map[string]bool{}
	}
	for i := range nodes {
		n := &nodes[i]
		n.SubID = subID
		n.Name = strings.TrimSpace(n.Name)
		if n.Name == "" {
			n.Name = fmt.Sprintf("%s:%d", n.Server, n.Port)
		}
		// tag 必须全局唯一（跨订阅也不能重名），否则 sing-box check 会拒绝
		base := n.Name
		tag := base
		for k := 2; reserved[tag]; k++ {
			tag = fmt.Sprintf("%s #%d", base, k)
		}
		reserved[tag] = true
		n.Tag = tag
		n.ID = nodeID(subID, n.Tag, n.Server, n.Port)
		if v, ok := prevEnabled[n.ID]; ok {
			n.Enabled = v
		} else {
			n.Enabled = true
		}
		if ob, ok := n.Outbound["tag"]; ok && ob == "" {
			n.Outbound["tag"] = n.Tag
		}
		n.Outbound["tag"] = n.Tag
	}
	return nodes, nil
}

func nodeID(subID, tag, server string, port int) string {
	h := md5.Sum([]byte(tag + "|" + server + "|" + strconv.Itoa(port)))
	return subID + "-" + hex.EncodeToString(h[:])[:10]
}

func looksLikeJSON(b []byte) bool {
	t := strings.TrimSpace(string(b))
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

/* ------------------------------------------------------------ clash yaml */

type clashProxy struct {
	Name              string            `yaml:"name"`
	Type              string            `yaml:"type"`
	Server            string            `yaml:"server"`
	Port              int               `yaml:"port"`
	UUID              string            `yaml:"uuid"`
	Password          string            `yaml:"password"`
	Cipher            string            `yaml:"cipher"`
	AlterID           int               `yaml:"alterId"`
	Network           string            `yaml:"network"`
	TLS               bool              `yaml:"tls"`
	SkipCertVerify    bool              `yaml:"skip-cert-verify"`
	Servername        string            `yaml:"servername"`
	SNI               string            `yaml:"sni"`
	Flow              string            `yaml:"flow"`
	ClientFingerprint string            `yaml:"client-fingerprint"`
	Username          string            `yaml:"username"`
	Obfs              string            `yaml:"obfs"`
	ObfsPassword      string            `yaml:"obfs-password"`
	RealityOpts       struct {
		PublicKey string `yaml:"public-key"`
		ShortID   string `yaml:"short-id"`
	} `yaml:"reality-opts"`
	WSOpts struct {
		Path    string            `yaml:"path"`
		Headers map[string]string `yaml:"headers"`
	} `yaml:"ws-opts"`
	GrpcOpts struct {
		ServiceName string `yaml:"grpc-service-name"`
	} `yaml:"grpc-opts"`
}

type clashDoc struct {
	Proxies []clashProxy `yaml:"proxies"`
}

func parseClash(body []byte) ([]model.Node, error) {
	var doc clashDoc
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("clash yaml: %w", err)
	}
	var out []model.Node
	for _, p := range doc.Proxies {
		if isInfoNode(p.Name) {
			continue
		}
		ob, err := clashToOutbound(p)
		if err != nil {
			continue // 单个节点不支持时跳过，不影响整份订阅
		}
		out = append(out, model.Node{
			Name:     p.Name,
			Type:     p.Type,
			Server:   p.Server,
			Port:     p.Port,
			Region:   DetectRegion(p.Name),
			Outbound: ob,
		})
	}
	return out, nil
}

func clashToOutbound(p clashProxy) (map[string]any, error) {
	if p.Server == "" || p.Port == 0 {
		return nil, fmt.Errorf("missing server")
	}
	ob := map[string]any{
		"type":        p.Type,
		"server":      p.Server,
		"server_port": p.Port,
	}

	tls := map[string]any{}
	if p.TLS {
		tls["enabled"] = true
		if sn := firstNonEmpty(p.Servername, p.SNI, p.Server); sn != "" {
			tls["server_name"] = sn
		}
		if p.SkipCertVerify {
			tls["insecure"] = true
		}
		if p.ClientFingerprint != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": p.ClientFingerprint}
		}
		if p.RealityOpts.PublicKey != "" {
			tls["reality"] = map[string]any{
				"enabled":    true,
				"public_key": p.RealityOpts.PublicKey,
				"short_id":   p.RealityOpts.ShortID,
			}
		}
	}

	switch p.Type {
	case "vless":
		if p.UUID == "" {
			return nil, fmt.Errorf("vless without uuid")
		}
		ob["uuid"] = p.UUID
		if p.Flow != "" {
			ob["flow"] = p.Flow
		}
	case "vmess":
		ob["uuid"] = p.UUID
		ob["security"] = "auto"
		ob["alter_id"] = p.AlterID
	case "trojan":
		ob["password"] = p.Password
		if _, ok := tls["enabled"]; !ok {
			tls["enabled"] = true
		}
	case "shadowsocks":
		ob["method"] = p.Cipher
		ob["password"] = p.Password
	case "hysteria2":
		ob["password"] = p.Password
		if p.Obfs != "" {
			ob["obfs"] = map[string]any{"type": p.Obfs, "password": p.ObfsPassword}
		}
		if _, ok := tls["enabled"]; !ok {
			tls["enabled"] = true
		}
	case "tuic":
		ob["uuid"] = p.UUID
		ob["password"] = p.Password
		if _, ok := tls["enabled"]; !ok {
			tls["enabled"] = true
		}
	case "http":
		if p.Username != "" {
			ob["username"] = p.Username
		}
		if p.Password != "" {
			ob["password"] = p.Password
		}
	default:
		return nil, fmt.Errorf("unsupported type %s", p.Type)
	}

	if len(tls) > 0 {
		ob["tls"] = tls
	}

	// 传输层
	switch p.Network {
	case "ws":
		tr := map[string]any{"type": "ws", "path": firstNonEmpty(p.WSOpts.Path, "/")}
		if host, ok := p.WSOpts.Headers["Host"]; ok && host != "" {
			tr["headers"] = map[string]any{"Host": host}
		}
		ob["transport"] = tr
	case "grpc":
		ob["transport"] = map[string]any{"type": "grpc", "service_name": p.GrpcOpts.ServiceName}
	case "http", "h2":
		ob["transport"] = map[string]any{"type": "http"}
	}
	return ob, nil
}

/* ------------------------------------------------------------ v2ray uris */

func parseURIList(body []byte) ([]model.Node, error) {
	raw := strings.TrimSpace(string(body))
	if !strings.HasPrefix(raw, "vless://") && !strings.HasPrefix(raw, "vmess://") &&
		!strings.HasPrefix(raw, "trojan://") && !strings.HasPrefix(raw, "ss://") &&
		!strings.HasPrefix(raw, "hysteria2://") {
		if decoded, err := base64.StdEncoding.DecodeString(padB64(raw)); err == nil {
			raw = string(decoded)
		} else if decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(raw)); err == nil {
			raw = string(decoded)
		}
	}

	var out []model.Node
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if n, err := parseURI(line); err == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

func parseURI(s string) (model.Node, error) {
	if strings.HasPrefix(s, "vmess://") {
		return parseVmess(s)
	}
	u, err := url.Parse(s)
	if err != nil {
		return model.Node{}, err
	}
	port, _ := strconv.Atoi(u.Port())
	name := strings.TrimSpace(u.Fragment)
	q := u.Query()

	n := model.Node{Name: name, Server: u.Hostname(), Port: port, Region: DetectRegion(name)}
	ob := map[string]any{"type": u.Scheme, "server": n.Server, "server_port": port}

	switch u.Scheme {
	case "vless":
		ob["uuid"] = u.User.Username()
		if f := q.Get("flow"); f != "" {
			ob["flow"] = f
		}
	case "trojan":
		ob["password"] = u.User.Username()
	case "ss":
		// ss://base64(method:password)@host:port
		if ui := u.User.String(); ui != "" {
			if dec, err := base64.StdEncoding.DecodeString(padB64(ui)); err == nil {
				parts := strings.SplitN(string(dec), ":", 2)
				if len(parts) == 2 {
					ob["method"], ob["password"] = parts[0], parts[1]
				}
			}
		}
	case "hysteria2":
		ob["password"] = u.User.Username()
	default:
		return model.Node{}, fmt.Errorf("unsupported scheme %s", u.Scheme)
	}

	tls := map[string]any{}
	if sec := q.Get("security"); sec == "tls" || sec == "reality" || u.Scheme == "trojan" || u.Scheme == "hysteria2" {
		tls["enabled"] = true
		if sni := q.Get("sni"); sni != "" {
			tls["server_name"] = sni
		}
		if fp := q.Get("fp"); fp != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
		}
		if sec == "reality" {
			tls["reality"] = map[string]any{
				"enabled":    true,
				"public_key": q.Get("pbk"),
				"short_id":   q.Get("sid"),
			}
		}
	}
	if len(tls) > 0 {
		ob["tls"] = tls
	}
	switch q.Get("type") {
	case "ws":
		tr := map[string]any{"type": "ws", "path": firstNonEmpty(q.Get("path"), "/")}
		if h := q.Get("host"); h != "" {
			tr["headers"] = map[string]any{"Host": h}
		}
		ob["transport"] = tr
	case "grpc":
		ob["transport"] = map[string]any{"type": "grpc", "service_name": q.Get("serviceName")}
	}

	n.Type = u.Scheme
	n.Outbound = ob
	return n, nil
}

func parseVmess(s string) (model.Node, error) {
	raw := strings.TrimPrefix(s, "vmess://")
	dec, err := base64.StdEncoding.DecodeString(padB64(raw))
	if err != nil {
		return model.Node{}, err
	}
	var v struct {
		PS   string `json:"ps"`
		Add  string `json:"add"`
		Port any    `json:"port"`
		ID   string `json:"id"`
		Net  string `json:"net"`
		Host string `json:"host"`
		Path string `json:"path"`
		TLS  string `json:"tls"`
		SNI  string `json:"sni"`
	}
	if err := json.Unmarshal(dec, &v); err != nil {
		return model.Node{}, err
	}
	port, _ := strconv.Atoi(fmt.Sprint(v.Port))
	ob := map[string]any{
		"type": "vmess", "server": v.Add, "server_port": port,
		"uuid": v.ID, "security": "auto", "alter_id": 0,
	}
	if v.TLS == "tls" {
		tls := map[string]any{"enabled": true}
		if v.SNI != "" {
			tls["server_name"] = v.SNI
		}
		ob["tls"] = tls
	}
	if v.Net == "ws" {
		tr := map[string]any{"type": "ws", "path": firstNonEmpty(v.Path, "/")}
		if v.Host != "" {
			tr["headers"] = map[string]any{"Host": v.Host}
		}
		ob["transport"] = tr
	}
	return model.Node{
		Name: v.PS, Type: "vmess", Server: v.Add, Port: port,
		Region: DetectRegion(v.PS), Outbound: ob,
	}, nil
}

/* ------------------------------------------------------------ sing-box json */

func parseSingboxJSON(body []byte) ([]model.Node, error) {
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("sing-box json: %w", err)
	}
	var out []model.Node
	for _, ob := range doc.Outbounds {
		t, _ := ob["type"].(string)
		if t == "selector" || t == "urltest" || t == "direct" || t == "block" || t == "dns" {
			continue
		}
		tag, _ := ob["tag"].(string)
		server, _ := ob["server"].(string)
		port := 0
		if p, ok := ob["server_port"].(float64); ok {
			port = int(p)
		}
		out = append(out, model.Node{
			Name: tag, Type: t, Server: server, Port: port,
			Region: DetectRegion(tag), Outbound: ob,
		})
	}
	return out, nil
}

/* ------------------------------------------------------------ helpers */

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func padB64(s string) string {
	s = strings.TrimSpace(s)
	if m := len(s) % 4; m != 0 {
		s += strings.Repeat("=", 4-m)
	}
	return s
}

var regionWords = []struct {
	code string
	keys []string
}{
	{"HK", []string{"香港", "Hong Kong", "HK", "深港", "沪港"}},
	{"TW", []string{"台湾", "台灣", "Taiwan", "TW"}},
	{"JP", []string{"日本", "Japan", "东京", "大阪", "JP"}},
	{"SG", []string{"新加坡", "狮城", "Singapore", "SG"}},
	{"US", []string{"美国", "美國", "United States", "USA", "US"}},
	{"KR", []string{"韩国", "韓國", "Korea", "KR"}},
	{"GB", []string{"英国", "United Kingdom", "UK", "GB"}},
	{"DE", []string{"德国", "Germany", "DE"}},
	{"FR", []string{"法国", "France", "FR"}},
	{"RU", []string{"俄罗斯", "Russia", "RU"}},
	{"CA", []string{"加拿大", "Canada", "CA"}},
	{"AU", []string{"澳大利亚", "Australia", "AU"}},
	{"IN", []string{"印度", "India", "IN"}},
	{"TR", []string{"土耳其", "Turkey", "TR"}},
}

// DetectRegion 先看国旗 emoji，再按关键词匹配。
func DetectRegion(name string) string {
	runes := []rune(name)
	if len(runes) >= 2 {
		a, b := runes[0], runes[1]
		if a >= 0x1F1E6 && a <= 0x1F1FF && b >= 0x1F1E6 && b <= 0x1F1FF {
			return string([]rune{0x41 + (a - 0x1F1E6), 0x41 + (b - 0x1F1E6)})
		}
	}
	lower := strings.ToLower(name)
	for _, r := range regionWords {
		for _, k := range r.keys {
			if strings.Contains(lower, strings.ToLower(k)) {
				return r.code
			}
		}
	}
	return ""
}

// SortByRegion 让节点列表按地区聚拢，便于前端分组。
func SortByRegion(nodes []model.Node) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Region == nodes[j].Region {
			return nodes[i].Name < nodes[j].Name
		}
		return nodes[i].Region < nodes[j].Region
	})
}

// isInfoNode 过滤机场塞进订阅的「剩余流量 / 套餐到期 / 官网」占位条目。
func isInfoNode(name string) bool {
	for _, k := range []string{"剩余流量", "距离下次", "套餐到期", "官网", "过期时间", "重置剩余", "到期时间", "流量重置"} {
		if strings.Contains(name, k) {
			return true
		}
	}
	return false
}
