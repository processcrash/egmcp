package mysql

import (
	"context"
	"encoding/json"
	"testing"
)

func TestManifest(t *testing.T) {
	c := New()
	m := c.Manifest()
	if m.Name != "mysql" {
		t.Fatalf("name: %q", m.Name)
	}
	if len(m.ConfigSchema) == 0 {
		t.Fatalf("missing ConfigSchema")
	}
}

func TestInitRequiresDSN(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{ReadOnly: true})
	if err := c.Init(context.Background(), raw); err == nil {
		t.Fatalf("expected error for empty DSN")
	}
}

func TestInitBadDSN(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{DSN: "not-a-valid-dsn"})
	if err := c.Init(context.Background(), raw); err == nil {
		t.Fatalf("expected error for malformed DSN, got nil")
	}
}

func TestInitUnreachableServer(t *testing.T) {
	c := New()
	// Valid DSN syntax that points at an unreachable host. We allow a
	// short timeout by passing a context that fails the ping.
	raw, _ := json.Marshal(Config{
		DSN: "user:pass@tcp(127.0.0.1:1)/db?timeout=200ms",
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Init(ctx, raw); err == nil {
		t.Fatalf("expected error reaching unreachable server")
	}
}

func TestTools(t *testing.T) {
	c := New()
	tools := c.Tools()
	want := map[string]bool{
		"sql_query":      false,
		"list_databases": false,
		"list_tables":    false,
		"describe_table": false,
	}
	for _, t1 := range tools {
		if _, ok := want[t1.Name]; ok {
			want[t1.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("missing tool %q", name)
		}
	}
}
