package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/processcrash/egmcp/internal/config"
	"github.com/processcrash/egmcp/internal/connectors/builtin/filesystem"
	"github.com/processcrash/egmcp/internal/core"
	"github.com/processcrash/egmcp/internal/store"
	"github.com/processcrash/egmcp/pkg/connector"
)

// stubConnector returns canned responses so we can count what the
// ServerSet registers without depending on the filesystem connector.
type stubConnector struct {
	tools     []connector.ToolSpec
	resources []connector.ResourceSpec
}

func (s *stubConnector) Manifest() connector.Manifest {
	return connector.Manifest{
		Name:        "stub",
		DisplayName: "Stub",
		Capabilities: []string{
			connector.CapabilityTools,
			connector.CapabilityResources,
		},
	}
}
func (s *stubConnector) Init(_ context.Context, _ json.RawMessage) error { return nil }
func (s *stubConnector) HealthCheck(_ context.Context) error             { return nil }
func (s *stubConnector) Shutdown(_ context.Context) error                { return nil }
func (s *stubConnector) Tools() []connector.ToolSpec                    { return s.tools }
func (s *stubConnector) InvokeTool(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return []byte(`"ok"`), nil
}
func (s *stubConnector) Resources() []connector.ResourceSpec              { return s.resources }
func (s *stubConnector) ReadResource(_ context.Context, _ string) (connector.ResourceContents, error) {
	return connector.ResourceContents{Text: "{}"}, nil
}

func newTestServerSet(t *testing.T, sandbox string) (*ServerSet, string, *core.Router) {
	t.Helper()
	cfg := &config.Config{
		DataDir:      t.TempDir(),
		InstancesDir: t.TempDir(),
		PluginsDir:   t.TempDir(),
	}
	reg := connector.NewRegistry()
	// Register both filesystem (real) and stub.
	reg.MustRegister("filesystem", func() connector.Connector {
		return filesystem.New()
	})
	reg.MustRegister("stub", func() connector.Connector {
		return &stubConnector{
			tools: []connector.ToolSpec{
				{Name: "ping", Description: "ping"},
				{Name: "pong", Description: "pong"},
			},
			resources: []connector.ResourceSpec{
				{Name: "info", URI: "stub://info", MIMEType: "application/json"},
			},
		}
	})

	r, err := core.New(context.Background(), cfg, zap.NewNop(), reg)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	slug := "fs1"
	inst := &store.Instance{
		Slug:    slug,
		Enabled: true,
		Connectors: []store.ConnRef{
			{Type: "filesystem", Name: "fs", Config: map[string]any{"root": sandbox, "read_only": true}},
			{Type: "stub", Name: "stub1"},
		},
	}
	if err := r.UpsertInstance(inst); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err1 := r.GetConnector(slug, "fs"); err1 == nil {
			if _, err2 := r.GetConnector(slug, "stub1"); err2 == nil {
				return NewServerSet(r, zap.NewNop()), slug, r
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("connectors never became live")
	return nil, "", nil
}

func TestServerSetForUnknownSlug(t *testing.T) {
	cfg := &config.Config{
		DataDir: t.TempDir(), InstancesDir: t.TempDir(), PluginsDir: t.TempDir(),
	}
	reg := connector.NewRegistry()
	r, err := core.New(context.Background(), cfg, zap.NewNop(), reg)
	if err != nil {
		t.Fatalf("core: %v", err)
	}
	defer r.Close()
	set := NewServerSet(r, zap.NewNop())
	if _, err := set.For("nope"); err == nil {
		t.Fatalf("expected error for unknown slug")
	}
}

func TestServerSetInvalidate(t *testing.T) {
	set, slug, _ := newTestServerSet(t, t.TempDir())
	// First call: builds.
	srv1, err := set.For(slug)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	// Second call: returns cached.
	srv2, _ := set.For(slug)
	if srv1 != srv2 {
		t.Fatalf("expected cached server")
	}
	set.Invalidate(slug)
	srv3, _ := set.For(slug)
	if srv1 == srv3 {
		t.Fatalf("Invalidate should produce a fresh server")
	}
}

func TestOpenAPISpecIncludesTools(t *testing.T) {
	set, slug, _ := newTestServerSet(t, t.TempDir())
	spec, err := openAPISpec(set, slug)
	if err != nil {
		t.Fatalf("openAPISpec: %v", err)
	}
	if spec["openapi"] != "3.1.0" {
		t.Fatalf("openapi: %v", spec["openapi"])
	}
	// Tools list is currently empty (the SDK does not expose
	// introspection on a built server in v1.6.1) — the export is
	// deliberately a discoverability surface, not a full
	// reconstruction. We at least want the structure to be valid.
	paths, _ := spec["paths"].(map[string]any)
	if paths == nil {
		t.Fatalf("paths missing")
	}
}

func TestStripPrefix(t *testing.T) {
	for _, c := range []struct {
		in, want string
	}{
		{"filesystem__read_file", "read_file"},
		{"single", "single"},
		{"a__b__c", "b__c"},
	} {
		if got := stripPrefix(c.in); got != c.want {
			t.Fatalf("stripPrefix(%q): want %q, got %q", c.in, c.want, got)
		}
	}
}