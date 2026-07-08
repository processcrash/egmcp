// Package postgres exposes a PostgreSQL database to MCP clients. See
// the mysql connector for the contract; both share pkg/sqlconn for
// policy enforcement.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx stdlib driver registration

	"github.com/processcrash/egmcp/pkg/connector"
	"github.com/processcrash/egmcp/pkg/sqlconn"
)

// Config is the per-instance connector config.
type Config struct {
	DSN string `json:"dsn"`
	// ReadOnly refuses any non-SELECT statement.
	ReadOnly bool `json:"read_only"`
	// MaxRows caps rows returned per query.
	MaxRows int `json:"max_rows"`
	// StatementTimeout in seconds (0 = no limit).
	StatementTimeout int `json:"statement_timeout_seconds"`
	// AllowTables is a list of table names the connector is allowed
	// to reference (case-insensitive). Empty = no restriction.
	AllowTables []string `json:"allow_tables"`
	// Schema is the search_path. Empty = default.
	Schema string `json:"schema"`
}

// Connector implements the Postgres backend.
type Connector struct {
	manifest connector.Manifest
	conn     *sqlconn.Conn
	schema   string
}

// New returns a Connector with a static manifest.
func New() *Connector { return &Connector{manifest: manifestSchema} }

// Manifest returns the static description.
func (c *Connector) Manifest() connector.Manifest { return c.manifest }

// Init validates the config and opens the connection.
func (c *Connector) Init(ctx context.Context, raw json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("postgres: parse config: %w", err)
	}
	if cfg.DSN == "" {
		return errors.New("postgres: dsn is required")
	}
	allow := map[string]struct{}{}
	for _, t := range cfg.AllowTables {
		allow[t] = struct{}{}
	}
	sc, err := sqlconn.Open(sqlconn.Config{
		DriverName:       "pgx",
		DSN:              cfg.DSN,
		ReadOnly:         cfg.ReadOnly,
		MaxRows:          cfg.MaxRows,
		StatementTimeout: secondsToDuration(cfg.StatementTimeout),
		AllowTables:      allow,
	})
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	c.conn = sc
	c.schema = cfg.Schema
	return nil
}

// HealthCheck runs a cheap ping.
func (c *Connector) HealthCheck(ctx context.Context) error {
	if c.conn == nil {
		return errors.New("postgres: not initialised")
	}
	return c.conn.DB().PingContext(ctx)
}

// Shutdown closes the connection pool.
func (c *Connector) Shutdown(_ context.Context) error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// ─────────────────────────────────────────────────────────────────────
// Tool surface
// ─────────────────────────────────────────────────────────────────────

func (c *Connector) Tools() []connector.ToolSpec {
	return []connector.ToolSpec{
		{
			Name:        "sql_query",
			Description: "Run a SQL statement and return rows as JSON.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"sql"},
				"properties": map[string]any{
					"sql":  map[string]any{"type": "string"},
					"args": map[string]any{"type": "array", "items": map[string]any{}},
				},
			}),
		},
		{
			Name:        "list_schemas",
			Description: "List user schemas visible to the connection.",
			InputSchema: merge(map[string]any{"type": "object"}),
		},
		{
			Name:        "list_tables",
			Description: "List tables in the configured schema (or all if none configured).",
			InputSchema: merge(map[string]any{"type": "object"}),
		},
		{
			Name:        "describe_table",
			Description: "Return column metadata for a table.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"table"},
				"properties": map[string]any{
					"table":  map[string]any{"type": "string"},
					"schema": map[string]any{"type": "string"},
				},
			}),
		},
	}
}

// InvokeTool dispatches a tool call.
func (c *Connector) InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if c.conn == nil {
		return nil, errors.New("postgres: not initialised")
	}
	switch name {
	case "sql_query":
		var a struct {
			SQL  string `json:"sql"`
			Args []any  `json:"args"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		res, err := c.conn.Query(ctx, a.SQL, a.Args...)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	case "list_schemas":
		rows, err := c.conn.DB().QueryContext(ctx,
			`SELECT schema_name FROM information_schema.schemata
             WHERE schema_name NOT IN ('pg_catalog','information_schema','pg_toast')
             ORDER BY schema_name`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return json.Marshal(map[string]any{"schemas": out})
	case "list_tables":
		schema := c.schema
		if schema == "" {
			schema = "current_schema()"
		}
		q := fmt.Sprintf(`SELECT table_name FROM information_schema.tables
                          WHERE table_schema = %s ORDER BY table_name`, schema)
		rows, err := c.conn.DB().QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return json.Marshal(map[string]any{"tables": out})
	case "describe_table":
		var a struct {
			Table  string `json:"table"`
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if a.Table == "" {
			return nil, errors.New("postgres: table is required")
		}
		schema := a.Schema
		if schema == "" {
			schema = c.schema
		}
		if schema == "" {
			schema = "current_schema()"
		} else {
			// Validate the schema string to avoid injection into
			// the information_schema query.
			if !schemaIdentRE.MatchString(schema) {
				return nil, fmt.Errorf("postgres: invalid schema %q", schema)
			}
		}
		q := fmt.Sprintf(`SELECT column_name, data_type, is_nullable, column_default
                          FROM information_schema.columns
                          WHERE table_schema = %s AND table_name = $1
                          ORDER BY ordinal_position`, schema)
		res, err := c.conn.Query(ctx, q, a.Table)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	default:
		return nil, fmt.Errorf("postgres: unknown tool %q", name)
	}
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

func secondsToDuration(s int) time.Duration {
	if s <= 0 {
		return 0
	}
	return time.Duration(s) * time.Second
}

var schemaIdentRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func merge(m map[string]any) connector.JSONSchema {
	b, _ := json.Marshal(m)
	return b
}

// silent import to ensure pgx's stdlib driver is registered.
var _ = pgxpool.New

// manifestSchema is registered at boot.
var manifestSchema = connector.Manifest{
	Name:        "postgres",
	Version:     "0.1.0",
	DisplayName: "PostgreSQL",
	Description: "Read-only-by-default PostgreSQL connector.",
	Capabilities: []string{
		connector.CapabilityTools,
	},
	ConfigSchema: connector.JSONSchema(`{
  "type": "object",
  "required": ["dsn"],
  "properties": {
    "dsn": {
      "type": "string",
      "format": "password",
      "title": "DSN",
      "description": "PostgreSQL DSN, e.g. postgres://user:pass@host:5432/dbname"
    },
    "read_only": {"type": "boolean", "title": "Read-only", "default": true},
    "max_rows": {"type": "integer", "title": "Max rows per query", "minimum": 1},
    "statement_timeout_seconds": {"type": "integer", "title": "Statement timeout (s)", "minimum": 0, "default": 30},
    "allow_tables": {"type": "array", "title": "Allowed tables", "items": {"type": "string"}},
    "schema": {"type": "string", "title": "Default schema", "description": "Optional, e.g. 'public'"}
  }
}`),
}