// Package server wires HTTP handlers and middleware around the core router.
//
// In M0 it exposes only /healthz and the static asset fallback. The
// richer /api/v1/* and /mcp/{slug}/* surfaces land in later milestones.
package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/processcrash/egmcp/internal/api"
	"github.com/processcrash/egmcp/internal/auth"
	"github.com/processcrash/egmcp/internal/config"
	"github.com/processcrash/egmcp/internal/core"
	"github.com/processcrash/egmcp/internal/log"
)

//go:embed assets
var assetsFS embed.FS

// NewMux composes the HTTP handler tree.
//
// Middleware order matters: outermost wrappers (recovery, request id,
// logging) run before business handlers and wrap every response.
func NewMux(router *core.Router, cfg *config.Config, logger *zap.Logger) http.Handler {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(ginRecovery(logger))
	engine.Use(ginRequestID())
	engine.Use(ginLogger(logger))

	// Health probes.
	engine.GET("/healthz", gin.WrapF(healthzHandler(router)))
	engine.GET("/readyz", gin.WrapF(healthzHandler(router)))

	// REST API.
	authMgr, err := auth.NewManager(
		cfg.Auth.AdminUsername,
		cfg.Auth.AdminPasswordHash,
		cfg.Auth.JWTSecret,
		auth.MustParseLifetime(""),
	)
	if err != nil {
		// We've already validated the secret on boot, so this should
		// be impossible. Still, log and fail closed.
		logger.Error("auth manager init", log.Err(err))
	}
	api.Mount(engine, &api.API{
		Router:   router,
		Auth:     authMgr,
		Registry: router.Registry(),
		Logger:   logger,
	})

	// Static frontend. Falls back to /index.html for SPA routes.
	engine.NoRoute(gin.WrapH(staticHandler(assetsFS, logger)))

	return engine
}

// healthzHandler reports the platform's liveness.
func healthzHandler(router *core.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := router.Health()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      h.Status,
			"uptime":      h.Uptime,
			"instance_id": h.InstanceID,
		})
	}
}

// staticHandler serves the embedded web/dist if present. If the
// frontend hasn't been built yet (e.g. running backend-only during
// development), it returns a friendly placeholder instead of an
// unhelpful 404.
func staticHandler(emb embed.FS, logger *zap.Logger) http.Handler {
	sub, err := fs.Sub(emb, "assets")
	if err != nil || isEmpty(sub) {
		logger.Warn("frontend assets not embedded — running backend-only")
		return placeholderHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SPA fallback: try the asset, then fall back to index.html.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			path = "index.html"
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + path
		fileServer.ServeHTTP(w, r2)
	})
}

func isEmpty(fsys fs.FS) bool {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil || len(entries) == 0 {
		return true
	}
	hasIndex := false
	for _, e := range entries {
		if e.Name() == "index.html" || e.Name() == "index.htm" {
			hasIndex = true
			break
		}
	}
	return !hasIndex
}

// placeholderHandler is used when no frontend has been embedded.
func placeholderHandler() http.Handler {
	body := []byte(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>egmcp</title></head>
<body style="font-family:system-ui;margin:3rem;max-width:40rem;color:#333">
<h1 style="margin:0 0 1rem">egmcp</h1>
<p>Backend is up. The admin console has not been built into this image yet.</p>
<p>See <code>/healthz</code> for a JSON status response.</p>
</body></html>`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}
