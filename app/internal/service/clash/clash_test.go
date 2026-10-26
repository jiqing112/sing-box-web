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
