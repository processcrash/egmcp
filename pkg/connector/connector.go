// Package connector defines the contract between the egmcp platform
// and middleware-specific adapters (filesystem, MySQL, OSS, …).
//
// The package has two layers of stability:
//
//   - the SDK (Connector interface, JSON Schema types) is part of the
//     public API and is consumed by third-party plugin authors;
//   - the registry, dispatcher and HTTP transport live in internal/
//     and may change freely.
//
// Plugins and built-in connectors implement Connector and
// optionally ToolProvider / ToolInvoker, ResourceProvider /
// ResourceReader, PromptProvider / PromptRenderer. The platform
// reflects on these interfaces to assemble the per-instance MCP
// tool/resource/prompt list.
package connector

import (
	"context"
	"encoding/json"
)

// JSONSchema is a structural subset of JSON Schema Draft 2020-12.
// Front-ends (AntD Form renderer) consume this to generate UIs.
//
// We embed raw JSON rather than a fully typed model because real-world
// connector configs are very diverse — a strict typed model would
// become a maintenance burden. The renderer only needs a handful of
// well-known keywords.
type JSONSchema = json.RawMessage

// Manifest self-describes a Connector so the platform can generate
// configuration UIs, validate user input and register MCP tools.
type Manifest struct {
	// Name uniquely identifies the connector type, e.g. "mysql".
	// It must match `^[a-z][a-z0-9_-]{1,31}$`.
	Name string `json:"name"`
	// Version follows semver, e.g. "0.1.0".
	Version string `json:"version"`
	// DisplayName is the user-facing label (English).
	DisplayName string `json:"displayName"`
	// Description is shown next to the connector in the UI.
	Description string `json:"description"`
	// ConfigSchema is the JSON Schema describing the connector's
	// per-instance config object.
	ConfigSchema JSONSchema `json:"configSchema"`
	// Capabilities declares which protocol surfaces the connector
	// implements. Anything not listed is skipped.
	Capabilities []string `json:"capabilities"`
}

// Capability constants.
const (
	CapabilityTools     = "tools"
	CapabilityResources = "resources"
	CapabilityPrompts   = "prompts"
)

// Connector is the long-lived handle between the platform and a
// middleware. It is instantiated once per instance per connector
// (i.e. an instance with two MySQL connectors gets two Connector
// instances, one per DSN).
type Connector interface {
	// Manifest returns the static description. Implementations should
	// return a stable value (it is used as part of the connector key).
	Manifest() Manifest

	// Init prepares the connector with a config blob. It is called
	// once at instance start, and again whenever the instance config
	// file changes (so it must be safe to call repeatedly).
	Init(ctx context.Context, cfg json.RawMessage) error

	// HealthCheck should perform a lightweight, low-latency probe of
	// the underlying middleware. It is called both on startup and on
	// demand from the admin console.
	HealthCheck(ctx context.Context) error

	// Shutdown releases any resources held by the connector.
	Shutdown(ctx context.Context) error
}

// ToolSpec describes a single MCP tool a Connector exposes.
type ToolSpec struct {
	// Name is short and snake_case. At runtime the fully qualified
	// name becomes `{connector-name}:{spec-name}`.
	Name        string          `json:"name"`
	Description string          `json:"description"`
	// InputSchema follows JSON Schema Draft 2020-12.
	InputSchema JSONSchema      `json:"inputSchema"`
	// Annotations mirror the MCP tool annotations.
	Annotations ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations provides MCP hint metadata.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint bool   `json:"destructiveHint,omitempty"`
	IdempotentHint  bool   `json:"idempotentHint,omitempty"`
	OpenWorldHint   bool   `json:"openWorldHint,omitempty"`
}

// ResourceSpec describes a resource exposed by the Connector.
type ResourceSpec struct {
	Name        string `json:"name"`
	URI         string `json:"uri"` // template form, e.g. "schema://{table}"
	Description string `json:"description"`
	MIMEType    string `json:"mimeType"`
}

// ResourceContents is the body returned by ReadResource.
type ResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
	Blob     []byte `json:"blob,omitempty"`
}

// PromptSpec describes a prompt template.
type PromptSpec struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Arguments   []PromptArgument `json:"arguments"`
}

// PromptArgument is one parameter of a prompt template.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// PromptMessage is a single message in a rendered prompt.
type PromptMessage struct {
	Role    string         `json:"role"` // "user" | "assistant" | "system"
	Content map[string]any `json:"content"`
}

// Capability-mix-in interfaces. Connectors implement only the ones
// they need; the platform's instance registry discovers capabilities
// via the Manifest and via type assertions on the Connector value.

type ToolProvider interface {
	Tools() []ToolSpec
}

type ToolInvoker interface {
	InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}

type ResourceProvider interface {
	Resources() []ResourceSpec
}

type ResourceReader interface {
	ReadResource(ctx context.Context, uri string) (ResourceContents, error)
}

type PromptProvider interface {
	Prompts() []PromptSpec
}

type PromptRenderer interface {
	RenderPrompt(ctx context.Context, name string, args map[string]string) (PromptMessage, error)
}
