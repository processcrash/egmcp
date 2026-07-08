// Package mysql exposes a MySQL database to MCP clients.
//
// The connector maps to pkg/sqlconn for policy enforcement (read-only
// mode, statement timeout, allow-list). It also exposes three
// schema-discovery tools (list_databases, list_tables,
// describe_table) that round-trip the connector's permissions so the
// model has a clear view of what it may query.
package mysql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/processcrash/egmcp/pkg/connector"
	"github.com/processcrash/egmcp/pkg/sqlconn"
)

// Config is the per-instance connector config (declared in manifestSchema).
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
}

// Connector implements the MySQL backend.
type Connector struct {
	manifest connector.Manifest
	conn     *sqlconn.Conn
	dbName   string
}

// New returns a Connector with a static manifest.
func New() *Connector { return &Connector{manifest: manifestSchema} }

// Manifest returns the static description.
func (c *Connector) Manifest() connector.Manifest { return c.manifest }

// Init validates the config and opens the connection.
func (c *Connector) Init(ctx context.Context, raw json.RawMessage) error {
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("mysql: parse config: %w", err)
	}
	if cfg.DSN == "" {
		return errors.New("mysql: dsn is required")
	}

	allow := map[string]struct{}{}
	for _, t := range cfg.AllowTables {
		allow[t] = struct{}{}
	}

	sc, err := sqlconn.Open(sqlconn.Config{
		DriverName:       "mysql",
		DSN:              cfg.DSN,
		ReadOnly:         cfg.ReadOnly,
		MaxRows:          cfg.MaxRows,
		StatementTimeout: secondsToDuration(cfg.StatementTimeout),
		AllowTables:      allow,
	})
	if err != nil {
		return fmt.Errorf("mysql: %w", err)
	}
	c.conn = sc
	// Capture the database name for schema-discover tools.
	if db, err := extractDBName(cfg.DSN); err == nil {
		c.dbName = db
	}
	return nil
}

// HealthCheck runs a cheap ping.
func (c *Connector) HealthCheck(ctx context.Context) error {
	if c.conn == nil {
		return errors.New("mysql: not initialised")
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

// Tools describes the connector's tools.
func (c *Connector) Tools() []connector.ToolSpec {
	return []connector.ToolSpec{
		{
			Name:        "sql_query",
			Description: "Run a SQL statement and return the rows as JSON.",
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
			Name:        "list_databases",
			Description: "List databases visible to the connection.",
			InputSchema: merge(map[string]any{"type": "object"}),
		},
		{
			Name:        "list_tables",
			Description: "List tables in the configured database (or all if none configured).",
			InputSchema: merge(map[string]any{"type": "object"}),
		},
		{
			Name:        "describe_table",
			Description: "Return column metadata for a table.",
			InputSchema: merge(map[string]any{
				"type":     "object",
				"required": []string{"table"},
				"properties": map[string]any{
					"table": map[string]any{"type": "string"},
				},
			}),
		},
	}
}

// InvokeTool dispatches a tool call.
func (c *Connector) InvokeTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	if c.conn == nil {
		return nil, errors.New("mysql: not initialised")
	}
	switch name {
	case "sql_query":
		var a struct {
			SQL  string        `json:"sql"`
			Args []any         `json:"args"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		res, err := c.conn.Query(ctx, a.SQL, a.Args...)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	case "list_databases":
		rows, err := c.conn.DB().QueryContext(ctx,
			`SELECT SCHEMA_NAME FROM information_schema.SCHEMATA
             WHERE SCHEMA_NAME NOT IN ('information_schema','mysql','performance_schema','sys')
             ORDER BY SCHEMA_NAME`)
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
		return json.Marshal(map[string]any{"databases": out})
	case "list_tables":
		db := c.dbName
		scope := "DATABASE()"
		if db != "" {
			scope = "'" + db + "'"
		}
		q := fmt.Sprintf(`SELECT TABLE_NAME FROM information_schema.TABLES
                          WHERE TABLE_SCHEMA = %s ORDER BY TABLE_NAME`, scope)
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
			Table string `json:"table"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		if a.Table == "" {
			return nil, errors.New("mysql: table is required")
		}
		res, err := c.conn.Query(ctx,
			`SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT, COLUMN_KEY
             FROM information_schema.COLUMNS
             WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
             ORDER BY ORDINAL_POSITION`,
			a.Table)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	default:
		return nil, fmt.Errorf("mysql: unknown tool %q", name)
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

func extractDBName(dsn string) (string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", err
	}
	return cfg.DBName, nil
}

func merge(m map[string]any) connector.JSONSchema {
	b, _ := json.Marshal(m)
	return b
}

// manifestSchema is registered with the platform at boot.
var manifestSchema = connector.Manifest{
	Name:        "mysql",
	Version:     "0.1.0",
	DisplayName: "MySQL",
	Description: "Read-only-by-default MySQL connector.",
	Capabilities: []string{
		connector.CapabilityTools,
		connector.CapabilityResources,
	},
	ConfigSchema: connector.JSONSchema(`{
  "type": "object",
  "required": ["dsn"],
  "properties": {
    "dsn": {
      "type": "string",
      "format": "password",
      "title": "DSN",
      "description": "MySQL DSN, e.g. user:pass@tcp(host:3306)/dbname?parseTime=true"
    },
    "read_only": {"type": "boolean", "title": "Read-only", "default": true},
    "max_rows": {"type": "integer", "title": "Max rows per query", "minimum": 1},
    "statement_timeout_seconds": {"type": "integer", "title": "Statement timeout (s)", "minimum": 0, "default": 30},
    "allow_tables": {"type": "array", "title": "Allowed tables", "items": {"type": "string"}}
  }
}`),
}