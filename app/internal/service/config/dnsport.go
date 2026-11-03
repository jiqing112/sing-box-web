package config

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
)

// LocalDNSTag 是模板里「本机 DNS」入站的 tag：监听 127.0.0.1:53，
// 用户把 /etc/resolv.conf 指过来即可绕开污染。
const LocalDNSTag = "dns-in"

// LocalDNSPort 是模板里本机 DNS 入站的默认端口。
const LocalDNSPort = 53

// dnsPortFallbacks 是 53 被占用时的备选端口，按顺序取第一个可用的。
// 选这几个是因为它们不在常见服务的默认端口里，且都大于 1024，
// 非 root 运行面板时也能绑上。
var dnsPortFallbacks = []int{5353, 5335, 15353, 25353}

// portUsable 判断 host:port 能否被当前进程绑定。
// 入站是 direct 类型，TCP/UDP 都会监听，所以两个协议都要能绑上才算可用。
// 非 root 绑 53 会返回 EACCES，这里同样视为不可用——核心用同样的身份启动，
// 它也一样绑不上，提前降级比等核心 FATAL 更好。
func portUsable(host string, port int) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return false
	}
	_ = pc.Close()
	return true
}

// LocalDNSPortOf 从一份 sing-box 配置里读出本机 DNS 入站当前用的端口。
// 读不到返回 0。面板用它判断「某个端口是不是自己核心占着的」。
func LocalDNSPortOf(raw []byte) int {
	if len(raw) == 0 {
		return 0
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return 0
	}
	inbounds, ok := cfg["inbounds"].([]any)
	if !ok {
		return 0
	}
	for _, item := range inbounds {
		in, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if in["tag"] != LocalDNSTag && !(in["type"] == "direct" && isPort(in, LocalDNSPort)) {
			continue
		}
		if p, ok := inboundPort(in); ok {
			return p
		}
	}
	return 0
}

// RepairLocalDNS 检查配置里的本机 DNS 入站（dns-in）端口是否还能绑上。
// 麒麟等发行版自带 dnsmasq 占着 127.0.0.1:53，核心会以
// "listen tcp 127.0.0.1:53: bind: address already in use" 直接 FATAL，
// 进而进入崩溃循环、面板放弃重启——用户看到的是「装好了但核心一直起不来」。
//
// 端口被别的进程占用（或当前进程无权绑定）时，自动换到候选端口里第一个可用的；
// 全都不可用就把这个入站摘掉（核心照常工作，只是本机 DNS 接管不可用）。
// 返回修改后的配置和一句给用户看的说明，没改动时说明为空。
//
// ownPort 是「当前正在运行的核心已经占着的端口」：端口探活没法区分
// 「被自己核心占着」和「被 dnsmasq 占着」，必须由调用方告知。
// 传 0 表示核心没在跑。渲染时如果不传，核心跑着的时候会误判成冲突，
// 于是每次渲染都把端口改一遍，应用配置时还会因为重启核心失败而回滚。
func RepairLocalDNS(raw []byte, ownPort int) ([]byte, string) {
	if len(raw) == 0 {
		return raw, ""
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		// mihomo 的 YAML 或用户手写的非 JSON 内容，不归这里管
		return raw, ""
	}
	inbounds, ok := cfg["inbounds"].([]any)
	if !ok {
		return raw, ""
	}
	for i, item := range inbounds {
		in, ok := item.(map[string]any)
		if !ok {
			continue
		}
		port, ok := inboundPort(in)
		if !ok {
			continue
		}
		if in["tag"] != LocalDNSTag && !(in["type"] == "direct" && port == LocalDNSPort) {
			continue
		}
		host, _ := in["listen"].(string)
		if host == "" {
			host = "127.0.0.1"
		}
		// 自己核心占着的端口，或者本来就空闲：不用动
		if (ownPort > 0 && port == ownPort) || portUsable(host, port) {
			return raw, ""
		}

		// 优先复用核心当前占着的端口（渲染时模板里写的还是 53，
		// 但实际生效的是降级后的端口，别每渲染一次就换一个）
		for _, p := range append([]int{ownPort}, dnsPortFallbacks...) {
			if p <= 0 || p == port {
				continue
			}
			if !(p == ownPort || portUsable(host, p)) {
				continue
			}
			in["listen_port"] = p
			out, err := marshalConfig(cfg)
			if err != nil {
				return raw, ""
			}
			return out, fmt.Sprintf("本机 DNS 端口 %d 已被占用，已自动改用 %d（如需恢复请释放该端口后重启面板）", port, p)
		}

		cfg["inbounds"] = append(inbounds[:i:i], inbounds[i+1:]...)
		out, err := marshalConfig(cfg)
		if err != nil {
			return raw, ""
		}
		return out, fmt.Sprintf("本机 DNS 端口 %d 已被占用且没有可用备用端口，本次已跳过该入站", port)
	}
	return raw, ""
}

func inboundPort(in map[string]any) (int, bool) {
	switch v := in["listen_port"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}

func isPort(in map[string]any, want int) bool {
	p, ok := inboundPort(in)
	return ok && p == want
}

func marshalConfig(cfg map[string]any) ([]byte, error) {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
