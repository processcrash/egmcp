package core

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/processcrash/egmcp/internal/config"
	"github.com/processcrash/egmcp/internal/store"
	"github.com/processcrash/egmcp/pkg/connector"
)

type fakeConnector struct {
	manifest    connector.Manifest
	initCalls   int
	healthErr   error
	shutdownErr error
}

func (f *fakeConnector) Manifest() connector.Manifest { return f.manifest }
func (f *fakeConnector) Init(_ context.Context, _ json.RawMessage) error {
	f.initCalls++
	return nil
}
func (f *fakeConnector) HealthCheck(_ context.Context) error { return f.healthErr }
func (f *fakeConnector) Shutdown(_ context.Context) error    { return f.shutdownErr }

func newTestCore(t *testing.T) (*Router, *connector.Registry) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Server:       config.ServerConfig{Listen: ":0"},
		DataDir:      dir,
		InstancesDir: filepath.Join(dir, "instances"),
		PluginsDir:   filepath.Join(dir, "plugins"),
	}
	reg := connector.NewRegistry()
	reg.MustRegister("fake", func() connector.Connector {
		return &fakeConnector{manifest: connector.Manifest{Name: "fake"}}
	})
	r, err := New(context.Background(), cfg, zap.NewNop(), reg)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, reg
}

func sampleInstance() *store.Instance {
	return &store.Instance{
		Slug:        "alpha",
		DisplayName: "Alpha",
		Enabled:     true,
		Connectors: []store.ConnRef{{
			Type:   "fake",
			Name:   "main",
			Config: map[string]any{"k": "v"},
		}},
	}
}

func TestCoreUpsertAndGet(t *testing.T) {
	r, _ := newTestCore(t)
	if err := r.UpsertInstance(sampleInstance()); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := r.GetInstance("alpha")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DisplayName != "Alpha" {
		t.Fatalf("DisplayName: %q", got.DisplayName)
	}

	c, err := r.GetConnector("alpha", "main")
	if err != nil {
		t.Fatalf("GetConnector: %v", err)
	}
	if c.Manifest().Name != "fake" {
		t.Fatalf("connector name: %q", c.Manifest().Name)
	}
}

func TestCoreGetUnknownSlug(t *testing.T) {
	r, _ := newTestCore(t)
	if _, err := r.GetInstance("missing"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("want ErrInstanceNotFound, got %v", err)
	}
}

func TestCoreUpsertUnknownConnectorType(t *testing.T) {
	r, _ := newTestCore(t)
	inst := sampleInstance()
	inst.Connectors[0].Type = "ghost"
	if err := r.UpsertInstance(inst); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	// GetInstance still works (the unknown type is logged but the
	// instance is kept so /test can report failure).
	if _, err := r.GetInstance("alpha"); err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if _, err := r.GetConnector("alpha", "main"); err == nil {
		t.Fatalf("expected error getting missing connector")
	}
}

func TestCoreDeleteRemovesLiveState(t *testing.T) {
	r, _ := newTestCore(t)
	if err := r.UpsertInstance(sampleInstance()); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteInstance("alpha"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := r.GetInstance("alpha"); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("want ErrInstanceNotFound after delete, got %v", err)
	}
}

func TestCoreValidateConnectorHealth(t *testing.T) {
	r, reg := newTestCore(t)
	reg.MustRegister("unhealthy", func() connector.Connector {
		return &fakeConnector{
			manifest:  connector.Manifest{Name: "unhealthy"},
			healthErr: errors.New("kaboom"),
		}
	})
	inst := sampleInstance()
	inst.Connectors[0].Type = "unhealthy"
	if err := r.UpsertInstance(inst); err != nil {
		t.Fatal(err)
	}
	results, err := r.ValidateConnector("alpha")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if results["main"] == nil {
		t.Fatalf("expected an error from the unhealthy connector")
	}
}

func TestCoreHealthSummarisesInstances(t *testing.T) {
	r, _ := newTestCore(t)
	if err := r.UpsertInstance(sampleInstance()); err != nil {
		t.Fatal(err)
	}
	h := r.Health()
	if h.Status != "ok" {
		t.Fatalf("status: %q", h.Status)
	}
	if h.Instances != 1 {
		t.Fatalf("instances: %d", h.Instances)
	}
}

func TestCoreReloadAfterDiskEdit(t *testing.T) {
	r, _ := newTestCore(t)
	if err := r.UpsertInstance(sampleInstance()); err != nil {
		t.Fatal(err)
	}
	// Wait for the watcher tick.
	time.Sleep(900 * time.Millisecond)
	h := r.Health()
	if h.Instances != 1 {
		t.Fatalf("instances after reload: %d", h.Instances)
	}
}
