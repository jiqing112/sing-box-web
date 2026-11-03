package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"singbox-panel/internal/service/engine"
)

// 本文件收拢「从已下发的配置里读回事实」的查询。
//
// 面板有若干处需要知道配置里到底写了什么：mixed 端口是多少、有几条规则、
// 有没有 TUN、有没有监听透明代理端口的入站。这些判断在 sing-box（JSON）
// 和 mihomo（YAML）下字段完全不同，集中在这里避免每处都写两套 if。

// doc 是两种格式归一化之后的视图。
type doc map[string]any

func parseDoc(raw []byte, eng string) (doc, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("配置为空")
	}
	if engine.IsMihomo(eng) {
		var m map[string]any
		if err := yaml.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("配置不是合法 YAML: %w", err)
		}
		if m == nil {
			return nil, fmt.Errorf("配置为空")
		}
		return stringifyKeys(m).(map[string]any), nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("配置不是合法 JSON: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("配置为空")
	}
	return m, nil
}

// ValidateDoc 只做「是不是这份格式」的粗校验，真正的语义校验交给内核的 check。
func ValidateDoc(raw []byte, eng string) error {
	if _, err := parseDoc(raw, eng); err != nil {
		return err
	}
	return nil
}

// MixedPort 返回本机 mixed 入口的端口；没有该入口时返回 0。
func MixedPort(raw []byte, eng string) int {
	d, err := parseDoc(raw, eng)
	if err != nil {
		return 0
	}
	if engine.IsMihomo(eng) {
		return intVal(d["mixed-port"])
	}
	inb, _ := d["inbounds"].([]any)
	for _, it := range inb {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if t, _ := m["type"].(string); t == "mixed" {
			if p := intVal(m["listen_port"]); p > 0 {
				return p
			}
		}
	}
	return 0
}

// RuleCount 返回分流规则条数，用于概览页展示。
func RuleCount(raw []byte, eng string) int {
	d, err := parseDoc(raw, eng)
	if err != nil {
		return 0
	}
	if engine.IsMihomo(eng) {
		rules, _ := d["rules"].([]any)
		return len(rules)
	}
	route, _ := d["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	return len(rules)
}

// TunRunning 判断已下发的配置里是否真的带 TUN 接管。
func TunRunning(raw []byte, eng string) bool {
	d, err := parseDoc(raw, eng)
	if err != nil {
		return false
	}
	if engine.IsMihomo(eng) {
		tun, _ := d["tun"].(map[string]any)
		on, _ := tun["enable"].(bool)
		return on
	}
	inb, _ := d["inbounds"].([]any)
	for _, it := range inb {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if t, _ := m["type"].(string); t == "tun" {
			return true
		}
	}
	return false
}

// VerifyTproxy 确认配置里确实监听了透明代理要重定向到的端口。
// 不通过时不能下发 nft 规则：规则一生效，流量就会被送到没人监听的端口，
// 表现为整网断网。
func VerifyTproxy(raw []byte, eng string, port int, hijackDNS bool) error {
	d, err := parseDoc(raw, eng)
	if err != nil {
		return fmt.Errorf("配置解析失败: %w", err)
	}
	if engine.IsMihomo(eng) {
		if got := mihomoRedirPort(d); got != port {
			return fmt.Errorf("当前配置没有监听 :%d 的透明代理入站（当前为 %d），请先应用包含该端口的配置", port, got)
		}
		if hijackDNS {
			dns, _ := d["dns"].(map[string]any)
			listen := strVal(dns["listen"])
			if !strings.HasSuffix(listen, fmt.Sprintf(":%d", LanDNSPort)) {
				return fmt.Errorf("当前配置的 dns.listen 不是 :%d，无法接管局域网 DNS", LanDNSPort)
			}
		}
		return nil
	}
	inb, _ := d["inbounds"].([]any)
	hasRedirect, hasDNS := false, false
	for _, it := range inb {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		t, _ := m["type"].(string)
		if t == "redirect" && intVal(m["listen_port"]) == port {
			hasRedirect = true
		}
		if intVal(m["listen_port"]) == LanDNSPort {
			hasDNS = true
		}
	}
	if !hasRedirect {
		return fmt.Errorf("当前配置没有监听 :%d 的 redirect 入站，请先应用包含该入站的配置", port)
	}
	if hijackDNS && !hasDNS {
		return fmt.Errorf("当前配置没有监听 :%d 的 DNS 入站，无法接管局域网 DNS", LanDNSPort)
	}
	return nil
}

// mihomoRedirPort 返回配置里真正用于透明代理的端口，没有则返回 0。
//
// 两种写法都认：
//   - listeners 里 type: redir 的条目（渲染层现在用的写法，能显式指定监听地址）
//   - 顶层 redir-port（老配置、以及用户手写的配置）
//
// listeners 优先：它才是真正生效的那个入站。
func mihomoRedirPort(d doc) int {
	if ls, _ := d["listeners"].([]any); ls != nil {
		for _, it := range ls {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			if t, _ := m["type"].(string); t != "redir" {
				continue
			}
			if p := intVal(m["port"]); p > 0 {
				return p
			}
		}
	}
	return intVal(d["redir-port"])
}

// RedirReachableFromLAN 判断透明代理入站是否绑在局域网客户端连得到的地址上。
//
// 这不是「能不能启动」的问题，而是「LAN 透明代理是否真的生效」的问题：
// 入站只监听 127.0.0.1 时，nft 规则照样下发、照样命中，但客户端连的是
// 网关的局域网地址，那里没人监听，结果是 Connection refused。
// 配置里没写 listen（= 监听全部地址）或无法判断时一律返回 true，不制造假警告。
func RedirReachableFromLAN(raw []byte, eng string, port int) bool {
	d, err := parseDoc(raw, eng)
	if err != nil {
		return true
	}
	if engine.IsMihomo(eng) {
		ls, _ := d["listeners"].([]any)
		for _, it := range ls {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			if t, _ := m["type"].(string); t != "redir" || intVal(m["port"]) != port {
				continue
			}
			return !isLoopbackListen(strVal(m["listen"]))
		}
		// 老写法：redir-port 的监听地址由 bind-address / allow-lan 推导，
		// 从配置文本上无法可靠判断，不做猜测。
		return true
	}
	inb, _ := d["inbounds"].([]any)
	for _, it := range inb {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if t, _ := m["type"].(string); t != "redirect" || intVal(m["listen_port"]) != port {
			continue
		}
		return !isLoopbackListen(strVal(m["listen"]))
	}
	return true
}

func isLoopbackListen(addr string) bool {
	switch strings.ToLower(strings.TrimSpace(addr)) {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
}
