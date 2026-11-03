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
	cgOK    bool // nft 是否支持 socket cgroupv2 匹配（nftables >= 1.0）
}

func New(nftPath, ruleSetPath string, port int) *Manager {
	m := &Manager{nft: nftPath, port: port, ruleSet: ruleSetPath}
	m.cgPath, m.cgLevel = selfCgroup()
	m.cgOK = nftSupportsCgroupV2(nftPath)
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

// Mark 是本机透明代理用来标记「代理自己的出站连接」的 fwmark。
// 有 cgroupv2 匹配能力时不用它；nftables 太老（麒麟 V10 自带 0.9.3）时靠它防回环。
// 必须与配置里的 route.default_mark 保持一致，否则核心出站不会被标记，
// 流量会被重定向回自己形成回环。
const Mark = 0x2023

// LocalSupported 判断当前环境能否安全地接管本机流量。
// 两条路：nft 支持 socket cgroupv2（nftables >= 1.0）直接按 cgroup 排除自己；
// 否则退化成 fwmark —— 需要配置里有 route.default_mark（渲染时自动加）。
func (m *Manager) LocalSupported() bool {
	if m.CgroupSupported() {
		return true
	}
	return m.MarkSupported()
}

// CgroupSupported 表示能用 socket cgroupv2 排除代理自身的出站。
func (m *Manager) CgroupSupported() bool {
	return m.cgOK && m.cgPath != "" && m.cgLevel > 0
}

// MarkSupported 表示能退化成 fwmark 方案。meta mark 自 nftables 0.9 起就支持，
// 所以只要 nft 命令在就算可用；标记本身由核心（root）设置。
func (m *Manager) MarkSupported() bool { return m.nft != "" }

// nftSupportsCgroupV2 判断 nft 是否支持 socket cgroupv2 匹配。
// 这个语法是 nftables 1.0.0 引入的，0.9.x 会直接报
// "syntax error, unexpected string, expecting transparent or mark"。
// 用版本号判断而不是 `nft -c` 试解析：试解析需要 CAP_NET_ADMIN，
// 非 root 跑面板时会一律失败，反而误判成不支持。
func nftSupportsCgroupV2(nftPath string) bool {
	if nftPath == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, nftPath, "--version").Output()
	if err != nil {
		return false
	}
	return nftMajorVersion(string(out)) >= 1
}

// nftMajorVersion 从 "nftables v0.9.3 (Topsy)" 里取主版本号。
func nftMajorVersion(s string) int {
	i := strings.IndexByte(s, 'v')
	if i < 0 {
		return 0
	}
	rest := s[i+1:]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:j])
	if err != nil {
		return 0
	}
	return n
}

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

	// 本机流量：走 output 钩子。必须先排除代理自己的出站，
	// 否则 sing-box 的连接会被再次重定向回自己，形成回环。
	// 优先用 cgroupv2 精确匹配（nftables >= 1.0）；
	// 老版本 nftables（麒麟 V10 自带 0.9.3）没有这个语法，退化成 fwmark：
	// 核心启动时会给自己的出站套上 route.default_mark，这里按 mark 放行。
	if local {
		b.WriteString("  chain output {\n")
		b.WriteString("    type nat hook output priority -100; policy accept;\n")
		if m.CgroupSupported() {
			b.WriteString("    socket cgroupv2 level " + strconv.Itoa(m.cgLevel) + " \"" + m.cgPath + "\" return\n")
		} else {
			b.WriteString("    meta mark " + strconv.Itoa(Mark) + " return\n")
		}
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
		return fmt.Errorf("无法识别面板所在的 cgroup，且 nftables 不可用，接管本机流量可能回环，已拒绝")
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
	if !m.supportsRedirect() {
		return fmt.Errorf("内核缺少 NAT redirect 支持")
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("端口不合法")
	}
	return nil
}

// supportsRedirect 判断内核能否做 NAT redirect（transparent proxy 的基础）。
//
// 不能只看 /sys/module/nf_nat_redirect：内核把它编进去（CONFIG_NF_NAT_REDIRECT=y）
// 时不会生成这个目录，而 Debian/Ubuntu 官方内核正是这么编的 —— 只看目录会把
// 完全可用的机器误判成「内核缺少 NAT redirect 支持」，透明代理直接开不起来。
//
// 所以先走 /sys/module 的快路径（模块形式），不命中就用 `nft -c` 真解析一遍：
// check 模式不落规则，但会完整走内核的能力校验，模块缺失会当场报错。
func (m *Manager) supportsRedirect() bool {
	if _, err := os.Stat("/sys/module/nf_nat_redirect"); err == nil {
		return true
	}
	if _, err := os.Stat("/sys/module/nft_chain_nat"); err == nil {
		return true
	}
	const probe = `table inet tprobe_redirect {
  chain c {
    type nat hook prerouting priority dstnat; policy accept;
    ip saddr 127.0.0.1 meta l4proto tcp redirect to :1
  }
}
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.nft, "-c", "-f", "-")
	cmd.Stdin = strings.NewReader(probe)
	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = out
		return false
	}
	return true
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
