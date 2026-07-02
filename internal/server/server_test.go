package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/processcrash/egmcp/internal/config"
	"github.com/processcrash/egmcp/internal/core"
	"github.com/processcrash/egmcp/internal/log"
)

func TestHealthzReturns200(t *testing.T) {
	logger, err := log.New("debug")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}

	cfg := &config.Config{
		Server: config.ServerConfig{Listen: ":0"},
		DataDir: t.TempDir(),
	}
	r, err := core.New(cfg, logger)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}

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
	logger, err := log.New("debug")
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	cfg := &config.Config{DataDir: t.TempDir()}
	r, err := core.New(cfg, logger)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
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
