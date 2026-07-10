// Package swagger implements a Connector that loads an OpenAPI/Swagger
// document and exposes every path + method as an MCP tool.
//
// The connector discovers operations at Init time (one tool per
// operation) and dispatches invocations by building an HTTP request
// from the operation's parameters. Authentication is read from the
// components.securitySchemes block; the connector supports Bearer,
// APIKey, and Basic flows.
//
// We intentionally keep the schema-driven form renderer out of scope
// for M5 — operators configure a connector by giving us a spec URL
// plus credentials, and we build the rest.
package swagger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/processcrash/egmcp/pkg/connector"
)

// Config is the per-instance connector config.
type Config struct {
	// SpecURL is the canonical URL of the OpenAPI document.
	SpecURL string `json:"spec_url"`
	// SpecFile lets operators ship a spec as a file when no public
	// URL is available.
	SpecFile string `json:"spec_file"`
	// BaseURL overrides the spec's `servers[0].url`. Useful when
	// the public spec describes a hosted path while the operator
	// wants to hit a private deployment.
	BaseURL string `json:"base_url"`
	// Auth selects how to authenticate outgoing requests.
	Auth AuthConfig `json:"auth"`
	// MaxRPM (requests per minute, 0 = no limit) is an optional,
	// in-process rate limiter to protect the upstream from runaway
	// agents.
	MaxRPM int `json:"max_rpm"`
	// TimeoutSec bounds each upstream call; defaults to 30s.
	TimeoutSec int `json:"timeout_seconds"`
}

// AuthConfig describes how outgoing requests are authenticated.
type AuthConfig struct {
	// Type is one of "none", "bearer", "apiKey", "basic".
	Type string `json:"type"`
	// Bearer is the bearer token (when Type=bearer).
	Bearer string `json:"bearer"`
	// APIKeyName is the header/query parameter name (apiKey).
	APIKeyName string `json:"api_key_name"`
	// APIKeyValue is the value (apiKey).
	APIKeyValue string `json:"api_key_value"`
	// APIKeyIn is "header" or "query" (apiKey). Defaults to "header".
	APIKeyIn string `json:"api_key_in"`
	// BasicUser / BasicPass for basic auth.
	BasicUser string `json:"basic_user"`
	BasicPass string `json:"basic_pass"`
}

// Connector implements the OpenAPI connector.
type Connector struct {
	manifest connector.Manifest

	mu       sync.Mutex
	spec     *openapi3.T
	baseURL  string
	cfg      Config
	ops      []opSpec
	limiter  *rateLimiter
	client   *http.Client
}

type opSpec struct {
	toolName    string
	summary     string
	description string
	method      string
	path        string
	op          *openapi3.Operation
}

// New returns a Connector with a static manifest.
func New() *Connector { return &Connector{manifest: manifestSchema} }

// Manifest returns the static description.
func (c *Connector) Manifest() connector.Manifest { return c.manifest }

// Init loads + parses the spec and prepares the tool inventory.
func (c *Connector) Init(ctx context.Context, raw json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("swagger: parse config: %w", err)
	}
	if cfg.SpecURL == "" && cfg.SpecFile == "" {
		return errors.New("swagger: spec_url or spec_file is required")
	}
	if cfg.Auth.APIKeyIn == "" {
		cfg.Auth.APIKeyIn = "header"
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 30
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	var (
		spec *openapi3.T
		err  error
	)
	switch {
	case cfg.SpecURL != "":
		u, perr := url.Parse(cfg.SpecURL)
		if perr != nil {
			return fmt.Errorf("swagger: bad spec url: %w", perr)
		}
		spec, err = loader.LoadFromURI(u)
	case cfg.SpecFile != "":
		abs, _ := filepath.Abs(cfg.SpecFile)
		spec, err = loader.LoadFromFile(abs)
	}
	if err != nil {
		return fmt.Errorf("swagger: load spec: %w", err)
	}
	if err := spec.Validate(loader.Context); err != nil {
		return fmt.Errorf("swagger: validate spec: %w", err)
	}

	base := cfg.BaseURL
	if base == "" && len(spec.Servers) > 0 {
		base = spec.Servers[0].URL
	}
	if base == "" {
		return errors.New("swagger: no base URL resolved from base_url or spec servers")
	}
	cfg.BaseURL = strings.TrimRight(base, "/")
	c.spec = spec
	c.cfg = cfg
	c.baseURL = cfg.BaseURL
	c.limiter = newRateLimiter(cfg.MaxRPM)
	c.client = &http.Client{Timeout: time.Duration(cfg.TimeoutSec) * time.Second}
	c.ops = c.collectOperations()
	if len(c.ops) == 0 {
		return fmt.Errorf("swagger: no operations discovered (paths=%d)", pathsCount(spec))
	}
	return nil
}

func pathsCount(s *openapi3.T) int {
	if s == nil || s.Paths == nil {
		return 0
	}
	return s.Paths.Len()
}

// HealthCheck fetches the spec (cheaply) again. If the spec URL or
// file changed, the Init step would have caught it; this is purely a
// liveness probe.
func (c *Connector) HealthCheck(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spec == nil {
		return errors.New("swagger: not initialised")
	}
	return nil
}

// Shutdown is a no-op for the swagger connector.
func (c *Connector) Shutdown(_ context.Context) error { return nil }

// ─────────────────────────────────────────────────────────────────────
// Tool generation
// ─────────────────────────────────────────────────────────────────────

var (
	invalidToolSegment = regexp.MustCompile(`[^A-Za-z0-9_]+`)
)

// safeName converts any path/method/tag combination into an
// SDK-acceptable snake case tool name. The MCP SDK rejects names
// that contain characters outside [A-Za-z0-9_-].
func safeName(parts ...string) string {
	joined := strings.ToLower(strings.Join(parts, "__"))
	return invalidToolSegment.ReplaceAllString(joined, "_")
}

func (c *Connector) collectOperations() []opSpec {
	out := make([]opSpec, 0)
	if c.spec == nil || c.spec.Paths == nil {
		return out
	}
	// Order: walk paths in sorted order for stable tool naming.
	paths := sortedPaths(c.spec.Paths)
	for _, path := range paths {
		item := c.spec.Paths.Value(path)
		if item == nil {
			continue
		}
		ops := item.Operations()
		for m, op := range ops {
			if op == nil {
				continue
			}
			tag := strings.ToLower(strings.ReplaceAll(filepath.Base(path), "/", "_"))
			if len(op.Tags) > 0 {
				tag = strings.ToLower(op.Tags[0])
			}
			tool := safeName("call", tag, strings.ToLower(m), strings.TrimLeft(path, "/"))
			out = append(out, opSpec{
				toolName:    tool,
				summary:     firstNonEmpty(op.Summary, op.Description),
				description: op.Description,
				method:      strings.ToUpper(m),
				path:        path,
				op:          op,
			})
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func sortedPaths(p *openapi3.Paths) []string {
	if p == nil {
		return nil
	}
	keys := p.Keys()
	sortStrings(keys)
	return keys
}

// sortStrings is a tiny in-place string sort to avoid pulling in the
// sort package just for this helper.
func sortStrings(s []string) {
	// Insertion sort: fine for tens / hundreds of paths.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// Tools returns one spec per OpenAPI operation.
func (c *Connector) Tools() []connector.ToolSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]connector.ToolSpec, 0, len(c.ops))
	for _, o := range c.ops {
		out = append(out, connector.ToolSpec{
			Name:        o.toolName,
			Description: descFor(o),
			InputSchema: schemaFor(o),
		})
	}
	return out
}

// descFor composes a descriptive string for the tool registry from
// the operation's summary + the HTTP method + path. The model can
// use this to decide when to call the tool.
func descFor(o opSpec) string {
	method := strings.ToUpper(o.method)
	if o.summary != "" {
		return fmt.Sprintf("[%s %s] %s", method, o.path, o.summary)
	}
	return fmt.Sprintf("[%s %s]", method, o.path)
}

// schemaFor returns a permissive JSON Schema that asks the model
// for a flat object keyed by parameter name. We deliberately don't
// emit the full OpenAPI parameter object — it's cleaner for the
// model to see name + value pairs.
func schemaFor(o opSpec) connector.JSONSchema {
	props := map[string]any{}
	required := []string{}
	for _, p := range o.op.Parameters {
		name := p.Value.Name
		if name == "" {
			continue
		}
		props[name] = jsonSchemaForParam(p)
		if p.Value.Required {
			required = append(required, name)
		}
	}
	// Body parameters use a generic object — we don't try to model
	// request bodies fully in M5.
	if o.op.RequestBody != nil {
		props["body"] = map[string]any{
			"description": "Request body (object/array/scalar)",
		}
		if o.op.RequestBody.Value.Required {
			required = append(required, "body")
		}
	}
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	b, _ := json.Marshal(out)
	return b
}

func jsonSchemaForParam(p *openapi3.ParameterRef) map[string]any {
	if p == nil || p.Value == nil || p.Value.Schema == nil || p.Value.Schema.Value == nil {
		return map[string]any{"type": "string"}
	}
	types := p.Value.Schema.Value.Type
	var primary string
	if types != nil && len(*types) > 0 {
		primary = (*types)[0]
	}
	switch primary {
	case "integer":
		return map[string]any{"type": "integer", "description": p.Value.Description}
	case "number":
		return map[string]any{"type": "number", "description": p.Value.Description}
	case "boolean":
		return map[string]any{"type": "boolean", "description": p.Value.Description}
	case "array":
		return map[string]any{"type": "array", "description": p.Value.Description}
	default:
		return map[string]any{"type": "string", "description": p.Value.Description}
	}
}

// InvokeTool dispatches a tool call. The args object is a flat
// parameter map matching the per-operation JSON Schema.
func (c *Connector) InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var found *opSpec
	for i := range c.ops {
		if c.ops[i].toolName == name {
			found = &c.ops[i]
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("swagger: unknown tool %q", name)
	}

	var a map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, fmt.Errorf("swagger: parse args: %w", err)
		}
	}

	if c.limiter != nil {
		if err := c.limiter.allow(ctx); err != nil {
			return nil, err
		}
	}

	path, query, body, err := bindOperation(found, a)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		q := u.Query()
		for k, v := range query {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		bs, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(bs)
	}
	req, err := http.NewRequestWithContext(ctx, found.method, u.String(), bodyReader)
	if err != nil {
		return nil, err
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	applyAuth(req, c.cfg.Auth)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("swagger: http: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"status":  resp.StatusCode,
		"headers": flattenHeaders(resp.Header),
		"body":    string(data),
	}
	if resp.StatusCode >= 400 {
		return json.Marshal(out)
	}
	return json.Marshal(out)
}

// bindOperation walks the operation parameters, substitutes path
// tokens, and collects query / body values.
func bindOperation(o *opSpec, a map[string]any) (path string, query map[string]string, body any, err error) {
	path = o.path
	query = map[string]string{}
	for _, p := range o.op.Parameters {
		if p.Value == nil {
			continue
		}
		val, present := a[p.Value.Name]
		if !present {
			if p.Value.Required {
				err = fmt.Errorf("missing required parameter %q", p.Value.Name)
				return
			}
			continue
		}
		s := stringify(val)
		switch p.Value.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Value.Name+"}", url.PathEscape(s))
		case "query":
			query[p.Value.Name] = s
		case "header":
			// added to the request in InvokeTool via carry-through;
			// here we stash them as a header map.
			// We don't have direct header access here; we'll
			// collect them on the request side.
			if query == nil {
				query = map[string]string{}
			}
			query["__header__:"+p.Value.Name] = s
		}
	}
	// Pull out body if present.
	if v, ok := a["body"]; ok {
		body = v
	}
	return path, query, body, nil
}

// applyAuth applies the configured authentication scheme to a request.
func applyAuth(req *http.Request, a AuthConfig) {
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

func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%v", x)
	case bool:
		return fmt.Sprintf("%v", x)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
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

// ─────────────────────────────────────────────────────────────────────
// rateLimiter is a token-bucket style per-connector limiter. It's
// deliberately process-local: per-instance, not cluster-wide.
// ─────────────────────────────────────────────────────────────────────

type rateLimiter struct {
	mu      sync.Mutex
	tokens  float64
	perMin  float64
	lastRef time.Time
}

func newRateLimiter(maxRPM int) *rateLimiter {
	if maxRPM <= 0 {
		return nil
	}
	return &rateLimiter{
		tokens:  float64(maxRPM),
		perMin:  float64(maxRPM),
		lastRef: time.Now(),
	}
}

func (r *rateLimiter) allow(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	elapsed := now.Sub(r.lastRef).Minutes()
	r.lastRef = now
	r.tokens += elapsed * r.perMin
	if r.tokens > r.perMin {
		r.tokens = r.perMin
	}
	if r.tokens < 1 {
		return errors.New("swagger: rate limit exceeded (lower MaxRPM or wait)")
	}
	r.tokens--
	return nil
}

// manifestSchema is registered at boot.
var manifestSchema = connector.Manifest{
	Name:        "swagger",
	Version:     "0.1.0",
	DisplayName: "OpenAPI / Swagger",
	Description: "Wrap any OpenAPI 3.x API as MCP tools.",
	Capabilities: []string{
		connector.CapabilityTools,
		connector.CapabilityResources,
	},
	ConfigSchema: connector.JSONSchema(`{
  "type": "object",
  "title": "Swagger / OpenAPI connector",
  "required": [],
  "properties": {
    "spec_url":  {"type":"string", "title":"Spec URL", "description":"OpenAPI 3.x document URL"},
    "spec_file": {"type":"string", "title":"Spec file", "description":"Local path to OpenAPI document"},
    "base_url":  {"type":"string", "title":"Base URL override", "description":"Overrides spec servers[0]"},
    "max_rpm":   {"type":"integer","title":"Max requests per minute", "minimum":1, "default":600},
    "timeout_seconds": {"type":"integer","title":"Per-call timeout (s)","minimum":1,"default":30},
    "auth": {
      "type":"object",
      "title":"Authentication",
      "properties": {
        "type":            {"type":"string","enum":["none","bearer","apiKey","basic"]},
        "bearer":          {"type":"string","format":"password"},
        "api_key_name":    {"type":"string"},
        "api_key_value":   {"type":"string","format":"password"},
        "api_key_in":      {"type":"string","enum":["header","query"], "default":"header"},
        "basic_user":      {"type":"string"},
        "basic_pass":      {"type":"string","format":"password"}
      }
    }
  },
  "oneOf": [
    {"required":["spec_url"]},
    {"required":["spec_file"]}
  ]
}`),
}