package tproxy

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Manager 负责生成并下发 nftables 规则。
// 只做 redirect（TCP），不碰 UDP，也不碰本机 output，
// 避免影响路由器上的其它服务。
type Manager struct {
	mu      sync.Mutex
	nft     string
	port    int
	ruleSet string // 落盘的规则文件，便于审计与手动恢复
	cgPath  string // 面板自身所在 cgroup，用于排除 sing-box 自己的出站，避免回环
	cgLevel int
}

func New(nftPath, ruleSetPath string, port int) *Manager {
	m := &Manager{nft: nftPath, port: port, ruleSet: ruleSetPath}
	m.cgPath, m.cgLevel = selfCgroup()
	return m
}

// selfCgroup 读取 /proc/self/cgroup（cgroup v2），返回相对路径与层级。
// sing-box 是面板的子进程，和面板在同一个 cgroup，排除它就不会把代理自己的流量再送进代理。
func selfCgroup() (string, int) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		path := strings.TrimPrefix(line, "0::")
		path = strings.Trim(path, "/")
		if path == "" {
			return "", 0
		}
		parts := strings.Split(path, "/")
		return path, len(parts)
	}
	return "", 0
}

// LocalSupported 判断当前环境能否安全地接管本机流量。
func (m *Manager) LocalSupported() bool { return m.cgPath != "" && m.cgLevel > 0 }

const tableName = "singbox"

// lanDNSPort 与模板里的 dns-lan 入站保持一致
const lanDNSPort = 5354

// build 生成完整的 nft 脚本。local_nets 是「目标为内网就放行」，
// cidrs 是「哪些来源需要被接管」。
func (m *Manager) build(cidrs []string, local, hijackDNS bool) string {
	var b strings.Builder
	b.WriteString("table inet " + tableName + " {\n")
	b.WriteString("  set local_nets {\n    type ipv4_addr\n    flags interval\n")
	b.WriteString("    elements = { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4, 240.0.0.0/4 }\n  }\n")

	b.WriteString("  chain prerouting {\n")
	b.WriteString("    type nat hook prerouting priority dstnat; policy accept;\n")
	if hijackDNS && len(cidrs) > 0 {
		src := "ip saddr { " + strings.Join(cidrs, ", ") + " }"
		b.WriteString("    " + src + " udp dport 53 redirect to :" + strconv.Itoa(lanDNSPort) + "\n")
		b.WriteString("    " + src + " tcp dport 53 redirect to :" + strconv.Itoa(lanDNSPort) + "\n")
	}
	b.WriteString("    ip daddr @local_nets return\n")
	if len(cidrs) > 0 {
		b.WriteString("    ip saddr { " + strings.Join(cidrs, ", ") + " } meta l4proto tcp redirect to :" + strconv.Itoa(m.port) + "\n")
	}
	b.WriteString("  }\n")

	// 本机流量：走 output 钩子。必须先排除面板自身 cgroup，
	// 否则 sing-box 的出站会被再次重定向回自己，形成回环。
	if local && m.cgPath != "" {
		b.WriteString("  chain output {\n")
		b.WriteString("    type nat hook output priority -100; policy accept;\n")
		b.WriteString("    socket cgroupv2 level " + strconv.Itoa(m.cgLevel) + " \"" + m.cgPath + "\" return\n")
		b.WriteString("    ip daddr @local_nets return\n")
		b.WriteString("    meta l4proto tcp redirect to :" + strconv.Itoa(m.port) + "\n")
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// Preflight 在下发前做最低限度检查，避免把整机网络搞坏。
// Apply 下发规则：先删旧表再整表重建，保证幂等。
func (m *Manager) Apply(cidrs []string, local, hijackDNS bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.guard(); err != nil {
		return err
	}
	if len(cidrs) == 0 {
		return fmt.Errorf("没有配置任何需要接管的网段")
	}
	if local && !m.LocalSupported() {
		return fmt.Errorf("无法识别面板所在的 cgroup，接管本机流量可能导致回环，已拒绝")
	}
	script := m.build(cidrs, local, hijackDNS)

	if err := os.WriteFile(m.ruleSet, []byte(script), 0o600); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, m.nft, "delete", "table", "inet", tableName).Run()

	cmd := exec.CommandContext(ctx, m.nft, "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Disable 删除整张表，流量恢复直连。
func (m *Manager) Disable() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.guard(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.nft, "delete", "table", "inet", tableName)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "No such file") || strings.Contains(msg, "does not exist") {
			return nil
		}
		return fmt.Errorf("nft: %s", msg)
	}
	return nil
}

type Status struct {
	Enabled bool     `json:"enabled"`
	Local   bool     `json:"local"`
	DNS     bool     `json:"dns"`
	Rules   int      `json:"rules"`
	CIDRs   []string `json:"cidrs"`
	Port    int      `json:"port"`
	Mode    string   `json:"mode"`
}

// Status 读回当前内核里的规则，而不是相信内存里的开关。
func (m *Manager) Status(cidrs []string) Status {
	st := Status{Enabled: false, CIDRs: cidrs, Port: m.port, Mode: "redirect"}
	if m.Supported() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, m.nft, "list", "table", "inet", tableName).Output()
		if err != nil {
			return st
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "redirect to") {
				st.Rules++
			}
			if strings.Contains(line, "hook output") {
				st.Local = true
			}
			if strings.Contains(line, "dport 53 redirect") {
				st.DNS = true
			}
		}
		st.Enabled = true
	}
	return st
}

// guard 在不支持 nftables 的平台（Windows）上拦截所有规则操作。
func (m *Manager) guard() error {
	if !m.Supported() {
		return errUnsupported
	}
	return nil
}

// Preflight 在下发前做最低限度检查，避免把整机网络搞坏。
func (m *Manager) Preflight(port int) error {
	if err := m.guard(); err != nil {
		return err
	}
	if _, err := os.Stat(m.nft); err != nil {
		return fmt.Errorf("找不到 nft 命令")
	}
	// redirect 需要 nf_nat_redirect；内核模块存在或已内建都可
	if _, err := os.Stat("/sys/module/nf_nat_redirect"); err != nil {
		if _, err2 := os.Stat("/sys/module/nft_chain_nat"); err2 != nil {
			return fmt.Errorf("内核缺少 NAT redirect 支持")
		}
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("端口不合法")
	}
	return nil
}

// DetectLAN 自动识别需要接管的网段。用 net.Interfaces 而不是 iproute2，
// 这样 Linux 与 Windows 都能工作。
// 注意：这里不能排除默认路由所在网卡——很多设备是「旁路由」形态，
// 客户端的网关指向本机，但本机的默认路由也走同一张网卡。
func DetectLAN() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	skip := []string{"docker", "incus", "veth", "tun", "singbox", "wg", "br-wan", "lo"}
	var cidrs []string
	seen := map[string]bool{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		bad := false
		name := strings.ToLower(iface.Name)
		for _, s := range skip {
			if strings.HasPrefix(name, s) {
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			cidr := ipnet.String()
			if strings.HasSuffix(cidr, "/32") {
				continue
			}
			// 归一化成网络地址（192.168.68.28/24 → 192.168.68.0/24），nft 的集合需要标准前缀
			_, netw, err := net.ParseCIDR(cidr)
			if err != nil {
				continue
			}
			cidr = netw.String()
			if !isPrivate(cidr) || seen[cidr] {
				continue
			}
			seen[cidr] = true
			cidrs = append(cidrs, cidr)
		}
	}
	return cidrs
}

// isPrivate 只接管 RFC1918 网段，公网地址一律不碰。
func isPrivate(cidr string) bool {
	ipStr := strings.SplitN(cidr, "/", 2)[0]
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	return ip.IsPrivate()
}
