package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

func init() { gin.SetMode(gin.TestMode) }

func newTestManager(t *testing.T, plain string) *Manager {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	mgr, err := NewManager("admin", string(hash), "secret-secret-secret-secret", time.Hour)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr
}

func TestVerifyPassword(t *testing.T) {
	m := newTestManager(t, "p@ssword!")
	if err := m.VerifyPassword("p@ssword!"); err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
	if err := m.VerifyPassword("nope"); err == nil {
		t.Fatalf("incorrect password accepted")
	}
	if err := m.VerifyPassword(""); err == nil {
		t.Fatalf("empty password accepted")
	}
}

func TestIssueAndParse(t *testing.T) {
	m := newTestManager(t, "hunter2")
	tok, exp, err := m.Issue("admin")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if exp.Before(time.Now()) {
		t.Fatalf("expiry must be in the future: %v", exp)
	}
	claims, err := m.Parse(tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.Username != "admin" {
		t.Fatalf("Username: %q", claims.Username)
	}
	if claims.Subject != "admin" {
		t.Fatalf("Subject: %q", claims.Subject)
	}
}

func TestParseRejectsForgedToken(t *testing.T) {
	m := newTestManager(t, "x")
	other, err := NewManager("admin", string(m.passwordHash), "different-different-different", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := other.Issue("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Parse(tok); err == nil {
		t.Fatalf("token signed with another secret must be rejected")
	}
}

func TestMiddlewareAllowsPublicPaths(t *testing.T) {
	m := newTestManager(t, "p")

	r := gin.New()
	r.Use(m.Middleware("/api/v1/auth/login", "/healthz"))
	r.POST("/api/v1/auth/login", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })
	r.GET("/healthz", func(c *gin.Context) { c.String(200, "ok") })
	r.GET("/api/v1/me", func(c *gin.Context) { c.String(200, "secret") })

	for _, path := range []string{"/api/v1/auth/login", "/healthz"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
		// login is POST; healthz is GET above; harmless either way.
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code == http.StatusUnauthorized {
			t.Fatalf("%s blocked: %d", path, w.Code)
		}
	}
}

func TestMiddlewareBlocksWithoutToken(t *testing.T) {
	m := newTestManager(t, "p")
	r := gin.New()
	r.Use(m.Middleware("/api/v1/auth/login"))
	r.GET("/api/v1/me", func(c *gin.Context) { c.String(200, "secret") })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", strings.NewReader(""))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object: %v", body)
	}
	if errObj["code"] != "UNAUTHORIZED" {
		t.Fatalf("error code: %v", errObj)
	}
}

func TestMiddlewareAcceptsBearer(t *testing.T) {
	m := newTestManager(t, "p")
	tok, _, _ := m.Issue("admin")

	r := gin.New()
	r.Use(m.Middleware())
	r.GET("/api/v1/me", func(c *gin.Context) {
		username, _ := c.Get("auth.username")
		c.JSON(200, gin.H{"username": username})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestMiddlewareAcceptsQueryToken(t *testing.T) {
	m := newTestManager(t, "p")
	tok, _, _ := m.Issue("admin")

	r := gin.New()
	r.Use(m.Middleware())
	r.GET("/api/v1/me", func(c *gin.Context) { c.String(200, "ok") })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me?token="+tok, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestNewManagerValidation(t *testing.T) {
	if _, err := NewManager("", "h", "secret-secret-secret-secret", time.Hour); err == nil {
		t.Fatalf("expected error for empty username")
	}
	if _, err := NewManager("u", "", "secret-secret-secret-secret", time.Hour); err == nil {
		t.Fatalf("expected error for empty hash")
	}
	if _, err := NewManager("u", "h", "short", time.Hour); err == nil {
		t.Fatalf("expected error for short secret")
	}
}
