package subscription

import (
	"testing"
)

func TestParseURIListReservedTags(t *testing.T) {
	body := []byte("vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?security=tls#US-01\n" +
		"trojan://pass@5.6.7.8:8443#US-01\n")
	nodes, err := Parse(body, "s1", nil, map[string]bool{"US-01": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	if nodes[0].Tag != "US-01 #2" || nodes[1].Tag != "US-01 #3" {
		t.Fatalf("tags = %q, %q", nodes[0].Tag, nodes[1].Tag)
	}
	if !nodes[0].Enabled || !nodes[1].Enabled {
		t.Fatal("new nodes should default to enabled")
	}
}

func TestParseKeepsPreviousEnabledState(t *testing.T) {
	body := []byte("vless://11111111-2222-3333-4444-555555555555@1.2.3.4:443?security=tls#NodeA\n")
	first, err := Parse(body, "s1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Enabled = false

	second, err := Parse(body, "s1", first, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].ID != first[0].ID {
		t.Fatalf("id changed: %q -> %q", first[0].ID, second[0].ID)
	}
	if second[0].Enabled {
		t.Fatal("disabled state was not preserved")
	}
}

func TestParseEmptyBody(t *testing.T) {
	if _, err := Parse([]byte("   "), "s1", nil, nil); err == nil {
		t.Fatal("expected error for empty subscription")
	}
}
