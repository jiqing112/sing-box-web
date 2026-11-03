package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"singbox-panel/internal/model"
)

// 老模板的 download_detour: proxy 必须在打开数据目录时被迁走。
//
// 不迁的后果实测过：渲染层会把它改写成 http_client.detour=proxy，核心启动时
// 节点还没选好，规则集下载走代理直接失败 → FATAL → 崩溃循环 → 回滚到旧内核。
// 用户看到的只是「切换失败」，根本猜不到是规则集下载。
func TestMigrateLegacyDownloadDetour(t *testing.T) {
	dir := t.TempDir()
	tpl := map[string]any{
		"route": map[string]any{
			"rule_set": []any{
				map[string]any{
					"tag": "geosite-cn", "type": "remote", "format": "binary",
					"url":             "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs",
					"download_detour": "proxy",
					"update_interval": "7d",
				},
				map[string]any{
					"tag": "geoip-cn", "type": "remote", "format": "binary",
					"url":             "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-cn.srs",
					"download_detour": "direct",
					"update_interval": "7d",
				},
			},
		},
	}
	raw, _ := json.MarshalIndent(tpl, "", "  ")
	path := filepath.Join(dir, "template.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("写模板: %v", err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 只迁 sing-box 的模板；mihomo 用的是 YAML，不该被动
	if s.Engine() != "sing-box" {
		t.Fatalf("engine = %s, want sing-box", s.Engine())
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回模板: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("解析: %v", err)
	}
	sets := doc["route"].(map[string]any)["rule_set"].([]any)
	if len(sets) != 2 {
		t.Fatalf("rule_set 数量 = %d, want 2", len(sets))
	}
	for i, it := range sets {
		rs := it.(map[string]any)
		if _, ok := rs["download_detour"]; ok {
			t.Fatalf("第 %d 条仍有 download_detour", i)
		}
		hc, ok := rs["http_client"].(map[string]any)
		if !ok {
			t.Fatalf("第 %d 条没有 http_client", i)
		}
		if d, _ := hc["detour"].(string); d != model.RuleSetDetourTag {
			t.Fatalf("第 %d 条 detour = %q, want %q", i, d, model.RuleSetDetourTag)
		}
		if u, _ := rs["url"].(string); u != "https://cdn.jsdelivr.net/gh/SagerNet/sing-geosite@rule-set/geosite-cn.srs" &&
			u != "https://cdn.jsdelivr.net/gh/SagerNet/sing-geoip@rule-set/geoip-cn.srs" {
			t.Fatalf("第 %d 条 URL 没换成镜像: %s", i, u)
		}
	}

	// 幂等：再开一次不应该再改文件
	again, _ := os.ReadFile(path)
	if _, err := Open(dir); err != nil {
		t.Fatalf("二次 Open: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(again) != string(after) {
		t.Fatalf("迁移不幂等，文件被二次改写")
	}
}
