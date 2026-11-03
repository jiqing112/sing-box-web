package clash

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTrimConnectionsKeepsDestinationPort(t *testing.T) {
	raw := []byte(`{"connections":[{
		"id":"1",
		"metadata":{"host":"example.com","destinationIP":"1.1.1.1","destinationPort":"443","network":"tcp","process":"app.exe"},
		"chains":["proxy"],"rule":"match","rulePayload":"","upload":10,"download":20,"start":"2026-10-21T00:00:00Z"
	}]}`)
	out := trimConnections(raw, 200)
	var doc struct {
		Connections []struct {
			ID       string `json:"id"`
			Metadata struct {
				DestinationPort string `json:"destinationPort"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Connections) != 1 {
		t.Fatalf("connections = %d, want 1", len(doc.Connections))
	}
	if doc.Connections[0].Metadata.DestinationPort != "443" {
		t.Fatalf("destinationPort = %q, want 443", doc.Connections[0].Metadata.DestinationPort)
	}
}

func TestTrimConnectionsLimit(t *testing.T) {
	raw := []byte(`{"connections":[{"id":"1"},{"id":"2"},{"id":"3"}]}`)
	out := trimConnections(raw, 2)
	var doc struct {
		Connections []struct {
			ID string `json:"id"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Connections) != 2 {
		t.Fatalf("connections = %d, want 2", len(doc.Connections))
	}
}

func TestTrimConnectionsInvalidJSON(t *testing.T) {
	out := trimConnections([]byte("{not json"), 200)
	if strings.TrimSpace(string(out)) != `{"connections":[]}` {
		t.Fatalf("out = %s", out)
	}
}

// 切换内核后必须清掉延迟缓存：sing-box 与 mihomo 的 delay 测量口径不同，
// 同一节点实测可差 4~6 倍，沿用旧值会让人误判线路质量。
func TestResetDelays(t *testing.T) {
	c := New("127.0.0.1:9091")
	c.storeDelay("🇯🇵Japan 01", 137)
	c.storeDelay("🇺🇸United States 01", 149)
	if n := len(c.Delays()); n != 2 {
		t.Fatalf("delays = %d, want 2", n)
	}
	c.ResetDelays()
	if n := len(c.Delays()); n != 0 {
		t.Fatalf("ResetDelays 后仍有 %d 条缓存", n)
	}
	// 清空后还能继续写入，不能把 map 置成 nil
	c.storeDelay("🇯🇵Japan 01", 594)
	if n := len(c.Delays()); n != 1 {
		t.Fatalf("重新写入后 delays = %d, want 1", n)
	}
}
