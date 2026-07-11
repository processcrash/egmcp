// Package git implements a Connector that exposes a local git
// repository to MCP clients. The connector shells out to the `git`
// CLI rather than embedding go-git; this keeps the binary small and
// ensures behaviour matches what developers see in their terminal.
//
// Tools surface read-only operations by default. Mutating tools are
// only enabled when Config.ReadOnly is false (operators opt in
// explicitly).
package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/processcrash/egmcp/pkg/connector"
)

// Config is the per-instance connector config.
type Config struct {
	// Repo is the filesystem path to the repository.
	Repo string `json:"repo"`
	// ReadOnly disables mutating tools (currently we expose only
	// read-only tools; the flag is here for forward-compatibility).
	ReadOnly bool `json:"read_only"`
	// TimeoutSec bounds each git invocation; default 15s.
	TimeoutSec int `json:"timeout_seconds"`
	// MaxLogEntries caps the size of `log` results; default 50.
	MaxLogEntries int `json:"max_log_entries"`
}

// Connector implements the git backend.
type Connector struct {
	manifest connector.Manifest
	cfg     Config
}

// New returns a Connector with a static manifest.
func New() *Connector { return &Connector{manifest: manifestSchema} }

// Manifest returns the static description.
func (c *Connector) Manifest() connector.Manifest { return c.manifest }

// Init validates the config. We don't shell out to git here — the
// connector is lazy and will surface errors on the first call.
func (c *Connector) Init(_ context.Context, raw json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("git: parse config: %w", err)
	}
	if cfg.Repo == "" {
		return errors.New("git: repo is required")
	}
	abs, err := filepath.Abs(cfg.Repo)
	if err != nil {
		return fmt.Errorf("git: resolve repo: %w", err)
	}
	cfg.Repo = abs
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 15
	}
	if cfg.MaxLogEntries <= 0 {
		cfg.MaxLogEntries = 50
	}
	c.cfg = cfg
	return nil
}

// HealthCheck shells out to `git rev-parse` to confirm the path is a
// repository.
func (c *Connector) HealthCheck(ctx context.Context) error {
	_, err := c.run(ctx, "rev-parse", "--is-inside-work-tree")
	return err
}

// Shutdown is a no-op.
func (c *Connector) Shutdown(_ context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────
// Tools
// ─────────────────────────────────────────────────────────────────────

func (c *Connector) Tools() []connector.ToolSpec {
	return []connector.ToolSpec{
		{
			Name:        "log",
			Description: "Show recent commits (most recent first).",
			InputSchema: merge(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"max_count": map[string]any{"type": "integer", "minimum": 1, "default": 20},
					"branch":    map[string]any{"type": "string", "description": "Branch or ref (defaults to HEAD)"},
					"path":      map[string]any{"type": "string", "description": "Limit to a sub-path"},
				},
			}),
		},
		{
			Name:        "show",
			Description: "Show details for a single commit (hash required).",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"hash"},
				"properties": map[string]any{
					"hash": map[string]any{"type": "string"},
				},
			}),
		},
		{
			Name:        "diff",
			Description: "Diff between two refs (default: working tree vs HEAD).",
			InputSchema: merge(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from":  map[string]any{"type": "string", "description": "Left side ref (default HEAD)"},
					"to":    map[string]any{"type": "string", "description": "Right side ref (default working tree)"},
					"path":  map[string]any{"type": "string"},
					"staged": map[string]any{"type": "boolean", "default": false, "description": "If true, diff staged vs HEAD"},
				},
			}),
		},
		{
			Name:        "status",
			Description: "Show the working tree status (porcelain v1).",
			InputSchema: merge(map[string]any{"type": "object"}),
		},
		{
			Name:        "branches",
			Description: "List local + remote branches.",
			InputSchema: merge(map[string]any{"type": "object"}),
		},
		{
			Name:        "blame",
			Description: "Annotate a file with commit hashes and authors.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"path"},
				"properties": map[string]any{
					"path":  map[string]any{"type": "string"},
					"start": map[string]any{"type": "integer", "minimum": 1},
					"end":   map[string]any{"type": "integer", "minimum": 1},
				},
			}),
		},
		{
			Name:        "search",
			Description: "Search tracked file contents with `git grep`.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query":     map[string]any{"type": "string"},
					"path":      map[string]any{"type": "string", "description": "Limit to a sub-path"},
					"regex":     map[string]any{"type": "boolean", "default": true},
					"max_count": map[string]any{"type": "integer", "minimum": 1, "default": 50},
				},
			}),
		},
	}
}

// InvokeTool dispatches a tool call.
func (c *Connector) InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	var a map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
	}
	switch name {
	case "log":
		return c.toolLog(ctx, a)
	case "show":
		return c.toolShow(ctx, a)
	case "diff":
		return c.toolDiff(ctx, a)
	case "status":
		return c.toolStatus(ctx)
	case "branches":
		return c.toolBranches(ctx)
	case "blame":
		return c.toolBlame(ctx, a)
	case "search":
		return c.toolSearch(ctx, a)
	default:
		return nil, fmt.Errorf("git: unknown tool %q", name)
	}
}

func (c *Connector) toolLog(ctx context.Context, a map[string]any) (json.RawMessage, error) {
	count := intArg(a, "max_count", 20)
	if count > c.cfg.MaxLogEntries {
		count = c.cfg.MaxLogEntries
	}
	args := []string{
		"log",
		"--no-color",
		"--pretty=format:%H%x1f%an%x1f%ad%x1f%s",
		"--date=iso",
		fmt.Sprintf("-%d", count),
	}
	if branch := strArg(a, "branch", ""); branch != "" {
		args = append(args, branch)
	}
	if path := strArg(a, "path", ""); path != "" {
		args = append(args, "--", path)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	commits := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 4)
		commit := map[string]any{}
		if len(parts) > 0 {
			commit["hash"] = parts[0]
		}
		if len(parts) > 1 {
			commit["author"] = parts[1]
		}
		if len(parts) > 2 {
			commit["date"] = parts[2]
		}
		if len(parts) > 3 {
			commit["subject"] = parts[3]
		}
		commits = append(commits, commit)
	}
	return json.Marshal(map[string]any{"commits": commits})
}

func (c *Connector) toolShow(ctx context.Context, a map[string]any) (json.RawMessage, error) {
	hash := strArg(a, "hash", "")
	if hash == "" {
		return nil, errors.New("git: hash is required")
	}
	out, err := c.run(ctx, "show", "--no-color", "--stat", hash)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"hash":   hash,
		"output": out,
	})
}

func (c *Connector) toolDiff(ctx context.Context, a map[string]any) (json.RawMessage, error) {
	from := strArg(a, "from", "")
	to := strArg(a, "to", "")
	staged := boolArg(a, "staged", false)

	args := []string{"diff", "--no-color"}
	if staged {
		args = append(args, "--cached")
	}
	if from == "" && to == "" {
		// default: working tree vs HEAD
		args = append(args, "HEAD")
	} else {
		if from != "" {
			args = append(args, from)
		}
		if to != "" {
			args = append(args, to)
		}
	}
	if path := strArg(a, "path", ""); path != "" {
		args = append(args, "--", path)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"diff":      out,
		"is_empty":  out == "",
		"byte_size": len(out),
	})
}

func (c *Connector) toolStatus(ctx context.Context) (json.RawMessage, error) {
	out, err := c.run(ctx, "status", "--porcelain=v1", "-uall")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	entries := make([]map[string]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		if len(line) < 4 {
			continue
		}
		entries = append(entries, map[string]string{
			"status": line[:2],
			"path":   strings.TrimSpace(line[3:]),
		})
	}
	return json.Marshal(map[string]any{"entries": entries})
}

func (c *Connector) toolBranches(ctx context.Context) (json.RawMessage, error) {
	out, err := c.run(ctx, "branch", "--all", "--no-color")
	if err != nil {
		return nil, err
	}
	branches := []map[string]any{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		current := false
		name := line
		if strings.HasPrefix(name, "* ") {
			current = true
			name = strings.TrimSpace(name[2:])
		}
		branches = append(branches, map[string]any{
			"name":    name,
			"current": current,
		})
	}
	return json.Marshal(map[string]any{"branches": branches})
}

func (c *Connector) toolBlame(ctx context.Context, a map[string]any) (json.RawMessage, error) {
	path := strArg(a, "path", "")
	if path == "" {
		return nil, errors.New("git: path is required")
	}
	args := []string{"blame", "--no-color"}
	if start := intArg(a, "start", 0); start > 0 {
		end := intArg(a, "end", 0)
		if end > 0 && end >= start {
			args = append(args, fmt.Sprintf("-L%d,%d", start, end))
		} else {
			args = append(args, fmt.Sprintf("-L%d,+", start))
		}
	}
	args = append(args, "--", path)
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"path":  path,
		"blame": out,
	})
}

func (c *Connector) toolSearch(ctx context.Context, a map[string]any) (json.RawMessage, error) {
	query := strArg(a, "query", "")
	if query == "" {
		return nil, errors.New("git: query is required")
	}
	args := []string{"grep", "--no-color", "-n", "--column"}
	if !boolArg(a, "regex", true) {
		args = append(args, "-F")
	} else {
		args = append(args, "-E")
	}
	args = append(args, query)
	if path := strArg(a, "path", ""); path != "" {
		args = append(args, "--", path)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		// git grep returns 1 when no matches are found.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return json.Marshal(map[string]any{"matches": []string{}})
		}
		return nil, err
	}
	matches := strings.Split(strings.TrimRight(out, "\n"), "\n")
	return json.Marshal(map[string]any{"matches": matches})
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

// run executes git with the supplied arguments and returns its
// combined stdout. Stderr is captured and surfaced in the error
// message on failure.
func (c *Connector) run(ctx context.Context, args ...string) (string, error) {
	if c.cfg.Repo == "" {
		return "", errors.New("git: not initialised")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.TimeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.cfg.Repo
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git %s: timed out after %ds", args[0], c.cfg.TimeoutSec)
		}
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func strArg(a map[string]any, key, def string) string {
	if v, ok := a[key].(string); ok {
		return v
	}
	return def
}

func intArg(a map[string]any, key string, def int) int {
	if v, ok := a[key].(float64); ok {
		return int(v)
	}
	if v, ok := a[key].(int); ok {
		return v
	}
	return def
}

func boolArg(a map[string]any, key string, def bool) bool {
	if v, ok := a[key].(bool); ok {
		return v
	}
	return def
}

func merge(m map[string]any) connector.JSONSchema {
	b, _ := json.Marshal(m)
	return b
}

// manifestSchema is registered at boot.
var manifestSchema = connector.Manifest{
	Name:        "git",
	Version:     "0.1.0",
	DisplayName: "Git",
	Description: "Read-only operations on a local git repository.",
	Capabilities: []string{
		connector.CapabilityTools,
	},
	ConfigSchema: connector.JSONSchema(`{
  "type": "object",
  "required": ["repo"],
  "properties": {
    "repo":            {"type": "string",  "title": "Repository path", "description": "Local filesystem path to a git repo"},
    "read_only":       {"type": "boolean", "title": "Read-only", "default": true},
    "timeout_seconds": {"type": "integer", "title": "Per-call timeout (s)", "default": 15, "minimum": 1},
    "max_log_entries": {"type": "integer", "title": "Max log entries", "default": 50, "minimum": 1}
  }
}`),
}