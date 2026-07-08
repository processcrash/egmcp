package postgres

import (
	"context"
	"encoding/json"
	"testing"
)

func TestManifest(t *testing.T) {
	c := New()
	m := c.Manifest()
	if m.Name != "postgres" {
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
		t.Fatalf("expected error for malformed DSN")
	}
}

func TestTools(t *testing.T) {
	c := New()
	tools := c.Tools()
	want := map[string]bool{
		"sql_query":     false,
		"list_schemas":  false,
		"list_tables":   false,
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

func TestSchemaIdentRegex(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"public", true},
		{"my_schema", true},
		{"Schema1", true},
		{"1bad", false},
		{"bad-name", false},
		{"DROP TABLE x", false},
		{"", false},
	}
	for _, c1 := range cases {
		if got := schemaIdentRE.MatchString(c1.in); got != c1.want {
			t.Fatalf("schemaIdentRE.MatchString(%q): want %v, got %v", c1.in, c1.want, got)
		}
	}
}
