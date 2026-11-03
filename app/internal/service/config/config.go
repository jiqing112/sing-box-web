package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"singbox-panel/internal/model"
	"singbox-panel/internal/service/engine"
	"singbox-panel/internal/service/tproxy"
	"singbox-panel/internal/store"
)

/* ------------------------------------------------------------ render */

// RenderOpts 是需要由面板注入到配置里的运行时选项（模板里不写死）。
type RenderOpts struct {
	// Engine 决定渲染成 sing-box JSON 还是 mihomo YAML。
	// 空值按 sing-box 处理，保持与旧调用点一致。
	Engine string
	// DataDir 用于把模板里的相对路径（cache_file.path 等）解析成绝对路径。
	// 相对路径的解析基准是核心子进程的工作目录，跨平台、跨启动方式都不可靠。
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
	// TproxyPort 是透明代理 redirect 入站的端口；mihomo 用 redir-port 表达，
	// sing-box 的端口写在模板里（这里只是给 mihomo 用）。
	TproxyPort int
	// ClashAddr 是面板要连的 Clash API 地址，两个内核都渲染进配置里，
	// 保证「面板连的地址」与「内核监听的地址」永远一致。
	ClashAddr string
}

// FromSettings 把面板设置映射成渲染选项。
// 集中一处拼装，避免以后新增字段时漏掉某个调用点（曾经有 5 处重复构造）。
func FromSettings(set model.Settings, dataDir string) RenderOpts {
	return RenderOpts{
		Engine:      engine.Normalize(set.Engine),
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
		TproxyPort:  set.TproxyPort,
		ClashAddr:   set.ClashAddr,
	}
}

// DefaultClashAddr 是内核 Clash API 的默认监听地址。
const DefaultClashAddr = "127.0.0.1:9091"

// ClashController 返回要渲染进配置的控制器地址，未配置时用默认值。
func (o RenderOpts) ClashController() string {
	if o.ClashAddr != "" {
		return o.ClashAddr
	}
	return DefaultClashAddr
}

// DecodeTemplate 按引擎解码模板：sing-box 是 JSON，mihomo 是 YAML。
func DecodeTemplate(raw []byte, eng string) (map[string]any, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	if engine.IsMihomo(eng) {
		var m map[string]any
		if err := yaml.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("模板不是合法 YAML: %w", err)
		}
		if m == nil {
			m = map[string]any{}
		}
		return stringifyKeys(m).(map[string]any), nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("模板不是合法 JSON: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

// stringifyKeys 把 yaml 解出来的 map[any]any 统一成 map[string]any，
// 否则后续 json / yaml 序列化都会因为键类型不对而失败。
func stringifyKeys(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = stringifyKeys(val)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = stringifyKeys(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = stringifyKeys(t[i])
		}
		return t
	}
	return v
}

// RenderBytes 是给「手里只有模板原文」的调用方用的入口（API 层都是这种）。
func RenderBytes(tplRaw []byte, nodes []model.Node, opts RenderOpts) ([]byte, error) {
	tpl, err := DecodeTemplate(tplRaw, opts.Engine)
	if err != nil {
		return nil, err
	}
	return Render(tpl, nodes, opts)
}

// Render 把模板与节点片段合并成完整配置。
// 面板只做结构层的拼装：出站/代理由节点生成，其余字段全部来自模板。
func Render(tpl map[string]any, nodes []model.Node, opts RenderOpts) ([]byte, error) {
	if engine.IsMihomo(opts.Engine) {
		return RenderClash(tpl, nodes, opts)
	}
	return renderSingBox(tpl, nodes, opts)
}

func renderSingBox(tpl map[string]any, nodes []model.Node, opts RenderOpts) ([]byte, error) {
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
		// 老版本把 ss:// 的 scheme 直接存成 type=ss，sing-box 不认；
		// 这里再归一一次，升级后不重新拉订阅也能跑。
		if t := strVal(ob["type"]); t != "" {
			ob["type"] = model.NormalizeOutboundType(t)
		}
		outbounds = append(outbounds, ob)
	}
	outbounds = append(outbounds, map[string]any{"type": "direct", "tag": "direct"})
	// 规则集下载专用出站（见 model.RuleSetDetourTag 的说明）。
	// 加 tcp_fast_open 是为了让这个 direct 不是"空 direct"——sing-box 拒绝
	// 把 http_client.detour 指向空 direct。它不进 selector，用户选不到。
	outbounds = append(outbounds, map[string]any{
		"type": "direct", "tag": model.RuleSetDetourTag, "tcp_fast_open": true,
	})

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
	// 给核心自己的出站连接打上 fwmark：「接管本机流量」的 nft 规则靠它
	// 把代理自身的流量放行掉，否则会被重定向回自己形成回环。
	// 老版本 nftables（麒麟 V10 自带 0.9.3）没有 socket cgroupv2 匹配，
	// 只能走这条路；新版 nft 有 cgroup 匹配，这个标记留着也无害。
	// 只有 root 能设 SO_MARK，非 root 时跳过，避免核心连不上网。
	if runtime.GOOS == "linux" && os.Geteuid() == 0 {
		if _, ok := route["default_mark"]; !ok {
			route["default_mark"] = tproxy.Mark
		}
	}

	applyRoutingMode(merged, route, opts.RoutingMode)
	migrateRuleSetHTTPClient(route)

	exp, _ := merged["experimental"].(map[string]any)
	if exp == nil {
		exp = map[string]any{}
		merged["experimental"] = exp
	}
	// clash_api 既要在模板缺失时补上，也要保证控制器地址与面板配置一致：
	// 地址对不上时面板读不到状态、测速全失败，表现为「核心在跑但面板像死的」。
	clashAPI, _ := exp["clash_api"].(map[string]any)
	if clashAPI == nil {
		clashAPI = map[string]any{"default_mode": "rule"}
		exp["clash_api"] = clashAPI
	}
	if _, ok := clashAPI["default_mode"]; !ok {
		clashAPI["default_mode"] = "rule"
	}
	clashAPI["external_controller"] = opts.ClashController()
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

// Check 调用内核自己的校验命令做唯一权威校验：
// sing-box 用 `check -c`，mihomo 用 `-t -d <home> -f`。
// check 是独立进程，单独限制内存，避免与运行中的 core 叠加。
func Check(ctx context.Context, bin, eng, dataDir string, raw []byte) CheckResult {
	start := time.Now()
	home := engine.Home(dataDir, eng)
	ext := engine.Ext(eng)
	f, err := os.CreateTemp("", "panel-check-*"+ext)
	if err != nil {
		return CheckResult{OK: false, Output: err.Error()}
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return CheckResult{OK: false, Output: err.Error()}
	}
	f.Close()

	// mihomo 校验时如果工作目录里没有 geo 数据（GeoSite.dat / geoip.metadb），
	// 会顺带下载约 12 MB，实测 31 秒。面板会在后台预取（见 geodata 包），
	// 正常情况下这里几百毫秒就返回；90 秒是给「预取失败、mihomo 自己下载」
	// 留的兜底余量，不能去掉，否则慢线路下切换会误判成失败。
	timeout := 15 * time.Second
	if engine.IsMihomo(eng) {
		timeout = 90 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, bin, engine.CheckArgs(eng, f.Name(), home)...)
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
	mu     sync.Mutex
	bin    string
	eng    string
	home   string
	onSwap func() error // 重启核心
	probe  func(ctx context.Context) error
}

func NewApplier(st *store.Store, bin string) *Applier {
	return &Applier{store: st, bin: bin, eng: st.Engine(), home: st.Dir()}
}

// Home 是内核的工作目录（mihomo 的 -d），由数据目录与引擎共同决定。
func (a *Applier) Home() string { return engine.Home(a.home, a.eng) }

// SetEngine 在切换内核时同步校验/启动用的二进制与参数格式。
func (a *Applier) SetEngine(bin, eng string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bin = bin
	a.eng = engine.Normalize(eng)
	a.home = a.store.Dir()
}

func (a *Applier) SetReload(fn func() error)               { a.onSwap = fn }
func (a *Applier) SetProbe(fn func(context.Context) error) { a.probe = fn }

// Apply 是唯一的写配置入口：check → 快照 → 原子写 → 重启 → 探活 → 失败回滚。
func (a *Applier) Apply(ctx context.Context, raw []byte, note string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if err := ValidateDoc(raw, a.eng); err != nil {
		return err
	}
	if res := Check(ctx, a.bin, a.eng, a.home, raw); !res.OK {
		return &CheckError{Detail: res.Output, Engine: a.eng}
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
	if err := ValidateDoc(raw, a.eng); err != nil {
		return err
	}
	if res := Check(ctx, a.bin, a.eng, a.home, raw); !res.OK {
		return &CheckError{Detail: res.Output, Engine: a.eng}
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

type CheckError struct {
	Detail string
	Engine string
}

func (e *CheckError) Error() string {
	return engine.Label(e.Engine) + " check failed: " + e.Detail
}
