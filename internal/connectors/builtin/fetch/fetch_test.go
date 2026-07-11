package fetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManifest(t *testing.T) {
	c := New()
	m := c.Manifest()
	if m.Name != "fetch" {
		t.Fatalf("name: %q", m.Name)
	}
}

func TestInitRequiresBaseURL(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{})
	if err := c.Init(context.Background(), raw); err == nil {
		t.Fatalf("expected error for empty base_url")
	}
}

func TestInitAppliesDefaults(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{BaseURL: "http://example.com"})
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if c.cfg.TimeoutSec != 30 {
		t.Fatalf("timeout: %d", c.cfg.TimeoutSec)
	}
}

func TestGetDispatch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			w.Header().Set("X-Test", "yes")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("pong"))
			return
		}
		w.WriteHeader(404)
	}))
	defer upstream.Close()

	c := New()
	if err := c.Init(context.Background(), jsonRaw(`{"base_url":"`+upstream.URL+`"}`)); err != nil {
		t.Fatal(err)
	}
	out, err := c.InvokeTool(context.Background(), "get", jsonRaw(`{"path":"ping"}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if int(resp["status"].(float64)) != 200 {
		t.Fatalf("status: %v", resp["status"])
	}
	if resp["body"] != "pong" {
		t.Fatalf("body: %v", resp["body"])
	}
}

func TestToolNames(t *testing.T) {
	c := New()
	tools := c.Tools()
	want := map[string]bool{"get": false, "post": false, "put": false, "delete": false, "head": false}
	for _, t1 := range tools {
		if _, ok := want[t1.Name]; ok {
			want[t1.Name] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Fatalf("missing tool %q", n)
		}
	}
}

func jsonRaw(s string) []byte { return []byte(s) }