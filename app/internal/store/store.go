package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"singbox-panel/internal/model"
	"singbox-panel/internal/service/engine"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	dir string

	mu       sync.RWMutex
	subs     []model.Subscription
	settings model.Settings
	// engine 决定 config/template 的文件名（JSON 还是 YAML）。
	// 单独存一份是为了让 ConfigPath() 不必再去拿 Settings() 的读锁。
	engine string
}

func Open(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "snapshots")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	s := &Store{dir: dir, settings: model.DefaultSettings(), engine: engine.SingBox}

	if err := s.read(filepath.Join(dir, "settings.json"), &s.settings); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	// 兼容旧版 settings.json：缺字段时补默认值
	s.settings.Engine = engine.Normalize(s.settings.Engine)
	s.engine = s.settings.Engine
	if s.settings.KeepSnapshots <= 0 {
		s.settings.KeepSnapshots = 3
	}
	if s.settings.DelayWorkers <= 0 {
		s.settings.DelayWorkers = 3
	}
	if s.settings.DelayTTL <= 0 {
		s.settings.DelayTTL = 300
	}
	if s.settings.TproxyPort <= 0 {
		s.settings.TproxyPort = 12345
	}
	if s.settings.ClashAddr == "" {
		s.settings.ClashAddr = "127.0.0.1:9091"
	}
	if s.settings.TproxyMode == "" {
		s.settings.TproxyMode = "redirect"
	}
	if s.settings.RoutingMode == "" {
		s.settings.RoutingMode = "rule"
	}
	if s.settings.LanSocksPort <= 0 {
		s.settings.LanSocksPort = 1080
	}
	if s.settings.LanHTTPPort <= 0 {
		s.settings.LanHTTPPort = 8118
	}
	if s.settings.MixedPort <= 0 {
		s.settings.MixedPort = 7890
	}
	if s.settings.MixedListen == "" {
		s.settings.MixedListen = "127.0.0.1"
	}
	if err := s.read(filepath.Join(dir, "subs.json"), &s.subs); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := s.ensureTemplate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Engine 返回当前内核名（已归一化）。
func (s *Store) Engine() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.engine
}

// SetEngine 切换内核。切换后 ConfigPath/TemplatePath 指向该引擎自己的文件，
// 两个引擎的配置互不覆盖，切回去还能用原来那份。
func (s *Store) SetEngine(name string) error {
	e := engine.Normalize(name)
	s.mu.Lock()
	s.engine = e
	s.mu.Unlock()
	return s.ensureTemplate()
}

// ensureTemplate 在模板缺失时写入该引擎的内置模板。
// sing-box 写 JSON，mihomo 写 YAML；两者文件名不同，互不影响。
func (s *Store) ensureTemplate() error {
	path := s.TemplatePath()
	if _, err := os.Stat(path); err == nil {
		s.migrateTemplateURLs(path)
		return nil
	}
	var b []byte
	if engine.IsMihomo(s.Engine()) {
		b = []byte(defaultClashTemplate())
	} else {
		j, err := json.MarshalIndent(defaultTemplate(), "", "  ")
		if err != nil {
			return err
		}
		b = j
	}
	return WriteFileAtomic(path, b, 0o600)
}

// 老版本的内置模板把规则集指向 raw.githubusercontent.com，境内基本不可达，
// 核心启动时会 FATAL（"initial rule-set: ... dial tcp: i/o timeout"），
// 用户只看到一句 probe 失败，很难自己定位。
//
// 迁移只碰三处，且都要求与内置默认值一致：
//  1. URL 精确等于那两个 raw 地址 → 换成 jsDelivr 镜像
//  2. 这些规则集的 http_client.detour 为 proxy → 改成 direct
//  3. 更老的 download_detour 一级字段（proxy / direct）→ 同上
//
// 用户自己改过的 URL / 别的 http_client 一律不动。替换是幂等的。
func (s *Store) migrateTemplateURLs(path string) {
	if engine.IsMihomo(s.Engine()) {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return // 模板被改坏了，交给 check 报错，不在这里猜
	}
	route, _ := doc["route"].(map[string]any)
	sets, _ := route["rule_set"].([]any)
	changed := false
	for _, it := range sets {
		rs, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if url, _ := rs["url"].(string); url != "" {
			switch url {
			case "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs":
				rs["url"] = "https://cdn.jsdelivr.net/gh/SagerNet/sing-geosite@rule-set/geosite-cn.srs"
				changed = true
			case "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs":
				rs["url"] = "https://cdn.jsdelivr.net/gh/SagerNet/sing-geoip@rule-set/geoip-cn.srs"
				changed = true
			}
		}
		if hc, ok := rs["http_client"].(map[string]any); ok {
			if d, _ := hc["detour"].(string); d == "proxy" || d == "direct" {
				// proxy：节点全挂时下不到规则集，核心 FATAL
				// direct：sing-box 拒绝 detour 到空 direct，同样起不来
				hc["detour"] = model.RuleSetDetourTag
				changed = true
			}
		}
		// 更老的模板用的是 sing-box 1.14 之前的一级字段 download_detour。
		// 渲染层会把它改写成 http_client.detour，于是 "proxy" 又回来了 —— 启动时
		// 节点还没选好，规则集下不到，核心直接 FATAL。这里就地换成独立直连出站。
		if d, ok := rs["download_detour"].(string); ok && (d == "proxy" || d == "direct") {
			delete(rs, "download_detour")
			if _, has := rs["http_client"]; !has {
				rs["http_client"] = map[string]any{"detour": model.RuleSetDetourTag}
			}
			changed = true
		}
	}
	if !changed {
		return
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return
	}
	if err := WriteFileAtomic(path, out, 0o600); err != nil {
		return
	}
	log.Printf("已把模板里的规则集下载切到 jsDelivr 镜像并改为直连（%s）", filepath.Base(path))
}

func (s *Store) Dir() string { return s.dir }

// ConfigPath 是当前引擎的生效配置：sing-box 用 config.json，mihomo 用 config.yaml。
func (s *Store) ConfigPath() string {
	return filepath.Join(s.dir, engine.ConfigFile(s.Engine()))
}

func (s *Store) TemplatePath() string {
	return filepath.Join(s.dir, engine.TemplateFile(s.Engine()))
}

func (s *Store) snapshotDir() string        { return filepath.Join(s.dir, "snapshots") }
func (s *Store) nodesPath(id string) string { return filepath.Join(s.dir, "nodes-"+id+".json") }

/* ------------------------------------------------------------ atomic io */

func (s *Store) write(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", filepath.Base(path), err)
	}
	return WriteFileAtomic(path, b, 0o600)
}

func (s *Store) read(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// WriteFileAtomic 是写入磁盘的唯一入口：tmp → fsync → rename。
// 断电时只可能是旧文件或新文件，不会出现半截 JSON。
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

/* ------------------------------------------------------------ settings */

func (s *Store) Settings() model.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

func (s *Store) SaveSettings(v model.Settings) error {
	v.Engine = engine.Normalize(v.Engine)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = v
	s.engine = v.Engine
	return s.write(filepath.Join(s.dir, "settings.json"), v)
}

/* ------------------------------------------------------------ subscriptions */

func (s *Store) Subscriptions() []model.Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Subscription, len(s.subs))
	copy(out, s.subs)
	return out
}

func (s *Store) Subscription(id string) (model.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sub := range s.subs {
		if sub.ID == id {
			return sub, nil
		}
	}
	return model.Subscription{}, ErrNotFound
}

func (s *Store) UpsertSubscription(sub model.Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for i := range s.subs {
		if s.subs[i].ID == sub.ID {
			s.subs[i] = sub
			found = true
		}
	}
	if !found {
		s.subs = append(s.subs, sub)
	}
	return s.write(filepath.Join(s.dir, "subs.json"), s.subs)
}

func (s *Store) DeleteSubscription(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.subs[:0]
	for _, sub := range s.subs {
		if sub.ID != id {
			out = append(out, sub)
		}
	}
	s.subs = out
	os.Remove(s.nodesPath(id))
	return s.write(filepath.Join(s.dir, "subs.json"), s.subs)
}

/* ------------------------------------------------------------ nodes */

func (s *Store) Nodes(subID string) ([]model.Node, error) {
	var nodes []model.Node
	if err := s.read(s.nodesPath(subID), &nodes); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return nodes, nil
}

func (s *Store) SaveNodes(subID string, nodes []model.Node) error {
	return s.write(s.nodesPath(subID), nodes)
}

func (s *Store) AllNodes() []model.Node {
	subs := s.Subscriptions()
	var all []model.Node
	for _, sub := range subs {
		nodes, err := s.Nodes(sub.ID)
		if err != nil {
			continue
		}
		all = append(all, nodes...)
	}
	return all
}

func (s *Store) SetNodeEnabled(id string, enabled bool) error {
	for _, sub := range s.Subscriptions() {
		nodes, err := s.Nodes(sub.ID)
		if err != nil {
			continue
		}
		hit := false
		for i := range nodes {
			if nodes[i].ID == id {
				nodes[i].Enabled = enabled
				hit = true
			}
		}
		if hit {
			return s.SaveNodes(sub.ID, nodes)
		}
	}
	return ErrNotFound
}

/* ------------------------------------------------------------ config */

func (s *Store) Config() ([]byte, error) {
	b, err := os.ReadFile(s.ConfigPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

func (s *Store) WriteConfig(b []byte) error {
	return WriteFileAtomic(s.ConfigPath(), b, 0o600)
}

// TemplateRaw 读取当前引擎的模板原文：sing-box 是 JSON，mihomo 是 YAML。
// 面板不解析模板结构，交给 config 包按引擎解码。
func (s *Store) TemplateRaw() ([]byte, error) {
	b, err := os.ReadFile(s.TemplatePath())
	if os.IsNotExist(err) {
		if err := s.ensureTemplate(); err != nil {
			return nil, err
		}
		return os.ReadFile(s.TemplatePath())
	}
	return b, err
}

func (s *Store) SaveTemplateRaw(b []byte) error {
	return WriteFileAtomic(s.TemplatePath(), b, 0o600)
}

// BumpConfigVersion 每次成功写入配置后自增，用来表示「当前生效的是第几版」。
// 快照的 version 是另一套编号（用于回滚定位），两者不要混用。
func (s *Store) BumpConfigVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.ConfigVersion++
	v := s.settings.ConfigVersion
	_ = s.write(filepath.Join(s.dir, "settings.json"), s.settings)
	return v
}

// SetLastCheck 在同一个锁里更新校验状态，避免与版本自增互相覆盖。
func (s *Store) SetLastCheck(ok bool, at string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings.LastCheckOK = ok
	s.settings.LastCheckAt = at
	_ = s.write(filepath.Join(s.dir, "settings.json"), s.settings)
}

/* ------------------------------------------------------------ snapshots */

func (s *Store) snapIndexPath() string { return filepath.Join(s.snapshotDir(), "index.json") }

func (s *Store) Snapshots() []model.Snapshot {
	var list []model.Snapshot
	_ = s.read(s.snapIndexPath(), &list)
	sort.Slice(list, func(i, j int) bool { return list[i].Version > list[j].Version })
	return list
}

// TakeSnapshot 把当前生效配置另存为一份快照，并按 keep 裁剪。
func (s *Store) TakeSnapshot(note string) (model.Snapshot, error) {
	cur, err := s.Config()
	if err != nil || len(cur) == 0 {
		return model.Snapshot{}, errors.New("no config to snapshot")
	}
	list := s.Snapshots()
	version := 1
	for _, it := range list {
		if it.Version >= version {
			version = it.Version + 1
		}
	}
	file := fmt.Sprintf("v%d-%d%s", version, time.Now().Unix(), engine.Ext(s.Engine()))
	if err := WriteFileAtomic(filepath.Join(s.snapshotDir(), file), cur, 0o600); err != nil {
		return model.Snapshot{}, err
	}
	snap := model.Snapshot{
		Version: version,
		Time:    time.Now(),
		Note:    note,
		Size:    len(cur),
		File:    file,
	}
	list = append([]model.Snapshot{snap}, list...)

	keep := s.Settings().KeepSnapshots
	if keep <= 0 {
		keep = 3
	}
	for i, it := range list {
		if i >= keep {
			os.Remove(filepath.Join(s.snapshotDir(), it.File))
		}
	}
	if len(list) > keep {
		list = list[:keep]
	}
	if err := s.write(s.snapIndexPath(), list); err != nil {
		return model.Snapshot{}, err
	}
	s.gcSnapshots(list)
	return snap, nil
}

// gcSnapshots 回收索引之外的孤立快照文件。
// 只按索引遍历会漏掉历史遗留文件（例如早期版本写下的），久了会占满 flash。
func (s *Store) gcSnapshots(list []model.Snapshot) {
	keep := map[string]bool{"index.json": true}
	for _, it := range list {
		if it.File != "" {
			keep[it.File] = true
		}
	}
	entries, err := os.ReadDir(s.snapshotDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || keep[e.Name()] {
			continue
		}
		if strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".yaml") {
			_ = os.Remove(filepath.Join(s.snapshotDir(), e.Name()))
		}
	}
}

func (s *Store) SnapshotConfig(version int) ([]byte, error) {
	for _, it := range s.Snapshots() {
		if it.Version != version {
			continue
		}
		// 兼容旧索引（file 字段缺失）：按 v<version>-*.json / *.yaml 兜底查找
		if it.File == "" {
			var matches []string
			for _, ext := range []string{".json", ".yaml"} {
				m, _ := filepath.Glob(filepath.Join(s.snapshotDir(), fmt.Sprintf("v%d-*%s", version, ext)))
				matches = append(matches, m...)
			}
			if len(matches) == 0 {
				return nil, ErrNotFound
			}
			return os.ReadFile(matches[0])
		}
		return os.ReadFile(filepath.Join(s.snapshotDir(), it.File))
	}
	return nil, ErrNotFound
}

/* ------------------------------------------------------------ template */

func defaultTemplate() map[string]any {
	inbounds := []any{
		// 手动代理入口：仅监听本机回环 127.0.0.1，配合「接管本机流量」使用。
		// 局域网设备请改用带鉴权的 socks-lan(:1080)/http-lan(:8118)，
		// 不要把 mixed 暴露到 0.0.0.0（那是无鉴权、对全网开放的隐患）。
		map[string]any{
			"type": "mixed", "tag": "mixed-in",
			"listen": "127.0.0.1", "listen_port": 7890,
		},
	}
	// redirect 入站依赖内核 SO_ORIGINAL_DST，sing-box 仅在 Linux 上支持；
	// 两个 DNS 入站分别服务本机 resolv.conf 和 nft 的 53 端口劫持，也都只在 Linux 有意义。
	// Windows 上保留它们会导致核心启动即失败并进入崩溃循环。
	if runtime.GOOS == "linux" {
		inbounds = append(inbounds,
			// redirect 入站常驻：没有 nft 规则时不会有流量进来，
			// 打开透明代理只需要下发 nft，不必改配置重启核心。
			map[string]any{
				"type": "redirect", "tag": "redirect-in",
				"listen": "0.0.0.0", "listen_port": 12345,
			},
			// 本机 DNS：监听 127.0.0.1:53，resolv.conf 指过来即可绕开污染
			map[string]any{
				"type": "direct", "tag": "dns-in",
				"listen": "127.0.0.1", "listen_port": 53,
			},
			// 局域网 DNS：nft 把 LAN 的 53 端口重定向到这里（默认不启用）
			map[string]any{
				"type": "direct", "tag": "dns-lan",
				"listen": "0.0.0.0", "listen_port": 5354,
			},
		)
	}
	return map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"tag": "local", "type": "udp", "server": "223.5.5.5"},
				map[string]any{"tag": "remote", "type": "https", "server": "1.1.1.1", "detour": "proxy"},
			},
			"rules": []any{
				map[string]any{"rule_set": []any{"geosite-cn"}, "server": "local"},
			},
			"final": "remote",
		},
		"inbounds":  inbounds,
		"outbounds": []any{},
		"route": map[string]any{
			"rules": []any{
				map[string]any{"action": "sniff"},
				// DNS 查询交给内置 DNS 模块处理，否则会被当成普通连接再走一遍路由
				map[string]any{"protocol": "dns", "action": "hijack-dns"},
				// 内网直连
				map[string]any{"ip_is_private": true, "outbound": "direct"},
				// 国内域名 / IP 直连，避免无谓绕路
				map[string]any{"rule_set": []any{"geosite-cn"}, "outbound": "direct"},
				map[string]any{"rule_set": []any{"geoip-cn"}, "outbound": "direct"},
			},
			"rule_set": []any{
				map[string]any{
					"tag": "geosite-cn", "type": "remote", "format": "binary",
					// raw.githubusercontent.com 在境内基本不可达（实测 10s 零字节），
					// 走 jsDelivr 镜像 1.4s 就能下完。
					"url": "https://cdn.jsdelivr.net/gh/SagerNet/sing-geosite@rule-set/geosite-cn.srs",
					// 规则集下载必须独立于节点：走 proxy 的话节点全挂时核心会因为
					// 下不到规则集而 FATAL，用户连面板都进不去排查。
					// 但 sing-box 不允许 detour 指向"空 direct"，所以 Render 会额外
					// 生成一个带 tcp_fast_open 的 rule-set 出站专门给这里用。
					"http_client":     map[string]any{"detour": model.RuleSetDetourTag},
					"update_interval": "7d",
				},
				map[string]any{
					"tag": "geoip-cn", "type": "remote", "format": "binary",
					"url":             "https://cdn.jsdelivr.net/gh/SagerNet/sing-geoip@rule-set/geoip-cn.srs",
					"http_client":     map[string]any{"detour": model.RuleSetDetourTag},
					"update_interval": "7d",
				},
			},
			"final":                   "proxy",
			"auto_detect_interface":   true,
			"default_domain_resolver": map[string]any{"server": "local"},
		},
		"experimental": map[string]any{
			"clash_api": map[string]any{
				"external_controller": "127.0.0.1:9091",
				"default_mode":        "rule",
			},
			// 缓存规则集，避免每次重启都重新下载。
			// path 由 Render 解析为绝对路径（相对 DataDir），不要写死目录名。
			"cache_file": map[string]any{"enabled": true, "path": "cache.db"},
		},
	}
}

/* ------------------------------------------------------------ misc */

func SanitizeTag(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\u200b", "")
	if s == "" {
		return "node"
	}
	return s
}
