package model

import (
	"strings"
	"time"
)

// Subscription 订阅元数据。原始响应体不入库，只保留解析后的节点。
type Subscription struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Auto bool   `json:"auto"`
	// 自动更新间隔（小时）；0 表示旧数据未设置，按 24 小时处理
	IntervalHours int       `json:"interval_hours"`
	NodeCount     int       `json:"node_count"`
	UpdatedAt     time.Time `json:"updated_at"`
	UsageUsed     int64     `json:"usage_used"`
	UsageTotal    int64     `json:"usage_total"`
	Expire        int64     `json:"expire"`
	LastError     string    `json:"last_error,omitempty"`
}

// Node 一个可下发的出站节点。Outbound 是原生 sing-box outbound 片段，
// 面板不做字段映射，只负责搬运。
type Node struct {
	ID       string         `json:"id"`
	Tag      string         `json:"tag"`
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Server   string         `json:"server"`
	Port     int            `json:"port"`
	Region   string         `json:"region"`
	SubID    string         `json:"sub_id"`
	Enabled  bool           `json:"enabled"`
	Outbound map[string]any `json:"outbound"`
}

// RuleSetDetourTag 是「规则集下载」专用出站的 tag。
//
// 为什么需要它：sing-box 的 rule_set 支持 http_client.detour 指定下载走哪个出站，
// 但不允许指向"空 direct 出站"（报 detour to an empty direct outbound makes no sense），
// 而指向 proxy 又会让「节点全挂 → 下不到规则集 → 核心 FATAL 起不来」变成死锁。
// 所以渲染时额外生成一个带 tcp_fast_open 的 direct 出站专门用于下载，
// 它不进 selector，用户选不到。
const RuleSetDetourTag = "rule-set"

// NormalizeOutboundType 把订阅里常见的简写归一成 sing-box 的 outbound 类型名。
//
// v2ray / base64 订阅的 URI scheme 写的是 ss，但 sing-box 只认 shadowsocks；
// 直接拿 scheme 当 type 会让 `sing-box check` 报
// "unknown outbound type: ss" 并拒绝启动。
// Clash 格式的订阅本来就写 shadowsocks，两种来源归一后一致。
//
// 渲染时也要再调一次：老版本存进库里的节点仍是 type=ss，
// 升级后不重新拉订阅也必须能跑起来。
func NormalizeOutboundType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "ss", "shadowsocks":
		return "shadowsocks"
	case "hy2", "hysteria2":
		return "hysteria2"
	case "socks5", "socks":
		return "socks"
	default:
		return strings.ToLower(strings.TrimSpace(t))
	}
}

// Snapshot 配置快照。默认保留 3 份。
type Snapshot struct {
	Version int       `json:"version"`
	Time    time.Time `json:"time"`
	Note    string    `json:"note"`
	Size    int       `json:"size"`
	File    string    `json:"file"`
}

type Settings struct {
	Listen       string `json:"listen"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	// Engine 是当前驱动的内核：sing-box（默认）或 mihomo。
	// 空值表示老版本 settings.json，读取时统一按 sing-box 处理。
	Engine string `json:"engine"`
	// ClashAddr 是内核的 Clash API 监听地址，面板靠它读状态、测速、拉连接。
	// 默认 127.0.0.1:9091；本机已有其它 Clash 系客户端占用时可用 -clash 改。
	ClashAddr string `json:"clash_addr"`
	// 面板内开关：为 true 时免密码访问，账号密码原样保留，重新启用登录时可恢复
	LoginDisabled bool   `json:"login_disabled"`
	KeepSnapshots int    `json:"keep_snapshots"`
	DelayTTL      int    `json:"delay_ttl"`
	DelayWorkers  int    `json:"delay_workers"`
	TproxyMode    string `json:"tproxy_mode"`
	TproxyEnabled bool   `json:"tproxy_enabled"`
	TproxyLocal   bool   `json:"tproxy_local"`
	TproxyDNS     bool   `json:"tproxy_dns"`
	TunEnabled    bool   `json:"tun_enabled"`
	// rule = 国内直连其余走节点；global = 全部走节点；direct = 全部直连
	RoutingMode string `json:"routing_mode"`

	// 局域网带鉴权的独立代理端口
	LanProxy     bool     `json:"lan_proxy"`
	LanSocksPort int      `json:"lan_socks_port"`
	LanHTTPPort  int      `json:"lan_http_port"`
	LanUser      string   `json:"lan_user"`
	LanPass      string   `json:"lan_pass"`
	TproxyPort   int      `json:"tproxy_port"`
	LanCIDRs     []string `json:"lan_cidrs"`
	// 本机 mixed 入站（系统代理用）：端口与监听范围可在面板里改，
	// 渲染时覆盖模板里写死的 127.0.0.1:7890。
	MixedPort   int    `json:"mixed_port"`
	MixedListen string `json:"mixed_listen"`
	// 面板启动时是否自动拉起 sing-box；nil 表示未设置（视为 true，兼容旧配置）
	AutostartCore *bool  `json:"autostart_core,omitempty"`
	ConfigVersion int    `json:"config_version"`
	LastCheckAt   string `json:"last_check_at"`
	LastCheckOK   bool   `json:"last_check_ok"`
	LanIf         string `json:"lan_if"`
}

func DefaultSettings() Settings {
	return Settings{
		Listen:        "127.0.0.1:9090",
		Username:      "admin",
		Engine:        "sing-box",
		ClashAddr:     "127.0.0.1:9091",
		KeepSnapshots: 3,
		DelayTTL:      300,
		DelayWorkers:  3,
		TproxyMode:    "redirect",
	}
}

type LogLine struct {
	At  time.Time `json:"at"`
	Raw string    `json:"raw"`
}

type Status struct {
	Running     bool      `json:"running"`
	PID         int       `json:"pid"`
	StartedAt   time.Time `json:"started_at"`
	Uptime      int64     `json:"uptime"`
	Version     string    `json:"version"`
	ConfigVer   int       `json:"config_version"`
	Nodes       int       `json:"nodes"`
	Enabled     int       `json:"enabled"`
	Rules       int       `json:"rules"`
	RSS         int64     `json:"rss"`
	Listen      string    `json:"listen"`
	Tproxy      string    `json:"tproxy"`
	Subs        int       `json:"subs"`
	LastCheck   string    `json:"last_check"`
	LastCheckOK bool      `json:"last_check_ok"`
}
