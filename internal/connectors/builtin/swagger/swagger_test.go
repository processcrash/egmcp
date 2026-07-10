package swagger

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifest(t *testing.T) {
	c := New()
	m := c.Manifest()
	if m.Name != "swagger" {
		t.Fatalf("name: %q", m.Name)
	}
	if len(m.ConfigSchema) == 0 {
		t.Fatalf("missing ConfigSchema")
	}
}

func TestInitRequiresSpec(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{})
	if err := c.Init(context.Background(), raw); err == nil {
		t.Fatalf("expected error: spec_url or spec_file required")
	}
}

// petstoreOpenAPI is a tiny minimal OpenAPI 3.0 doc with two
// operations. Used by the tests below.
const petstoreOpenAPI = `openapi: 3.0.3
info:
  title: petstore
  version: 0.0.1
servers:
  - url: http://localhost:8081
paths:
  /pets/{id}:
    get:
      operationId: getPet
      summary: get a pet
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: integer
      responses:
        '200': {description: ok}
  /pets:
    post:
      operationId: createPet
      summary: create a pet
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
      responses:
        '201': {description: created}
`

func TestInitFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(path, []byte(petstoreOpenAPI), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New()
	raw, _ := json.Marshal(map[string]any{
		"spec_file": path,
		"base_url":  "http://localhost:8081",
	})
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatalf("Init: %v", err)
	}
	tools := c.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	for _, t1 := range tools {
		t.Logf("tool: %q", t1.Name)
	}
	// Tool names are computed safely; pick whatever shape we get and
	// just verify the post path is reachable.
	var post string
	for _, t1 := range tools {
		if strings.Contains(t1.Name, "post") {
			post = t1.Name
		}
	}
	if post == "" {
		t.Fatalf("no post tool found: %+v", tools)
	}
}

func TestInvokeToolDispatchesHTTP(t *testing.T) {
	// Spin up an upstream that answers /pets/42 with JSON.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pets/42" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":42,"name":"Rex"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer upstream.Close()

	// Build a tiny spec against the upstream.
	spec := `openapi: 3.0.3
info: {title: test, version: 0.0.1}
paths:
  /pets/{id}:
    get:
      operationId: getPet
      parameters:
        - name: id
          in: path
          required: true
          schema: {type: integer}
      responses:
        '200': {description: ok}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.yaml")
	if err := os.WriteFile(path, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New()
	raw, _ := json.Marshal(map[string]any{
		"spec_file": path,
		"base_url":  upstream.URL,
	})
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatalf("Init: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"id": float64(42)})
	tools := c.Tools()
	if len(tools) == 0 {
		t.Fatalf("no tools discovered")
	}
	out, err := c.InvokeTool(context.Background(), tools[0].Name, args)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if int(resp["status"].(float64)) != 200 {
		t.Fatalf("status: %v", resp["status"])
	}
	if !contains(resp["body"].(string), "Rex") {
		t.Fatalf("body: %v", resp["body"])
	}
}

func TestSafeName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"a", "a"},
		{"abc__def", "abc__def"},
		{"with space", "with_space"},
		{"multi/sep+chars", "multi_sep_chars"},
	}
	for _, c1 := range cases {
		if got := safeName(c1.in); got != c1.want {
			t.Fatalf("safeName(%q): want %q, got %q", c1.in, c1.want, got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
