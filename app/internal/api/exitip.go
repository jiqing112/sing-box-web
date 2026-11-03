// 出口 IP：面板经内核 mixed 入站访问 IP 回显服务实测当前出口。
// 与 Rust 版 /api/v1/exitip 行为一致：核心未运行或全部回显源失败时 ip 为 null，
// 结果带 60 秒缓存（失败 15 秒），避免首页轮询打爆回显服务。
package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	exitIPTTL     = 60 * time.Second
	exitIPFailTTL = 15 * time.Second
	exitIPTimeout = 6 * time.Second
)

// 测量结果缓存：ip 为空表示最近一次测量失败。
type exitIPResult struct {
	at  time.Time
	ip  string
	via string
}

func (s *Server) exitIP(w http.ResponseWriter, r *http.Request) {
	s.exitMu.Lock()
	defer s.exitMu.Unlock()

	ttl := exitIPTTL
	if s.exitRes.ip == "" {
		ttl = exitIPFailTTL
	}
	if !s.exitRes.at.IsZero() && time.Since(s.exitRes.at) < ttl {
		writeExitIP(w, s.exitRes.ip, s.exitRes.via)
		return
	}
	ip, via := s.measureExitIP()
	s.exitRes = exitIPResult{at: time.Now(), ip: ip, via: via}
	writeExitIP(w, ip, via)
}

func writeExitIP(w http.ResponseWriter, ip, via string) {
	if ip == "" {
		writeJSON(w, 200, map[string]any{"ip": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"ip": ip, "via": via})
}

func (s *Server) measureExitIP() (string, string) {
	if running, _, _, _ := s.sup.Status(); !running {
		return "", ""
	}
	port := s.store.Settings().MixedPort
	if port <= 0 {
		port = 7890
	}
	// 优先经 mixed 入站：测得的就是当前路由策略下真正的出口。
	if ip, err := fetchExitIP(fmt.Sprintf("http://127.0.0.1:%d", port)); err == nil {
		return ip, "proxy"
	}
	// mixed 不可用时退化为直连测量：rule 模式下测的是运营商出口；
	// 开了「接管本机流量」时其实仍经内核。
	if ip, err := fetchExitIP(""); err == nil {
		return ip, "direct"
	}
	return "", ""
}

// fetchExitIP 依次询问 IP 回显服务；proxy 为空表示直连。
// 单源 5 秒超时，全部失败返回错误。
func fetchExitIP(proxy string) (string, error) {
	tr := &http.Transport{}
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return "", err
		}
		tr.Proxy = http.ProxyURL(u)
	} else {
		tr.Proxy = nil
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	type source struct {
		url     string
		isTrace bool
	}
	sources := []source{
		{"https://api.ip.sb/ip", false},
		{"https://api.ipify.org", false},
		{"https://www.cloudflare.com/cdn-cgi/trace", true},
	}
	for _, src := range sources {
		ctx, cancel := context.WithTimeout(context.Background(), exitIPTimeout)
		ip, err := fetchOne(ctx, client, src.url, src.isTrace)
		cancel()
		if err == nil && ip != "" {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no exit ip source answered")
}

func fetchOne(ctx context.Context, client *http.Client, raw string, isTrace bool) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	text := strings.TrimSpace(string(buf[:n]))
	var ip string
	if isTrace {
		for _, line := range strings.Split(text, "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ip="); ok {
				ip = strings.TrimSpace(v)
				break
			}
		}
	} else {
		ip = strings.TrimSpace(text)
	}
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("not an ip: %q", ip)
	}
	return ip, nil
}
