// Package filesystem implements a Connector that exposes a sandboxed
// directory to MCP clients as a standard set of read/write/list tools
// and a directory-tree resource.
//
// The connector is deliberately conservative: every path is
// canonicalised and validated against the configured root before any
// I/O. Symlinks that escape the root are rejected.
package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/processcrash/egmcp/pkg/connector"
)

// Config is the per-instance config the filesystem connector expects.
// The schema is declared in manifestSchema below; validation is the
// caller's job (here we just check that root is non-empty and
// resolvable).
type Config struct {
	// Root is the only directory the connector will read from or
	// write to. Relative paths are resolved against the platform's
	// data dir.
	Root string `json:"root"`
	// ReadOnly disables write/delete tools.
	ReadOnly bool `json:"read_only"`
	// MaxFileBytes caps the size returned by read_file. Defaults to
	// 4 MiB when zero.
	MaxFileBytes int `json:"max_file_bytes"`
}

// Connector is the filesystem connector.
type Connector struct {
	manifest connector.Manifest
	root     string
	readOnly bool
	maxBytes int
}

// New returns a Connector with a static manifest. The instance root
// is supplied in Init, not here.
func New() *Connector {
	return &Connector{manifest: manifestSchema}
}

// Manifest returns the static description.
func (c *Connector) Manifest() connector.Manifest { return c.manifest }

// Init validates the supplied config and prepares the connector.
func (c *Connector) Init(_ context.Context, raw json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("filesystem: parse config: %w", err)
	}
	if cfg.Root == "" {
		return errors.New("filesystem: root is required")
	}
	abs, err := filepath.Abs(cfg.Root)
	if err != nil {
		return fmt.Errorf("filesystem: resolve root: %w", err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("filesystem: stat root: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("filesystem: root %q is not a directory", abs)
	}
	c.root = abs
	c.readOnly = cfg.ReadOnly
	c.maxBytes = cfg.MaxFileBytes
	if c.maxBytes <= 0 {
		c.maxBytes = 4 << 20 // 4 MiB
	}
	return nil
}

// HealthCheck ensures the root still exists and is readable.
func (c *Connector) HealthCheck(_ context.Context) error {
	if c.root == "" {
		return errors.New("filesystem: not initialised")
	}
	st, err := os.Stat(c.root)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("root %q is not a directory", c.root)
	}
	return nil
}

// Shutdown is a no-op for the filesystem connector; there is nothing
// to release.
func (c *Connector) Shutdown(_ context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────
// resolve + helpers
// ─────────────────────────────────────────────────────────────────────

// resolve maps an incoming path to an absolute path inside the root
// and rejects anything that would escape via "..", absolute paths, or
// symlinks. It returns the cleaned absolute path or an error.
func (c *Connector) resolve(p string) (string, error) {
	if c.root == "" {
		return "", errors.New("filesystem: not initialised")
	}
	if p == "" {
		return c.root, nil
	}
	joined := filepath.Join(c.root, filepath.Clean("/"+p))
	abs, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(c.root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes root", p)
	}
	// Reject symlinks that point outside the sandbox.
	if st, err := os.Lstat(abs); err == nil && st.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		if !strings.HasPrefix(target, c.root) {
			return "", fmt.Errorf("symlink %q escapes root", p)
		}
	}
	return abs, nil
}

func (c *Connector) checkWritable() error {
	if c.readOnly {
		return errors.New("filesystem: read-only mode")
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────
// Tools + Resources
// ─────────────────────────────────────────────────────────────────────

// Tools returns the connector's tool inventory. Tool names are
// namespaced at runtime as "filesystem:<name>".
func (c *Connector) Tools() []connector.ToolSpec {
	return []connector.ToolSpec{
		{
			Name:        "read_file",
			Description: "Read a UTF-8 text file. Path is relative to the sandbox root.",
			InputSchema: schemaString("path"),
		},
		{
			Name:        "write_file",
			Description: "Write a UTF-8 text file. Overwrites existing content. Disabled in read-only mode.",
			InputSchema: mergeRawSchemas(
				schemaString("path"),
				schemaString("content"),
				connector.JSONSchema(`{"type":"object","properties":{"create_dirs":{"type":"boolean","default":false}}}`),
			),
		},
		{
			Name:        "list_dir",
			Description: "List the immediate children of a directory.",
			InputSchema: mergeRawSchemas(
				schemaString("path"),
				connector.JSONSchema(`{"type":"object","properties":{"with_hidden":{"type":"boolean","default":false}}}`),
			),
		},
		{
			Name:        "delete_file",
			Description: "Delete a file or empty directory. Disabled in read-only mode.",
			InputSchema: schemaString("path"),
			Annotations: connector.ToolAnnotations{DestructiveHint: true},
		},
		{
			Name:        "search",
			Description: "Recursively search for files whose name contains the given substring (case-insensitive).",
			InputSchema: mergeRawSchemas(
				schemaString("query"),
				connector.JSONSchema(`{"type":"object","properties":{"max_results":{"type":"integer","minimum":1,"default":100}}}`),
			),
		},
	}
}

// InvokeTool dispatches a tool call.
func (c *Connector) InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	var a map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("filesystem: parse args: %w", err)
		}
	}
	getString := func(k string) string {
		v, _ := a[k].(string)
		return v
	}
	getBool := func(k string, def bool) bool {
		v, ok := a[k].(bool)
		if !ok {
			return def
		}
		return v
	}
	getInt := func(k string, def int) int {
		switch n := a[k].(type) {
		case float64:
			return int(n)
		case int:
			return n
		default:
			return def
		}
	}

	switch name {
	case "read_file":
		return c.toolReadFile(ctx, getString("path"))
	case "write_file":
		if err := c.checkWritable(); err != nil {
			return nil, err
		}
		return c.toolWriteFile(ctx, getString("path"), getString("content"), getBool("create_dirs", false))
	case "list_dir":
		return c.toolListDir(ctx, getString("path"), getBool("with_hidden", false))
	case "delete_file":
		if err := c.checkWritable(); err != nil {
			return nil, err
		}
		return c.toolDeleteFile(ctx, getString("path"))
	case "search":
		return c.toolSearch(ctx, getString("query"), getInt("max_results", 100))
	default:
		return nil, fmt.Errorf("filesystem: unknown tool %q", name)
	}
}

func (c *Connector) toolReadFile(_ context.Context, p string) (json.RawMessage, error) {
	abs, err := c.resolve(p)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%q is a directory", p)
	}
	if st.Size() > int64(c.maxBytes) {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", st.Size(), c.maxBytes)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"path":     relPath(c.root, abs),
		"size":     st.Size(),
		"modified": st.ModTime().UTC().Format(time.RFC3339),
		"content":  string(data),
	}
	return json.Marshal(out)
}

func (c *Connector) toolWriteFile(_ context.Context, p, content string, createDirs bool) (json.RawMessage, error) {
	abs, err := c.resolve(p)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err == nil && st.IsDir() {
		return nil, fmt.Errorf("%q is a directory", p)
	}
	if createDirs {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"path":     relPath(c.root, abs),
		"size":     st.Size(),
		"modified": st.ModTime().UTC().Format(time.RFC3339),
	})
}

func (c *Connector) toolListDir(_ context.Context, p string, withHidden bool) (json.RawMessage, error) {
	abs, err := c.resolve(p)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", p)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !withHidden && strings.HasPrefix(name, ".") {
			continue
		}
		items = append(items, map[string]any{
			"name":  name,
			"isDir": e.IsDir(),
			"size":  e.Type().String(),
		})
	}
	return json.Marshal(map[string]any{
		"path":    relPath(c.root, abs),
		"entries": items,
	})
}

func (c *Connector) toolDeleteFile(_ context.Context, p string) (json.RawMessage, error) {
	abs, err := c.resolve(p)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		// Refuse to delete non-empty directories to keep this
		// connector's blast radius small.
		entries, _ := os.ReadDir(abs)
		if len(entries) > 0 {
			return nil, fmt.Errorf("refusing to delete non-empty directory %q", p)
		}
	}
	if err := os.Remove(abs); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"path":    relPath(c.root, abs),
		"deleted": true,
	})
}

func (c *Connector) toolSearch(_ context.Context, query string, maxResults int) (json.RawMessage, error) {
	if query == "" {
		return nil, errors.New("filesystem: query is required")
	}
	q := strings.ToLower(query)
	hits := make([]map[string]any, 0, 16)
	err := filepath.Walk(c.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if len(hits) >= maxResults {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(c.root, path)
		if rel == "." {
			return nil
		}
		if strings.Contains(strings.ToLower(info.Name()), q) {
			hits = append(hits, map[string]any{
				"path":  rel,
				"isDir": info.IsDir(),
				"size":  info.Size(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"query":   query,
		"results": hits,
	})
}

// Resources exposes the directory tree at a single root resource.
func (c *Connector) Resources() []connector.ResourceSpec {
	if c.root == "" {
		return nil
	}
	return []connector.ResourceSpec{{
		Name:        "tree",
		URI:         "fs://tree",
		Description: "Recursive listing of files in the sandbox root",
		MIMEType:    "application/json",
	}}
}

func (c *Connector) ReadResource(_ context.Context, uri string) (connector.ResourceContents, error) {
	if c.root == "" {
		return connector.ResourceContents{}, errors.New("filesystem: not initialised")
	}
	if uri != "fs://tree" {
		return connector.ResourceContents{}, fmt.Errorf("filesystem: unknown resource %q", uri)
	}
	type entry struct {
		Path  string `json:"path"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	var entries []entry
	_ = filepath.Walk(c.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(c.root, path)
		if rel == "." {
			return nil
		}
		var sz int64
		if !info.IsDir() {
			sz = info.Size()
		}
		entries = append(entries, entry{Path: rel, IsDir: info.IsDir(), Size: sz})
		return nil
	})
	data, _ := json.MarshalIndent(entries, "", "  ")
	return connector.ResourceContents{
		URI:      uri,
		MIMEType: "application/json",
		Text:     string(data),
	}, nil
}

func relPath(root, abs string) string {
	rel, _ := filepath.Rel(root, abs)
	if rel == "" {
		return "."
	}
	return filepath.ToSlash(rel)
}

// ─────────────────────────────────────────────────────────────────────
// JSON Schema helpers
// ─────────────────────────────────────────────────────────────────────

func schemaString(name string) connector.JSONSchema {
	b, _ := json.Marshal(map[string]any{
		"type":     "object",
		"required": []string{name},
		"properties": map[string]any{
			name: map[string]any{"type": "string"},
		},
	})
	return b
}

// mergeRawSchemas combines a list of partial JSON Schemas (encoded as
// raw JSON) into a single object schema, accumulating required
// properties.
func mergeRawSchemas(parts ...connector.JSONSchema) connector.JSONSchema {
	out := map[string]any{"type": "object"}
	props := map[string]any{}
	var required []string
	for _, p := range parts {
		var part map[string]any
		if err := json.Unmarshal(p, &part); err != nil {
			continue
		}
		if r, ok := part["required"].([]any); ok {
			for _, v := range r {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		}
		if pr, ok := part["properties"].(map[string]any); ok {
			for k, v := range pr {
				props[k] = v
			}
		}
	}
	if len(props) > 0 {
		out["properties"] = props
	}
	if len(required) > 0 {
		out["required"] = required
	}
	b, _ := json.Marshal(out)
	return b
}

// manifestSchema is the static description registered with the
// platform at boot.
var manifestSchema = connector.Manifest{
	Name:        "filesystem",
	Version:     "0.1.0",
	DisplayName: "Filesystem",
	Description: "Read, write, list and search a sandboxed directory.",
	Capabilities: []string{
		connector.CapabilityTools,
		connector.CapabilityResources,
	},
	ConfigSchema: connector.JSONSchema(`{
  "type": "object",
  "required": ["root"],
  "properties": {
    "root": {
      "type": "string",
      "title": "Root directory",
      "description": "The directory the connector is allowed to read from and write to."
    },
    "read_only": {
      "type": "boolean",
      "title": "Read-only",
      "default": false
    },
    "max_file_bytes": {
      "type": "integer",
      "title": "Max file size (bytes)",
      "description": "Cap on bytes returned by read_file.",
      "minimum": 1024
    }
  }
}`),
}
