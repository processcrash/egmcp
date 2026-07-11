// Package api wires the /api/v1/* HTTP surface of the platform.
//
// Endpoints (all under /api/v1):
//
//   POST   /auth/login              public, returns JWT
//   GET    /me                      protected, returns the admin profile
//   GET    /instances               protected, list all instances
//   POST   /instances               protected, create or replace
//   GET    /instances/{slug}        protected
//   PUT    /instances/{slug}        protected, full replace
//   DELETE /instances/{slug}        protected
//   POST   /instances/{slug}/test   protected, HealthCheck each connector
//   GET    /connectors/builtin      protected, list registered connectors
//   GET    /plugins                 protected, list plugin metadata (M6)
//
// The package exposes a single Mount function that registers all
// routes against a Gin engine. Mount also installs the auth
// middleware on the protected sub-tree.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/processcrash/egmcp/internal/auth"
	"github.com/processcrash/egmcp/internal/core"
	"github.com/processcrash/egmcp/internal/log"
	egmcpplugin "github.com/processcrash/egmcp/internal/plugin"
	"github.com/processcrash/egmcp/internal/store"
	"github.com/processcrash/egmcp/pkg/connector"
)

// slugRe mirrors the same constraint as store.Instance.Validate.
// Duplicated here for early HTTP-layer rejection before touching disk.
var slugRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// API is the dependency bundle shared across handlers.
type API struct {
	Router    *core.Router
	Auth      *auth.Manager
	Registry  *connector.Registry
	Logger    *zap.Logger
	Plugins   *egmcpplugin.Manager
}

// Mount registers the API on the provided engine. The caller is
// expected to have already wired root middleware (recovery,
// request-id, request log).
func Mount(r *gin.Engine, api *API) {
	v1 := r.Group("/api/v1")
	v1.POST("/auth/login", api.handleLogin)
	v1.POST("/auth/refresh", api.handleRefresh)

	// Everything below requires a valid bearer token.
	authed := v1.Group("")
	authed.Use(api.Auth.Middleware())
	authed.GET("/me", api.handleMe)
	authed.GET("/connectors/builtin", api.handleBuiltinConnectors)
	authed.GET("/plugins", api.handlePluginsList)
	authed.POST("/plugins/upload", api.handlePluginUpload)
	authed.DELETE("/plugins/:name", api.handlePluginDelete)

	authed.GET("/instances", api.handleListInstances)
	authed.POST("/instances", api.handleCreateInstance)
	authed.GET("/instances/:slug", api.handleGetInstance)
	authed.PUT("/instances/:slug", api.handleReplaceInstance)
	authed.DELETE("/instances/:slug", api.handleDeleteInstance)
	authed.POST("/instances/:slug/test", api.handleTestInstance)
	authed.POST("/instances/:slug/rotate-key", api.handleRotateKey)
}

// ─────────────────────────────────────────────────────────────────────
// plugins
// ─────────────────────────────────────────────────────────────────────

func (a *API) handlePluginsList(c *gin.Context) {
	if a.Plugins == nil {
		c.JSON(http.StatusOK, gin.H{"plugins": []any{}})
		return
	}
	manifests, err := a.Plugins.Scan()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "SCAN_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"plugins": manifests})
}

func (a *API) handlePluginUpload(c *gin.Context) {
	if a.Plugins == nil {
		writeError(c, http.StatusServiceUnavailable, "PLUGINS_DISABLED", "plugin support is not enabled")
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_INPUT", "missing file field")
		return
	}
	if file.Size > 50*1024*1024 {
		writeError(c, http.StatusBadRequest, "FILE_TOO_LARGE", "plugin exceeds 50MiB limit")
		return
	}
	src, err := file.Open()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "READ_FAILED", err.Error())
		return
	}
	defer src.Close()
	data := make([]byte, file.Size)
	if _, err := src.Read(data); err != nil {
		writeError(c, http.StatusInternalServerError, "READ_FAILED", err.Error())
		return
	}
	if err := a.Plugins.SaveUpload(file.Filename, data); err != nil {
		writeError(c, http.StatusBadRequest, "LOAD_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusCreated, gin.H{"name": filepathBase(file.Filename), "size": file.Size})
}

func (a *API) handlePluginDelete(c *gin.Context) {
	name := c.Param("name")
	if a.Plugins == nil {
		writeError(c, http.StatusServiceUnavailable, "PLUGINS_DISABLED", "plugin support is not enabled")
		return
	}
	if err := a.Plugins.Delete(name); err != nil {
		writeError(c, http.StatusBadRequest, "DELETE_FAILED", err.Error())
		return
	}
	c.Status(http.StatusNoContent)
}

// filepathBase is a tiny wrapper to avoid an extra import in this file
// when the helper is the only consumer.
func filepathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

// ─────────────────────────────────────────────────────────────────────
// auth
// ─────────────────────────────────────────────────────────────────────

type loginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type loginResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Username  string `json:"username"`
	TTL       int    `json:"ttl_seconds"`
}

func (a *API) handleLogin(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	if req.Username != a.Auth.ConfiguredUsername() {
		writeError(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "username or password is incorrect")
		return
	}
	if err := a.Auth.VerifyPassword(req.Password); err != nil {
		writeError(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "username or password is incorrect")
		return
	}
	tok, exp, err := a.Auth.Issue(req.Username)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "ISSUE_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, loginResponse{
		Token:     tok,
		ExpiresAt: exp.UTC().Format("2006-01-02T15:04:05Z"),
		Username:  req.Username,
		TTL:       a.Auth.LifetimeSeconds(),
	})
}

func (a *API) handleRefresh(c *gin.Context) {
	// Refresh is just a re-issue of an existing valid token.
	claims, ok := a.authClaims(c)
	if !ok {
		// If no token was provided, accept the username/password
		// again as a fresh login. This keeps the refresh endpoint
		// usable from clients that lost the original token.
		a.handleLogin(c)
		return
	}
	tok, exp, err := a.Auth.Issue(claims.Username)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "ISSUE_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, loginResponse{
		Token:     tok,
		ExpiresAt: exp.UTC().Format("2006-01-02T15:04:05Z"),
		Username:  claims.Username,
		TTL:       a.Auth.LifetimeSeconds(),
	})
}

func (a *API) handleMe(c *gin.Context) {
	claims, _ := a.authClaims(c)
	c.JSON(http.StatusOK, gin.H{
		"username": claims.Username,
		"subject":  claims.Subject,
	})
}

// ─────────────────────────────────────────────────────────────────────
// connectors
// ─────────────────────────────────────────────────────────────────────

type connectorDescriptor struct {
	Name         string          `json:"name"`
	DisplayName  string          `json:"displayName"`
	Description  string          `json:"description"`
	Capabilities []string        `json:"capabilities"`
	ConfigSchema json.RawMessage `json:"configSchema"`
}

func (a *API) handleBuiltinConnectors(c *gin.Context) {
	out := make([]connectorDescriptor, 0, 4)
	for _, name := range a.Registry.Names() {
		f, ok := a.Registry.Get(name)
		if !ok {
			continue
		}
		c := f()
		m := c.Manifest()
		out = append(out, connectorDescriptor{
			Name:         m.Name,
			DisplayName:  m.DisplayName,
			Description:  m.Description,
			Capabilities: m.Capabilities,
			ConfigSchema: m.ConfigSchema,
		})
	}
	c.JSON(http.StatusOK, gin.H{"connectors": out})
}

// ─────────────────────────────────────────────────────────────────────
// instances
// ─────────────────────────────────────────────────────────────────────

func (a *API) handleListInstances(c *gin.Context) {
	insts := a.Router.ListInstances()
	c.JSON(http.StatusOK, gin.H{"instances": insts})
}

func (a *API) handleGetInstance(c *gin.Context) {
	slug := c.Param("slug")
	inst, err := a.Router.GetInstance(slug)
	if err != nil {
		if errors.Is(err, core.ErrInstanceNotFound) {
			writeError(c, http.StatusNotFound, "NOT_FOUND", "instance not found")
			return
		}
		writeError(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	c.JSON(http.StatusOK, inst)
}

func (a *API) handleCreateInstance(c *gin.Context) {
	var inst store.Instance
	if err := c.ShouldBindJSON(&inst); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	if !slugRe.MatchString(inst.Slug) {
		writeError(c, http.StatusBadRequest, "INVALID_SLUG", "slug must match "+slugRe.String())
		return
	}
	if a.Router.HasInstance(inst.Slug) {
		writeError(c, http.StatusConflict, "ALREADY_EXISTS", "instance with this slug already exists")
		return
	}
	if err := a.Router.UpsertInstance(&inst); err != nil {
		writeError(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}
	a.Logger.Info("instance created",
		log.String("slug", inst.Slug),
		log.Int("connectors", len(inst.Connectors)),
	)
	c.JSON(http.StatusCreated, &inst)
}

func (a *API) handleReplaceInstance(c *gin.Context) {
	slug := c.Param("slug")
	var inst store.Instance
	if err := c.ShouldBindJSON(&inst); err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	if inst.Slug != "" && inst.Slug != slug {
		writeError(c, http.StatusBadRequest, "SLUG_MISMATCH", "URL slug and body slug must match")
		return
	}
	inst.Slug = slug
	if err := a.Router.UpsertInstance(&inst); err != nil {
		writeError(c, http.StatusBadRequest, "VALIDATION_FAILED", err.Error())
		return
	}
	c.JSON(http.StatusOK, &inst)
}

func (a *API) handleDeleteInstance(c *gin.Context) {
	slug := c.Param("slug")
	if !a.Router.HasInstance(slug) {
		writeError(c, http.StatusNotFound, "NOT_FOUND", "instance not found")
		return
	}
	if err := a.Router.DeleteInstance(slug); err != nil {
		writeError(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	a.Logger.Info("instance deleted", log.String("slug", slug))
	c.Status(http.StatusNoContent)
}

type testResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func (a *API) handleTestInstance(c *gin.Context) {
	slug := c.Param("slug")
	if !a.Router.HasInstance(slug) {
		writeError(c, http.StatusNotFound, "NOT_FOUND", "instance not found")
		return
	}
	results, err := a.Router.ValidateConnector(slug)
	if err != nil {
		writeError(c, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	out := make([]testResult, 0, len(results))
	for name, e := range results {
		r := testResult{Name: name, Status: "ok"}
		if e != nil {
			r.Status = "fail"
			r.Error = e.Error()
		}
		out = append(out, r)
	}
	c.JSON(http.StatusOK, gin.H{"results": out})
}

func (a *API) handleRotateKey(c *gin.Context) {
	slug := c.Param("slug")
	inst, err := a.Router.GetInstance(slug)
	if err != nil {
		writeError(c, http.StatusNotFound, "NOT_FOUND", "instance not found")
		return
	}
	newKey, err := randomKey(24)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "RANDOM", err.Error())
		return
	}
	inst.APIKeys = append(inst.APIKeys, newKey)
	if err := a.Router.UpsertInstance(inst); err != nil {
		writeError(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"new_key": newKey, "slug": slug})
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

func (a *API) authClaims(c *gin.Context) (*auth.Claims, bool) {
	v, ok := c.Get("auth.claims")
	if !ok {
		return nil, false
	}
	cl, ok := v.(*auth.Claims)
	return cl, ok
}

func writeError(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{"code": code, "message": msg},
	})
}
