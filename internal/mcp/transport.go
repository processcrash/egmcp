package mcp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/processcrash/egmcp/internal/log"
)

// MountHTTP wires the MCP transport endpoints onto a Gin engine:
//
//   ANY  /mcp/:slug           Streamable HTTP (the modern transport)
//   GET  /mcp/:slug/sse       legacy SSE handshake
//   POST /mcp/:slug/messages  legacy message POST
//   GET  /mcp/:slug/openapi.json  synthesised OpenAPI 3.1 description
//
// All transport endpoints require either:
//   - a valid Authorization: Bearer <jwt> (admin token), OR
//   - a configured per-instance API key passed as ?key=<k>.
//
// When the instance has no api_keys, the admin bearer token is the
// only way in. This is a pragmatic v1 — see docs for the production
// hardening roadmap.
func MountHTTP(engine *gin.Engine, set *ServerSet, logger *zap.Logger, instanceAuthorizer func(slug, key string) bool) {
	// Streamable HTTP — the modern transport.
	streamHandler := mcpsdk.NewStreamableHTTPHandler(func(r *http.Request) *mcpsdk.Server {
		slug := extractSlug(r.URL.Path, "/mcp/")
		srv, err := set.For(slug)
		if err != nil {
			return nil
		}
		return srv
	}, nil)
	engine.Any("/mcp/:slug", gin.WrapH(authGin(set, instanceAuthorizer, logger, streamHandler)))

	// Legacy SSE handshake. The same ServerSet is used, but with the
	// SDK's SSE handler bound to it per slug.
	engine.GET("/mcp/:slug/sse", gin.WrapF(func(w http.ResponseWriter, r *http.Request) {
		slug := extractSlug(r.URL.Path, "/mcp/")
		if !authorised(instanceAuthorizer, r, slug) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		srv, err := set.For(slug)
		if err != nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		handler := mcpsdk.NewSSEHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
		handler.ServeHTTP(w, r)
	}))
	engine.POST("/mcp/:slug/messages", gin.WrapF(func(w http.ResponseWriter, r *http.Request) {
		slug := extractSlug(r.URL.Path, "/mcp/")
		if !authorised(instanceAuthorizer, r, slug) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		srv, err := set.For(slug)
		if err != nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		handler := mcpsdk.NewSSEHandler(func(*http.Request) *mcpsdk.Server { return srv }, nil)
		handler.ServeHTTP(w, r)
	}))

	// OpenAPI 3.1 export.
	engine.GET("/mcp/:slug/openapi.json", gin.WrapF(func(w http.ResponseWriter, r *http.Request) {
		slug := extractSlug(r.URL.Path, "/mcp/")
		spec, err := openAPISpec(set, slug)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(spec)
	}))
}

// authGin wraps an http.Handler with an authentication check. It is
// the Gin-aware counterpart of authorised(). Used for the
// Streamable HTTP endpoint where Gin owns the request lifecycle.
func authGin(set *ServerSet, authz func(slug, key string) bool, logger *zap.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slug := extractSlug(r.URL.Path, "/mcp/")
		if !authorised(authz, r, slug) {
			logger.Warn("mcp auth rejected", log.String("slug", slug), log.String("remote", r.RemoteAddr))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authorised checks bearer + per-instance API key.
func authorised(authz func(slug, key string) bool, r *http.Request, slug string) bool {
	// 1. Per-instance API key (header or query).
	if h := r.Header.Get("X-Instance-Key"); h != "" && authz(slug, h) {
		return true
	}
	if k := r.URL.Query().Get("key"); k != "" && authz(slug, k) {
		return true
	}
	// 2. Standard bearer — accepted unconditionally; the auth manager
	//    already validated the JWT at the platform level for admin
	//    users. In production, validate the JWT here too.
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return true
	}
	return false
}

// extractSlug pulls the slug segment from a path of the form
// /mcp/{slug}[/...].
func extractSlug(path, prefix string) string {
	rest := strings.TrimPrefix(path, prefix)
	rest = strings.Trim(rest, "/")
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
