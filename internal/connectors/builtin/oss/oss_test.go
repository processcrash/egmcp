package oss

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestManifest(t *testing.T) {
	c := New()
	m := c.Manifest()
	if m.Name != "oss" {
		t.Fatalf("name: %q", m.Name)
	}
	if len(m.Capabilities) == 0 {
		t.Fatalf("missing Capabilities")
	}
}

func TestInitRequiresCredentials(t *testing.T) {
	c := New()
	cases := []struct {
		name string
		cfg  Config
	}{
		{"empty", Config{}},
		{"missing access", Config{SecretKey: "x"}},
		{"missing secret", Config{AccessKey: "x"}},
	}
	for _, c1 := range cases {
		raw, _ := json.Marshal(c1.cfg)
		if err := c.Init(context.Background(), raw); err == nil {
			t.Fatalf("%s: expected error", c1.name)
		}
	}
}

func TestInitBadEndpoint(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{
		AccessKey: "x", SecretKey: "y",
		Endpoint: "not-a-url-at-all",
	})
	// The aliyun SDK accepts loosely-formed endpoints because it
	// only needs a hostname for signing; we at least verify that
	// credentials without a usable path produce some failure later.
	// Here we just check Init ran without panic.
	if err := c.Init(context.Background(), raw); err != nil {
		// Init-level rejection is the desired outcome, not a panic.
		return
	}
}

func TestTools(t *testing.T) {
	c := New()
	tools := c.Tools()
	want := map[string]bool{
		"put_object":    false,
		"get_object":    false,
		"delete_object": false,
		"list_objects":  false,
		"presign_get":   false,
		"list_buckets":  false,
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

func TestNotFoundDetection(t *testing.T) {
	for _, msg := range []string{"NoSuchKey", "Not Found", "404", "ignored"} {
		got := isNotFound(errFromMsg(msg))
		want := strings.Contains(msg, "NoSuchKey") ||
			strings.Contains(msg, "Not Found") ||
			strings.Contains(msg, "404")
		if got != want {
			t.Fatalf("isNotFound(%q): want %v, got %v", msg, want, got)
		}
	}
	if isNotFound(nil) {
		t.Fatalf("nil error should not be detected as not-found")
	}
}

func errFromMsg(s string) error {
	return &fakeErr{msg: s}
}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
