package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"github.com/processcrash/egmcp/internal/auth"
	"github.com/processcrash/egmcp/internal/config"
	"github.com/processcrash/egmcp/internal/core"
	"github.com/processcrash/egmcp/internal/store"
	"github.com/processcrash/egmcp/pkg/connector"
)

func init() { gin.SetMode(gin.TestMode) }

func newAPI(t *testing.T) (*gin.Engine, *core.Router, string) {
	t.Helper()
	cfg := &config.Config{
		Server:       config.ServerConfig{Listen: ":0"},
		DataDir:      t.TempDir(),
		InstancesDir: t.TempDir(),
		PluginsDir:   t.TempDir(),
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	authMgr, err := auth.NewManager("admin", string(hash),
		"secret-secret-secret-secret-12345", time.Hour)
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	reg := connector.NewRegistry()
	reg.MustRegister("fake", func() connector.Connector {
		return fakeConnector{manifest: connector.Manifest{
			Name:        "fake",
			DisplayName: "Fake",
			Description: "For tests",
			Capabilities: []string{connector.CapabilityTools},
			ConfigSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
		}}
	})
	r, err := core.New(context.Background(), cfg, zap.NewNop(), reg)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	g := gin.New()
	g.Use(func(c *gin.Context) { c.Next() })
	Mount(g, &API{
		Router:   r,
		Auth:     authMgr,
		Registry: reg,
		Logger:   zap.NewNop(),
	})
	return g, r, "hunter2"
}

// fakeConnector is a thin test double used across API tests.
type fakeConnector struct {
	manifest connector.Manifest
}

func (f fakeConnector) Manifest() connector.Manifest             { return f.manifest }
func (f fakeConnector) Init(_ context.Context, _ json.RawMessage) error { return nil }
func (f fakeConnector) HealthCheck(_ context.Context) error     { return nil }
func (f fakeConnector) Shutdown(_ context.Context) error        { return nil }

func login(t *testing.T, g *gin.Engine, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": password,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login: code %d, body %s", w.Code, w.Body.String())
	}
	var resp loginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("login decode: %v", err)
	}
	if resp.Token == "" {
		t.Fatalf("login returned empty token")
	}
	return resp.Token
}

func bearer(token string) string { return "Bearer " + token }

func TestLoginBadCredentials(t *testing.T) {
	g, _, _ := newAPI(t)
	body, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "wrong",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestMeRequiresToken(t *testing.T) {
	g, _, _ := newAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestMeWithToken(t *testing.T) {
	g, _, pwd := newAPI(t)
	tok := login(t, g, pwd)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", bearer(tok))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["username"] != "admin" {
		t.Fatalf("username: %v", body["username"])
	}
}

func TestBuiltinConnectors(t *testing.T) {
	g, _, pwd := newAPI(t)
	tok := login(t, g, pwd)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connectors/builtin", nil)
	req.Header.Set("Authorization", bearer(tok))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var body struct {
		Connectors []struct {
			Name string `json:"name"`
		} `json:"connectors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Connectors) != 1 || body.Connectors[0].Name != "fake" {
		t.Fatalf("connectors: %+v", body)
	}
}

func TestInstanceCRUD(t *testing.T) {
	g, _, pwd := newAPI(t)
	tok := login(t, g, pwd)

	// Create.
	inst := store.Instance{
		Slug:        "alpha",
		DisplayName: "Alpha",
		Enabled:     true,
		Connectors: []store.ConnRef{{
			Type: "fake",
			Name: "main",
			Config: map[string]any{
				"name": "primary",
			},
		}},
	}
	body, _ := json.Marshal(&inst)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(tok))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", w.Code, w.Body.String())
	}

	// List.
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/instances", nil)
	listReq.Header.Set("Authorization", bearer(tok))
	g.ServeHTTP(httptest.NewRecorder(), listReq)
	w = httptest.NewRecorder()
	g.ServeHTTP(w, listReq)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"alpha"`) {
		t.Fatalf("list missing alpha: %s", w.Body.String())
	}

	// Get.
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/instances/alpha", nil)
	getReq.Header.Set("Authorization", bearer(tok))
	w = httptest.NewRecorder()
	g.ServeHTTP(w, getReq)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d", w.Code)
	}

	// Test connectors.
	testReq := httptest.NewRequest(http.MethodPost, "/api/v1/instances/alpha/test", nil)
	testReq.Header.Set("Authorization", bearer(tok))
	w = httptest.NewRecorder()
	g.ServeHTTP(w, testReq)
	if w.Code != http.StatusOK {
		t.Fatalf("test: %d body=%s", w.Code, w.Body.String())
	}
	var tr struct {
		Results []testResult `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tr); err != nil {
		t.Fatalf("test decode: %v", err)
	}
	if len(tr.Results) != 1 || tr.Results[0].Status != "ok" {
		t.Fatalf("test results: %+v", tr.Results)
	}

	// Delete.
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/instances/alpha", nil)
	delReq.Header.Set("Authorization", bearer(tok))
	w = httptest.NewRecorder()
	g.ServeHTTP(w, delReq)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
}

func TestCreateRejectsBadSlug(t *testing.T) {
	g, _, pwd := newAPI(t)
	tok := login(t, g, pwd)

	inst := store.Instance{Slug: "BadSlug", Connectors: []store.ConnRef{{Type: "fake", Name: "n"}}}
	body, _ := json.Marshal(&inst)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(tok))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateRejectsDuplicateSlug(t *testing.T) {
	g, _, pwd := newAPI(t)
	tok := login(t, g, pwd)

	inst := store.Instance{
		Slug: "alpha",
		Connectors: []store.ConnRef{{Type: "fake", Name: "main"}},
	}
	body, _ := json.Marshal(&inst)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(tok))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("first create: %d body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(tok))
	w = httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate create: want 409, got %d", w.Code)
	}
}

func TestGetUnknownInstance(t *testing.T) {
	g, _, pwd := newAPI(t)
	tok := login(t, g, pwd)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/instances/ghost", nil)
	req.Header.Set("Authorization", bearer(tok))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestRotateKey(t *testing.T) {
	g, _, pwd := newAPI(t)
	tok := login(t, g, pwd)

	inst := store.Instance{
		Slug: "rot",
		Connectors: []store.ConnRef{{Type: "fake", Name: "main"}},
	}
	body, _ := json.Marshal(&inst)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(tok))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/instances/rot/rotate-key", nil)
	req.Header.Set("Authorization", bearer(tok))
	w = httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("rotate: %d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["new_key"] == "" {
		t.Fatalf("missing new_key")
	}
	if len(resp["new_key"]) < 32 {
		t.Fatalf("new_key too short: %q", resp["new_key"])
	}
}

func TestRefreshWithoutTokenActsAsLogin(t *testing.T) {
	g, _, pwd := newAPI(t)
	body, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": pwd,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: %d", w.Code)
	}
}
