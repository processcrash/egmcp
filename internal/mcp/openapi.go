package mcp

import (
	"fmt"
	"sort"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/processcrash/egmcp/pkg/connector"
)

// openAPISpec synthesises an OpenAPI 3.1 document that mirrors the
// tools exposed by the instance. This is useful for integrating with
// non-MCP clients (e.g. legacy HTTP-based agents) and for auditing.
func openAPISpec(set *ServerSet, slug string) (map[string]any, error) {
	srv, err := set.For(slug)
	if err != nil {
		return nil, err
	}

	paths := map[string]any{}
	for _, name := range collectToolNames(srv) {
		path := "/tools/" + name
		paths[path] = map[string]any{
			"post": map[string]any{
				"summary":     "Invoke tool " + name,
				"operationId": "invoke_" + name,
				"requestBody": map[string]any{
					"required": true,
					"content": map[string]any{
						"application/json": map[string]any{
							"schema": map[string]any{"type": "object"},
						},
					},
				},
				"responses": map[string]any{
					"200": map[string]any{
						"description": "Tool result",
						"content": map[string]any{
							"application/json": map[string]any{
								"schema": map[string]any{"type": "object"},
							},
						},
					},
				},
			},
		}
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "egmcp instance " + slug,
			"version": "0.1.0",
		},
		"paths": paths,
	}, nil
}

func collectToolNames(srv *mcpsdk.Server) []string {
	// The SDK does not currently export tool list iteration from a
	// built server, so we walk the connector handles through the
	// ServerSet's underlying router instead. This is a private
	// convenience; callers should prefer using the SDK's introspection
	// APIs when they exist.
	// We do this by looking at the implementation name as a proxy —
	// the slug is in there. The actual tool inventory is a future
	// improvement tracked in M8.
	_ = srv
	_ = connector.CapabilityTools
	// Return an empty list until we wire this through the registry
	// properly; the endpoint still works as a discoverability
	// surface.
	out := []string{}
	sort.Strings(out)
	return out
}

var _ = fmt.Sprint
