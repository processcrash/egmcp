// Package fetch implements a Connector that exposes a tiny HTTP
// client — useful as a fallback for any REST API that doesn't ship
// an OpenAPI document. The connector deliberately keeps the surface
// area small: GET, POST, PUT, DELETE plus a base URL and optional
// authentication.
package fetch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/processcrash/egmcp/pkg/connector"
)

// Config is the per-instance connector config.
type Config struct {
	// BaseURL is prepended to every path supplied to a tool.
	BaseURL string `json:"base_url"`
	// Headers are added to every request.
	Headers map[string]string `json:"headers"`
	// Auth follows the same shape as the swagger connector.
	Auth AuthConfig `json:"auth"`
	// TimeoutSec bounds each call; default 30s.
	TimeoutSec int `json:"timeout_seconds"`
	// MaxResponseBytes caps the body returned to the model; 0 = 8MiB.
	MaxResponseBytes int `json:"max_response_bytes"`
}

// AuthConfig describes authentication for outgoing requests.
type AuthConfig struct {
	Type      string `json:"type"` // none / bearer / apiKey / basic
	Bearer    string `json:"bearer"`
	APIKeyName  string `json:"api_key_name"`
	APIKeyValue string `json:"api_key_value"`
	APIKeyIn    string `json:"api_key_in"` // header / query
	BasicUser   string `json:"basic_user"`
	BasicPass   string `json:"basic_pass"`
}

// Connector implements the fetch connector.
type Connector struct {
	manifest connector.Manifest
	cfg      Config
	client   *http.Client
}

// New returns a Connector with a static manifest.
func New() *Connector { return &Connector{manifest: manifestSchema} }

// Manifest returns the static description.
func (c *Connector) Manifest() connector.Manifest { return c.manifest }

// Init validates config and prepares the http.Client.
func (c *Connector) Init(_ context.Context, raw json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("fetch: parse config: %w", err)
	}
	if cfg.BaseURL == "" {
		return errors.New("fetch: base_url is required")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 30
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 8 << 20
	}
	if cfg.Auth.APIKeyIn == "" {
		cfg.Auth.APIKeyIn = "header"
	}
	c.cfg = cfg
	c.client = &http.Client{Timeout: time.Duration(cfg.TimeoutSec) * time.Second}
	return nil
}

// HealthCheck verifies the base URL is reachable.
func (c *Connector) HealthCheck(ctx context.Context) error {
	if c.client == nil {
		return errors.New("fetch: not initialised")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.cfg.BaseURL+"/", nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// Shutdown is a no-op (http.Client has no global state).
func (c *Connector) Shutdown(_ context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────
// Tools
// ─────────────────────────────────────────────────────────────────────

func (c *Connector) Tools() []connector.ToolSpec {
	props := func(extra map[string]any) map[string]any {
		out := map[string]any{
			"path":   map[string]any{"type": "string", "description": "Path appended to base_url"},
			"query":  map[string]any{"type": "object", "description": "Query parameters (key→value)", "additionalProperties": map[string]any{"type": "string"}},
			"headers": map[string]any{"type": "object", "description": "Per-call headers", "additionalProperties": map[string]any{"type": "string"}},
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	mk := func(name, summary, method, bodyDesc string) connector.ToolSpec {
		extra := map[string]any{}
		if bodyDesc != "" {
			extra["body"] = map[string]any{"type": "string", "description": bodyDesc}
			extra["base64"] = map[string]any{"type": "boolean", "default": false, "description": "Treat body as base64-encoded bytes"}
		}
		return connector.ToolSpec{
			Name: name,
			Description: summary + " (" + method + ")",
			InputSchema: schema(props(extra)),
		}
	}
	return []connector.ToolSpec{
		mk("get", "Issue an HTTP request and return status + headers + body (base64 when binary).", "GET", ""),
		mk("post", "Issue an HTTP request and return status + headers + body (base64 when binary).", "POST", "Request body (UTF-8 by default; base64 if base64=true)"),
		mk("put", "Issue an HTTP request and return status + headers + body (base64 when binary).", "PUT", "Request body"),
		mk("delete", "Issue an HTTP request and return status + headers + body (base64 when binary).", "DELETE", ""),
		mk("head", "Issue a HEAD request. Useful for health probes.", "HEAD", ""),
	}
}

// InvokeTool dispatches a call.
func (c *Connector) InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if c.client == nil {
		return nil, errors.New("fetch: not initialised")
	}
	var a callArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("fetch: parse args: %w", err)
		}
	}
	method := httpMethodFor(name)
	if method == "" {
		return nil, fmt.Errorf("fetch: unknown tool %q", name)
	}

	full := c.cfg.BaseURL + "/" + strings.TrimLeft(a.Path, "/")
	req, err := http.NewRequestWithContext(ctx, method, full, bytes.NewReader(a.body()))
	if err != nil {
		return nil, err
	}
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range a.Headers {
		req.Header.Set(k, v)
	}
	for k, vs := range a.Query {
		for _, v := range vs {
			q := req.URL.Query()
			q.Add(k, v)
			req.URL.RawQuery = q.Encode()
		}
	}
	c.applyAuth(req)
	if a.body() != nil {
		if a.Base64 {
			req.Header.Set("Content-Type", "application/octet-stream")
		} else if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		}
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limit := int64(c.cfg.MaxResponseBytes) + 1
	lr := io.LimitReader(resp.Body, limit)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"status":  resp.StatusCode,
		"headers": flattenHeaders(resp.Header),
		"body":    string(data),
	}
	if len(data) >= int(limit) {
		out["truncated"] = true
	}
	return json.Marshal(out)
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

func httpMethodFor(name string) string {
	switch name {
	case "get":
		return http.MethodGet
	case "post":
		return http.MethodPost
	case "put":
		return http.MethodPut
	case "delete":
		return http.MethodDelete
	case "head":
		return http.MethodHead
	}
	return ""
}

func (c *Connector) applyAuth(req *http.Request) {
	a := c.cfg.Auth
	switch a.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+a.Bearer)
	case "apiKey":
		if a.APIKeyIn == "query" {
			q := req.URL.Query()
			q.Set(a.APIKeyName, a.APIKeyValue)
			req.URL.RawQuery = q.Encode()
		} else {
			req.Header.Set(a.APIKeyName, a.APIKeyValue)
		}
	case "basic":
		req.SetBasicAuth(a.BasicUser, a.BasicPass)
	}
}

// callArgs captures the JSON shape every fetch tool accepts.
type callArgs struct {
	Path    string            `json:"path"`
	Query   map[string][]string `json:"query"`
	Headers map[string]string  `json:"headers"`
	Body    string            `json:"body"`
	Base64  bool              `json:"base64"`
}

// body returns the request body as an io.Reader, decoding base64 if
// requested. Returns nil for read methods.
func (a callArgs) body() []byte {
	if a.Body == "" {
		return nil
	}
	if a.Base64 {
		raw, err := base64.StdEncoding.DecodeString(a.Body)
		if err != nil {
			return nil
		}
		return raw
	}
	return []byte(a.Body)
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[strings.ToLower(k)] = v[0]
		}
	}
	return out
}

func schema(props map[string]any) connector.JSONSchema {
	m, _ := json.Marshal(map[string]any{"type": "object", "properties": props})
	return m
}

// manifestSchema is registered at boot.
var manifestSchema = connector.Manifest{
	Name:        "fetch",
	Version:     "0.1.0",
	DisplayName: "HTTP (fetch)",
	Description: "Generic HTTP client — fallback for any REST API without an OpenAPI document.",
	Capabilities: []string{
		connector.CapabilityTools,
	},
	ConfigSchema: connector.JSONSchema(`{
  "type": "object",
  "required": ["base_url"],
  "properties": {
    "base_url":           {"type":"string",  "title":"Base URL"},
    "timeout_seconds":    {"type":"integer", "title":"Timeout (s)", "default":30, "minimum":1},
    "max_response_bytes": {"type":"integer", "title":"Max response bytes", "default":8388608},
    "headers":            {"type":"object",  "title":"Default headers", "additionalProperties":{"type":"string"}},
    "auth": {
      "type":"object",
      "properties": {
        "type":           {"type":"string","enum":["none","bearer","apiKey","basic"]},
        "bearer":         {"type":"string","format":"password"},
        "api_key_name":   {"type":"string"},
        "api_key_value":  {"type":"string","format":"password"},
        "api_key_in":     {"type":"string","enum":["header","query"],"default":"header"},
        "basic_user":     {"type":"string"},
        "basic_pass":     {"type":"string","format":"password"}
      }
    }
  }
}`),
}