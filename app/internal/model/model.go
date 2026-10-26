package model

import "time"

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
