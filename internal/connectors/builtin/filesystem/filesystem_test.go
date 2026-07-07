package filesystem

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func mustInit(t *testing.T, c *Connector, cfg Config) {
	t.Helper()
	raw, _ := json.Marshal(cfg)
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

func TestInitRequiresRoot(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{})
	if err := c.Init(context.Background(), raw); err == nil {
		t.Fatalf("expected error for empty root")
	}
}

func TestInitValidatesRoot(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{Root: filepath.Join(t.TempDir(), "does-not-exist")})
	if err := c.Init(context.Background(), raw); err == nil {
		t.Fatalf("expected error for non-existent root")
	}
}

func TestReadAndWrite(t *testing.T) {
	root := t.TempDir()
	c := New()
	mustInit(t, c, Config{Root: root})

	// write
	raw, _ := json.Marshal(map[string]any{
		"path":    "hello.txt",
		"content": "hello world",
	})
	if _, err := c.InvokeTool(context.Background(), "write_file", raw); err != nil {
		t.Fatalf("write: %v", err)
	}

	// read
	raw, _ = json.Marshal(map[string]any{"path": "hello.txt"})
	out, err := c.InvokeTool(context.Background(), "read_file", raw)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Content != "hello world" {
		t.Fatalf("content: %q", resp.Content)
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	root := t.TempDir()
	c := New()
	mustInit(t, c, Config{Root: root, ReadOnly: true})

	raw, _ := json.Marshal(map[string]any{"path": "x.txt", "content": "x"})
	if _, err := c.InvokeTool(context.Background(), "write_file", raw); err == nil {
		t.Fatalf("write must fail in read-only mode")
	}
}

func TestPathTraversalRejected(t *testing.T) {
	root := t.TempDir()
	c := New()
	mustInit(t, c, Config{Root: root})

	for _, bad := range []string{"../escape.txt", "..", "/abs/file", "sub/../../escape"} {
		raw, _ := json.Marshal(map[string]any{"path": bad})
		if _, err := c.InvokeTool(context.Background(), "read_file", raw); err == nil {
			t.Fatalf("path %q should be rejected", bad)
		}
	}
}

func TestListDirFiltersHidden(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{".hidden", "visible", "sub"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := New()
	mustInit(t, c, Config{Root: root})

	raw, _ := json.Marshal(map[string]any{"path": ".", "with_hidden": false})
	out, err := c.InvokeTool(context.Background(), "list_dir", raw)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var resp struct {
		Entries []map[string]any `json:"entries"`
	}
	_ = json.Unmarshal(out, &resp)
	for _, e := range resp.Entries {
		if e["name"] == ".hidden" {
			t.Fatalf("hidden file was returned: %+v", resp.Entries)
		}
	}

	raw, _ = json.Marshal(map[string]any{"path": ".", "with_hidden": true})
	out, _ = c.InvokeTool(context.Background(), "list_dir", raw)
	resp.Entries = nil
	_ = json.Unmarshal(out, &resp)
	if len(resp.Entries) != 3 {
		t.Fatalf("with_hidden=true should return 3 entries, got %d", len(resp.Entries))
	}
}

func TestSearchFindsFiles(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a.txt", "sub/b.txt", "sub/notes.md"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := New()
	mustInit(t, c, Config{Root: root})

	raw, _ := json.Marshal(map[string]any{"query": "B", "max_results": 10})
	out, err := c.InvokeTool(context.Background(), "search", raw)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var resp struct {
		Results []map[string]any `json:"results"`
	}
	_ = json.Unmarshal(out, &resp)
	if len(resp.Results) == 0 {
		t.Fatalf("expected hits, got 0")
	}
}

func TestDeleteFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New()
	mustInit(t, c, Config{Root: root})

	raw, _ := json.Marshal(map[string]any{"path": "x.txt"})
	if _, err := c.InvokeTool(context.Background(), "delete_file", raw); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
}

func TestReadResourceTree(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"a.txt", "sub/b.txt"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := New()
	mustInit(t, c, Config{Root: root})
	r, err := c.ReadResource(context.Background(), "fs://tree")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if r.MIMEType != "application/json" {
		t.Fatalf("mime: %q", r.MIMEType)
	}
	// Use forward-slash comparison because filepath.Rel on Windows
	// emits backslashes.
	if !contains(r.Text, "a.txt") || !contains(r.Text, "b.txt") {
		t.Fatalf("tree missing entries: %s", r.Text)
	}
}

func TestHealthCheck(t *testing.T) {
	root := t.TempDir()
	c := New()
	mustInit(t, c, Config{Root: root})
	if err := c.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}

	// After deleting the root, HealthCheck must fail.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := c.HealthCheck(context.Background()); err == nil {
		t.Fatalf("HealthCheck should fail when root is missing")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
