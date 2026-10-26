package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"singbox-panel/internal/model"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	dir string

	mu       sync.RWMutex
	subs     []model.Subscription
	settings model.Settings
}

func Open(dir string) (*Store, error) {
	for _, d := range []string{dir, filepath.Join(dir, "snapshots")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	s := &Store{dir: dir, settings: model.DefaultSettings()}

	if err := s.read(filepath.Join(dir, "settings.json"), &s.settings); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	// 兼容旧版 settings.json：缺字段时补默认值
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
	if err := s.read(filepath.Join(dir, "subs.json"), &s.subs); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if _, err := os.Stat(s.TemplatePath()); os.IsNotExist(err) {
		if err := s.write(s.TemplatePath(), defaultTemplate()); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Dir() string                { return s.dir }
func (s *Store) ConfigPath() string         { return filepath.Join(s.dir, "config.json") }
func (s *Store) TemplatePath() string       { return filepath.Join(s.dir, "template.json") }
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = v
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

func (s *Store) Template() (map[string]any, error) {
	var tpl map[string]any
	if err := s.read(s.TemplatePath(), &tpl); err != nil {
		return nil, err
	}
	return tpl, nil
}

func (s *Store) SaveTemplate(tpl map[string]any) error {
	return s.write(s.TemplatePath(), tpl)
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
	file := fmt.Sprintf("v%d-%d.json", version, time.Now().Unix())
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
		if strings.HasSuffix(e.Name(), ".json") {
			_ = os.Remove(filepath.Join(s.snapshotDir(), e.Name()))
		}
	}
}

func (s *Store) SnapshotConfig(version int) ([]byte, error) {
	for _, it := range s.Snapshots() {
		if it.Version != version {
			continue
		}
		// 兼容旧索引（file 字段缺失）：按 v<version>-*.json 兜底查找
		if it.File == "" {
			matches, _ := filepath.Glob(filepath.Join(s.snapshotDir(), fmt.Sprintf("v%d-*.json", version)))
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
					"url": "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs",
					// sing-box 1.14 起 download_detour 已弃用、1.16 移除，
					// 改用 http_client 指定下载走的出站（proxy 由 Render 生成）。
					"http_client":     map[string]any{"detour": "proxy"},
					"update_interval": "7d",
				},
				map[string]any{
					"tag": "geoip-cn", "type": "remote", "format": "binary",
					"url":             "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs",
					"http_client":     map[string]any{"detour": "proxy"},
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
