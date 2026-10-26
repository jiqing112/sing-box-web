package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"singbox-panel/internal/model"
	"singbox-panel/internal/store"
)

/* ------------------------------------------------------------ render */

// RenderOpts 是需要由面板注入到配置里的运行时选项（模板里不写死）。
type RenderOpts struct {
	// DataDir 用于把模板里的相对路径（cache_file.path 等）解析成绝对路径。
	// 相对路径的解析基准是 sing-box 子进程的工作目录，跨平台、跨启动方式都不可靠。
	DataDir    string
	LanProxy   bool
	SocksPort  int
	HTTPPort   int
	User       string
	Pass       string
	TunEnabled bool
	// RoutingMode 为空或 rule 时保持模板原样；global/direct 会覆盖模板的分流规则。
	RoutingMode string
	// MixedPort / MixedListen 覆盖模板里 mixed 入站的监听端口与地址。
	// 模板是老用户手改过的文件，里面写死的 127.0.0.1:7890 不该成为改端口的唯一途径。
	MixedPort   int
	MixedListen string
}

// FromSettings 把面板设置映射成渲染选项。
// 集中一处拼装，避免以后新增字段时漏掉某个调用点（曾经有 5 处重复构造）。
func FromSettings(set model.Settings, dataDir string) RenderOpts {
	return RenderOpts{
		DataDir:     dataDir,
		LanProxy:    set.LanProxy,
		SocksPort:   set.LanSocksPort,
		HTTPPort:    set.LanHTTPPort,
		User:        set.LanUser,
		Pass:        set.LanPass,
		TunEnabled:  set.TunEnabled,
		RoutingMode: set.RoutingMode,
		MixedPort:   set.MixedPort,
		MixedListen: set.MixedListen,
	}
}

// Render 把模板与节点片段合并成完整配置。
// 面板只做 JSON 层的拼装：outbounds 由节点生成，其余字段全部来自模板。
func Render(tpl map[string]any, nodes []model.Node, opts RenderOpts) ([]byte, error) {
	var enabled []model.Node
	for _, n := range nodes {
		if n.Enabled {
			enabled = append(enabled, n)
		}
	}
	if len(enabled) == 0 {
		return nil, fmt.Errorf("没有启用的节点")
	}

	tags := make([]string, 0, len(enabled))
	for _, n := range enabled {
		tags = append(tags, n.Tag)
	}

	outbounds := make([]any, 0, len(enabled)+3)
	// 前两个是策略组：proxy（selector）与 auto（urltest）
	outbounds = append(outbounds, map[string]any{
		"type":      "selector",
		"tag":       "proxy",
		"outbounds": append([]string{"auto", "direct"}, tags...),
		"default":   "auto",
	})
	group := map[string]any{
		"type":      "urltest",
		"tag":       "auto",
		"outbounds": tags,
		"url":       "https://www.gstatic.com/generate_204",
		"interval":  "10m",
		"tolerance": 50,
	}
	outbounds = append(outbounds, group)

	for _, n := range enabled {
		ob := cloneMap(n.Outbound)
		ob["tag"] = n.Tag
		outbounds = append(outbounds, ob)
	}
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": "direct"})

	merged := cloneMap(tpl)
	if merged == nil {
		// 模板被改成 null / 非法内容时兜底，避免往 nil map 里赋值 panic
		merged = map[string]any{}
	}
	merged["outbounds"] = outbounds

	route, _ := merged["route"].(map[string]any)
	if route == nil {
		route = map[string]any{}
		merged["route"] = route
	}
	if v, _ := route["final"].(string); v == "" {
		route["final"] = "proxy"
	}
	if _, ok := route["auto_detect_interface"]; !ok {
		route["auto_detect_interface"] = true
	}

	applyRoutingMode(merged, route, opts.RoutingMode)
	migrateRuleSetHTTPClient(route)

	exp, _ := merged["experimental"].(map[string]any)
	if exp == nil {
		exp = map[string]any{}
		merged["experimental"] = exp
	}
	if _, ok := exp["clash_api"]; !ok {
		exp["clash_api"] = map[string]any{
			"external_controller": "127.0.0.1:9091",
			"default_mode":        "rule",
		}
	}
	if cache, _ := exp["cache_file"].(map[string]any); cache != nil && opts.DataDir != "" {
		if p, _ := cache["path"].(string); p != "" && !filepath.IsAbs(p) {
			// 旧模板按「数据目录固定叫 data」写成 data/cache.db；数据目录改名后
			// 相对路径不再成立，这里剥掉历史前缀再挂到真实 DataDir 下。
			legacy := "data" + string(filepath.Separator)
			if strings.HasPrefix(p, "data/") || strings.HasPrefix(p, legacy) {
				p = p[len("data/"):]
			}
			abs, err := filepath.Abs(filepath.Join(opts.DataDir, p))
			if err == nil {
				cache["path"] = abs
			}
		}
	}

	// 本机 mixed 入站：端口与监听范围以面板设置为准（模板里通常写死 127.0.0.1:7890）。
	// 只改已有 mixed 入站，不凭空新增：模板是用户可编辑的，删掉 mixed 就是真的不要它。
	if opts.MixedPort > 0 || opts.MixedListen != "" {
		inb, _ := merged["inbounds"].([]any)
		for _, it := range inb {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			if t, _ := m["type"].(string); t != "mixed" {
				continue
			}
			if opts.MixedPort > 0 {
				m["listen_port"] = opts.MixedPort
			}
			if opts.MixedListen != "" {
				m["listen"] = opts.MixedListen
			}
		}
	}

	// 局域网独立端口：socks 与 http 分开，便于客户端按需接入。
	// 用户名和密码都填写才启用鉴权；两者都留空则为开放代理（无鉴权）。
	if opts.LanProxy {
		inb, _ := merged["inbounds"].([]any)
		withAuth := opts.User != "" && opts.Pass != ""
		var users []any
		if withAuth {
			users = []any{map[string]any{"username": opts.User, "password": opts.Pass}}
		}
		socks := map[string]any{
			"type": "socks", "tag": "socks-lan",
			"listen": "0.0.0.0", "listen_port": opts.SocksPort,
		}
		http := map[string]any{
			"type": "http", "tag": "http-lan",
			"listen": "0.0.0.0", "listen_port": opts.HTTPPort,
		}
		if withAuth {
			socks["users"] = users
			http["users"] = users
		}
		inb = append(inb, socks, http)
		merged["inbounds"] = inb
	}

	// TUN 入站：整网卡接管（Windows 用 wintun，Linux 用 tun 设备）。
	// auto_route 让系统默认路由指向 TUN；strict_route 关闭以避免
	// Windows 上 VPN/虚拟网卡共存时把自身回环流量也吞掉。
	if opts.TunEnabled {
		inb, _ := merged["inbounds"].([]any)
		inb = append(inb, map[string]any{
			"type":           "tun",
			"tag":            "tun-in",
			"interface_name": "singbox-tun",
			"address":        []string{"172.19.0.1/30"},
			"mtu":            9000,
			"auto_route":     true,
			"strict_route":   false,
			"stack":          "mixed",
		})
		merged["inbounds"] = inb
	}

	return json.MarshalIndent(merged, "", "  ")
}

// applyRoutingMode 用面板设置覆盖模板里的分流策略。
// rule 保持模板原样；global/direct 只保留必要的动作规则，避免模板里
// 遗留的 CN 直连规则让「全局代理」名不副实。内网直连在任何模式下都保留，
// 因为私有地址无法经远端节点转发。
func applyRoutingMode(merged, route map[string]any, mode string) {
	switch mode {
	case "global":
		route["rules"] = []any{
			map[string]any{"action": "sniff"},
			map[string]any{"protocol": "dns", "action": "hijack-dns"},
			map[string]any{"ip_is_private": true, "outbound": "direct"},
		}
		route["final"] = "proxy"
		if dns, _ := merged["dns"].(map[string]any); dns != nil {
			dns["rules"] = []any{}
			dns["final"] = "remote"
		}
	case "direct":
		route["rules"] = []any{
			map[string]any{"action": "sniff"},
			map[string]any{"protocol": "dns", "action": "hijack-dns"},
		}
		route["final"] = "direct"
		if dns, _ := merged["dns"].(map[string]any); dns != nil {
			dns["rules"] = []any{}
			dns["final"] = "local"
		}
	}
}

// migrateRuleSetHTTPClient 把 sing-box 1.14 已弃用的 download_detour 改写成 http_client。
// 这个字段 1.16 会被移除：到那时带 download_detour 的配置会直接 check 失败，
// 核心起不来 → 崩溃循环 → 撤销透明代理 → 整网断网，所以在渲染层提前统一改写。
// 代价是最低核心版本要求变为 1.14（模板里的 default_domain_resolver 本来就要求 1.11+）。
func migrateRuleSetHTTPClient(route map[string]any) {
	sets, _ := route["rule_set"].([]any)
	for _, it := range sets {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		detour, _ := m["download_detour"].(string)
		if detour == "" {
			continue
		}
		if _, ok := m["http_client"]; !ok {
			m["http_client"] = map[string]any{"detour": detour}
		}
		delete(m, "download_detour")
	}
}

func cloneMap(in map[string]any) map[string]any {
	b, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

/* ------------------------------------------------------------ check */

type CheckResult struct {
	OK      bool   `json:"ok"`
	Output  string `json:"output"`
	Elapsed int64  `json:"elapsed_ms"`
}

// Check 调用 sing-box check 做唯一权威校验。
// check 是独立进程，单独限制内存，避免与运行中的 core 叠加。
func Check(ctx context.Context, bin string, raw []byte) CheckResult {
	start := time.Now()
	f, err := os.CreateTemp("", "sb-check-*.json")
	if err != nil {
		return CheckResult{OK: false, Output: err.Error()}
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return CheckResult{OK: false, Output: err.Error()}
	}
	f.Close()

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, bin, "check", "-c", f.Name())
	cmd.Env = append(os.Environ(), "GOMEMLIMIT=64MiB")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	res := CheckResult{
		OK:      err == nil,
		Output:  text,
		Elapsed: time.Since(start).Milliseconds(),
	}
	if err != nil && text == "" {
		res.Output = err.Error()
	}
	return res
}

/* ------------------------------------------------------------ apply */

type Applier struct {
	store  *store.Store
	bin    string
	mu     sync.Mutex
	onSwap func() error // 重启核心
	probe  func(ctx context.Context) error
}

func NewApplier(st *store.Store, bin string) *Applier {
	return &Applier{store: st, bin: bin}
}

func (a *Applier) SetReload(fn func() error)               { a.onSwap = fn }
func (a *Applier) SetProbe(fn func(context.Context) error) { a.probe = fn }

// Apply 是唯一的写配置入口：check → 快照 → 原子写 → 重启 → 探活 → 失败回滚。
func (a *Applier) Apply(ctx context.Context, raw []byte, note string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !json.Valid(raw) {
		return fmt.Errorf("配置不是合法 JSON")
	}
	if res := Check(ctx, a.bin, raw); !res.OK {
		return &CheckError{Detail: res.Output}
	}

	prev, _ := a.store.Config()
	if len(prev) > 0 {
		if _, err := a.store.TakeSnapshot("应用前备份 · " + note); err != nil {
			return fmt.Errorf("snapshot: %w", err)
		}
	}
	if err := a.store.WriteConfig(raw); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if a.onSwap != nil {
		if err := a.onSwap(); err != nil {
			return a.rollback(ctx, prev, fmt.Errorf("restart: %w", err))
		}
	}
	if a.probe != nil {
		// Windows 上重启核心有两个慢源：TUN 冷启动要建虚拟网卡 + 改路由；
		// cache.db 文件锁偶尔要等 9 秒才 FATAL，再靠 supervisor 的自动重启兜底。
		// 探活窗口必须盖过「失败→自动重启→成功」的全过程，否则会误判回滚。
		pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		if err := a.probe(pctx); err != nil {
			return a.rollback(ctx, prev, fmt.Errorf("probe: %w", err))
		}
	}
	a.store.BumpConfigVersion()
	return nil
}

func (a *Applier) rollback(ctx context.Context, prev []byte, cause error) error {
	if len(prev) == 0 {
		return cause
	}
	if err := a.store.WriteConfig(prev); err != nil {
		return fmt.Errorf("%v; rollback write failed: %w", cause, err)
	}
	if a.onSwap != nil {
		_ = a.onSwap()
	}
	return fmt.Errorf("%v（已回滚到上一份配置）", cause)
}

// Rollback 回滚到指定快照版本。
func (a *Applier) Rollback(ctx context.Context, version int) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	raw, err := a.store.SnapshotConfig(version)
	if err != nil {
		return err
	}
	if res := Check(ctx, a.bin, raw); !res.OK {
		return &CheckError{Detail: res.Output}
	}
	cur, _ := a.store.Config()
	if len(cur) > 0 {
		_, _ = a.store.TakeSnapshot("回滚前自动备份")
	}
	if err := a.store.WriteConfig(raw); err != nil {
		return err
	}
	if a.onSwap != nil {
		if err := a.onSwap(); err != nil {
			return a.rollback(ctx, cur, err)
		}
	}
	if a.probe != nil {
		pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		if err := a.probe(pctx); err != nil {
			return a.rollback(ctx, cur, fmt.Errorf("probe: %w", err))
		}
	}
	a.store.BumpConfigVersion()
	return nil
}

type CheckError struct{ Detail string }

func (e *CheckError) Error() string { return "sing-box check failed: " + e.Detail }
