package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/processcrash/egmcp/internal/config"
	"github.com/processcrash/egmcp/internal/core"
	"github.com/processcrash/egmcp/internal/log"
	"github.com/processcrash/egmcp/pkg/connector"
	"go.uber.org/zap"
)

func newTestRouter(t *testing.T) (*core.Router, *config.Config, *zap.Logger) {
	t.Helper()
	logger, err := log.New("debug")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	cfg := &config.Config{
		Server:       config.ServerConfig{Listen: ":0"},
		DataDir:      t.TempDir(),
		InstancesDir: t.TempDir(),
		PluginsDir:   t.TempDir(),
	}
	r, err := core.New(context.Background(), cfg, logger, connector.NewRegistry())
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	return r, cfg, logger
}

func TestHealthzReturns200(t *testing.T) {
	r, cfg, logger := newTestRouter(t)
	defer r.Close()

	mux := NewMux(r, cfg, logger)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: want application/json, got %q", ct)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field: want ok, got %v", body["status"])
	}
	if _, ok := body["uptime"]; !ok {
		t.Fatalf("missing uptime field: %v", body)
	}
}

func TestReadyzMirrorsHealthz(t *testing.T) {
	r, cfg, logger := newTestRouter(t)
	defer r.Close()
	mux := NewMux(r, cfg, logger)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz: want 200, got %d", resp.StatusCode)
	}
}
