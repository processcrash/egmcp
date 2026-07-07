// Package mcp wires egmcp instances to the Model Context Protocol.
//
// Each MCP instance (one slug) becomes one *mcp.Server that aggregates
// the tools/resources/prompts of every Connector configured for that
// instance. The package exposes:
//
//   - ServerFor(router, slug) — build (or fetch) the MCP server for
//     a slug.
//   - MountHTTP — register /mcp/{slug} (Streamable HTTP) and the
//     legacy SSE endpoints on a Gin engine.
//   - MountOpenAPI — serve a synthesised OpenAPI 3.1 description of
//     the current instance's tools.
//
// The implementation is intentionally thin: the heavy lifting
// (protocol compliance, session management, JSON-RPC framing) is
// delegated to github.com/modelcontextprotocol/go-sdk.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/processcrash/egmcp/internal/core"
	"github.com/processcrash/egmcp/internal/log"
	"github.com/processcrash/egmcp/internal/store"
	"github.com/processcrash/egmcp/pkg/connector"
)

// ServerSet owns the mcp.Server instances keyed by slug. The map is
// rebuilt on instance change so it always reflects the latest set of
// configured connectors.
type ServerSet struct {
	mu      sync.RWMutex
	servers map[string]*mcpsdk.Server
	router  *core.Router
	logger  *zap.Logger
}

// NewServerSet constructs an empty ServerSet.
func NewServerSet(router *core.Router, logger *zap.Logger) *ServerSet {
	return &ServerSet{
		servers: make(map[string]*mcpsdk.Server),
		router:  router,
		logger:  logger,
	}
}

// For returns (or builds) the MCP server for the given slug. Returns
// nil and a non-nil error if the slug is unknown.
func (s *ServerSet) For(slug string) (*mcpsdk.Server, error) {
	s.mu.RLock()
	srv, ok := s.servers[slug]
	s.mu.RUnlock()
	if ok {
		return srv, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if srv, ok = s.servers[slug]; ok {
		return srv, nil
	}
	live, err := s.router.GetInstance(slug)
	if err != nil {
		return nil, err
	}
	srv = mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "egmcp-" + slug,
		Version: "0.1.0",
	}, &mcpsdk.ServerOptions{
		Instructions: fmt.Sprintf("egmcp-managed MCP server. Connectors: %d", len(live.Connectors)),
	})
	if err := s.registerConnectors(srv, slug, live.Connectors); err != nil {
		return nil, err
	}
	s.servers[slug] = srv
	s.logger.Info("mcp server built",
		log.String("slug", slug),
		log.Int("connectors", len(live.Connectors)),
	)
	return srv, nil
}

// Invalidate clears any cached server for slug. Called when the
// underlying instance changes.
func (s *ServerSet) Invalidate(slug string) {
	s.mu.Lock()
	delete(s.servers, slug)
	s.mu.Unlock()
}

// InvalidateAll drops every cached server. Used at startup or on
// drastic config changes.
func (s *ServerSet) InvalidateAll() {
	s.mu.Lock()
	s.servers = make(map[string]*mcpsdk.Server)
	s.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────
// Tool/Resource/Prompt registration
// ─────────────────────────────────────────────────────────────────────

func (s *ServerSet) registerConnectors(srv *mcpsdk.Server, slug string, refs []store.ConnRef) error {
	for _, ref := range refs {
		c, err := s.router.GetConnector(slug, ref.Name)
		if err != nil {
			s.logger.Warn("connector missing in live registry; skipping",
				log.String("slug", slug),
				log.String("connector", ref.Name),
				log.Err(err),
			)
			continue
		}
		prefix := c.Manifest().Name

		// Tools.
		if tp, ok := c.(connector.ToolProvider); ok {
			if ti, ok := c.(connector.ToolInvoker); ok {
				for _, spec := range tp.Tools() {
					spec := spec // capture
					inv := invokerAdapter{name: ref.Name, invoker: ti, logger: s.logger}
					inputSchema := spec.InputSchema
					if len(inputSchema) == 0 {
						// The MCP SDK requires a typed object
						// schema; fall back to an empty one when the
						// connector author left it unspecified.
						inputSchema = json.RawMessage(`{"type":"object"}`)
					}
					srv.AddTool(&mcpsdk.Tool{
						Name:        fmt.Sprintf("%s__%s", prefix, spec.Name),
						Description: spec.Description,
						InputSchema: inputSchema,
						Annotations: &mcpsdk.ToolAnnotations{
							Title:           spec.Annotations.Title,
							ReadOnlyHint:    spec.Annotations.ReadOnlyHint,
							DestructiveHint: boolPtr(spec.Annotations.DestructiveHint),
							IdempotentHint:  spec.Annotations.IdempotentHint,
							OpenWorldHint:   boolPtr(spec.Annotations.OpenWorldHint),
						},
					}, inv.handle)
				}
			}
		}

		// Resources.
		if rp, ok := c.(connector.ResourceProvider); ok {
			if rr, ok := c.(connector.ResourceReader); ok {
				for _, spec := range rp.Resources() {
					spec := spec
					r := resourceReaderAdapter{reader: rr, spec: spec, logger: s.logger}
					srv.AddResource(&mcpsdk.Resource{
						Name:     fmt.Sprintf("%s__%s", prefix, spec.Name),
						MIMEType: spec.MIMEType,
						URI:      spec.URI,
					}, r.handle)
				}
			}
		}
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

// invokerAdapter adapts a connector.ToolInvoker to the SDK's tool
// handler shape. It unmarshals the SDK's raw arguments and produces a
// CallToolResult with the connector's JSON response as a text content
// block.
type invokerAdapter struct {
	slug    string
	name    string
	invoker connector.ToolInvoker
	logger  *zap.Logger
}

func (a invokerAdapter) handle(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	toolName := stripPrefix(req.Params.Name)
	args := req.Params.Arguments
	started := time.Now()
	out, err := a.invoker.InvokeTool(ctx, toolName, args)
	latency := time.Since(started)
	a.logger.Info("tool invoked",
		log.String("connector", a.name),
		log.String("tool", toolName),
		log.Any("latency_ms", latency.Milliseconds()),
		log.Err(err),
	)
	if err != nil {
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil
	}
	return &mcpsdk.CallToolResult{
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(out)}},
	}, nil
}

type resourceReaderAdapter struct {
	reader connector.ResourceReader
	spec   connector.ResourceSpec
	logger *zap.Logger
}

func (a resourceReaderAdapter) handle(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	contents, err := a.reader.ReadResource(ctx, req.Params.URI)
	if err != nil {
		return nil, err
	}
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{
			URI:      contents.URI,
			MIMEType: contents.MIMEType,
			Text:     contents.Text,
		}},
	}, nil
}

func stripPrefix(name string) string {
	for i := 0; i < len(name); i++ {
		if name[i] == '_' && i+1 < len(name) && name[i+1] == '_' {
			return name[i+2:]
		}
	}
	return name
}
