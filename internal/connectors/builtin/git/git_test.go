package git

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initRepo creates a tiny local git repo with one commit.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@local"},
		{"config", "user.name", "test"},
		{"config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	// Create a file and commit it.
	if err := exec.Command("git", "-C", dir, "config", "user.email", "test@local").Run(); err != nil {
		t.Fatal(err)
	}
	writeFile := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := writeFile0(p, body); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("hello.txt", "hello world\n")
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile("docs/intro.md", "Welcome\n")
	commit := [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", "initial"},
		{"log", "--oneline"},
	}
	for _, args := range commit {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func writeFile0(path, body string) error {
	return writeFileHelper(path, body)
}

func TestManifest(t *testing.T) {
	c := New()
	if c.Manifest().Name != "git" {
		t.Fatalf("name: %q", c.Manifest().Name)
	}
}

func TestInitRequiresRepo(t *testing.T) {
	c := New()
	raw, _ := json.Marshal(Config{})
	if err := c.Init(context.Background(), raw); err == nil {
		t.Fatalf("expected error for empty repo")
	}
}

func TestHealthCheckOnRepo(t *testing.T) {
	dir := initRepo(t)
	c := New()
	raw, _ := json.Marshal(Config{Repo: dir})
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if err := c.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
}

func TestLogTool(t *testing.T) {
	dir := initRepo(t)
	c := New()
	raw, _ := json.Marshal(Config{Repo: dir})
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	out, err := c.InvokeTool(context.Background(), "log", []byte(`{}`))
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	var resp struct {
		Commits []map[string]any `json:"commits"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(resp.Commits))
	}
	if resp.Commits[0]["subject"] != "initial" {
		t.Fatalf("subject: %v", resp.Commits[0]["subject"])
	}
}

func TestStatusTool(t *testing.T) {
	dir := initRepo(t)
	c := New()
	raw, _ := json.Marshal(Config{Repo: dir})
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	out, err := c.InvokeTool(context.Background(), "status", []byte(`{}`))
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	var resp struct {
		Entries []map[string]string `json:"entries"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("expected clean tree, got %d entries", len(resp.Entries))
	}
}

func TestSearchTool(t *testing.T) {
	dir := initRepo(t)
	c := New()
	raw, _ := json.Marshal(Config{Repo: dir})
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"query": "hello"})
	out, err := c.InvokeTool(context.Background(), "search", args)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var resp struct {
		Matches []string `json:"matches"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Matches) == 0 {
		t.Fatalf("expected at least one match")
	}
}

func TestBranchesTool(t *testing.T) {
	dir := initRepo(t)
	c := New()
	raw, _ := json.Marshal(Config{Repo: dir})
	if err := c.Init(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	out, err := c.InvokeTool(context.Background(), "branches", []byte(`{}`))
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	var resp struct {
		Branches []map[string]any `json:"branches"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Branches) == 0 {
		t.Fatalf("expected at least one branch")
	}
}

func TestToolNames(t *testing.T) {
	c := New()
	tools := c.Tools()
	want := map[string]bool{
		"log": false, "show": false, "diff": false, "status": false,
		"branches": false, "blame": false, "search": false,
	}
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