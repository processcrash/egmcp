// Package main is a minimal egmcp third-party plugin. Build it with
//
//	go build -buildmode=plugin -o hello.so .
//
// and drop `hello.so` into the platform's data/plugins directory.
// On Windows, use `-o hello.dll` instead.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/processcrash/egmcp/pkg/connector"
)

// Hello is the connector exported to the platform. The variable name
// must be exactly `Connector` (the platform also looks for
// `NewConnector` as a fallback for older authors).
var Connector connector.Connector = &hello{}

type hello struct {
	prefix string
}

// Manifest returns the static description that the platform shows
// in the create-wizard.
func (h *hello) Manifest() connector.Manifest {
	return connector.Manifest{
		Name:         "hello",
		Version:      "0.1.0",
		DisplayName:  "Hello (sample plugin)",
		Description:  "Returns a greeting. Bundled with the repo to demonstrate plugin loading.",
		Capabilities: []string{connector.CapabilityTools},
		ConfigSchema: connector.JSONSchema(`{
  "type": "object",
  "properties": {
    "prefix": {
      "type": "string",
      "title": "Prefix",
      "description": "Optional greeting prefix; defaults to 'Hello'."
    }
  }
}`),
	}
}

// Init validates the supplied config blob.
func (h *hello) Init(_ context.Context, raw json.RawMessage) error {
	var cfg struct {
		Prefix string `json:"prefix"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return err
		}
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "Hello"
	}
	h.prefix = cfg.Prefix
	return nil
}

func (h *hello) HealthCheck(_ context.Context) error { return nil }
func (h *hello) Shutdown(_ context.Context) error    { return nil }

// Tools returns the connector's MCP tools.
func (h *hello) Tools() []connector.ToolSpec {
	return []connector.ToolSpec{
		{
			Name:        "greet",
			Description: "Greet someone by name.",
			InputSchema: connector.JSONSchema(`{
  "type": "object",
  "required": ["name"],
  "properties": {"name": {"type": "string"}}
}`),
		},
	}
}

// InvokeTool dispatches a tool call.
func (h *hello) InvokeTool(_ context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	switch name {
	case "greet":
		var a struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if a.Name == "" {
			return nil, fmt.Errorf("name is required")
		}
		return json.Marshal(map[string]string{
			"greeting": fmt.Sprintf("%s, %s!", h.prefix, a.Name),
		})
	default:
		return nil, fmt.Errorf("hello: unknown tool %q", name)
	}
}