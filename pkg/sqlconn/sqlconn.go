// Package sqlconn is the shared abstraction for connecting to a
// relational database (MySQL, PostgreSQL, SQLite, …) and exposing
// its queries to MCP clients in a controlled way.
//
// The package keeps the connection lifecycle, result-scanning logic
// and security policies (statement timeout, row cap, table allow-list)
// uniform across drivers. Connector implementations live under
// internal/connectors/builtin/<driver> and translate the per-driver
// config into this shared shape.
package sqlconn

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Config is the cross-driver configuration for a relational
// connection. Each driver adapter maps its own typed config to this
// shape before calling Open.
type Config struct {
	// DriverName is the value passed to sql.Open (e.g. "mysql",
	// "pgx"). The connection pool is owned by the caller.
	DriverName string
	// DSN is the driver-specific connection string.
	DSN string
	// ReadOnly rejects mutating statements (anything that isn't a
	// SELECT, SHOW, EXPLAIN or WITH...SELECT). The check is
	// statement-prefix based and is intended as a defence-in-depth
	// measure, not a full SQL parser.
	ReadOnly bool
	// MaxRows caps the number of rows returned in a single query.
	// Zero or negative means "no cap".
	MaxRows int
	// StatementTimeout bounds the time a single statement may run.
	// Zero means "no per-statement timeout".
	StatementTimeout time.Duration
	// AllowTables, when non-empty, restricts which tables may
	// appear in FROM/JOIN clauses. The check is best-effort: it
	// tokenises the SQL and rejects anything that references a
	// table outside the set. Always supply a lower-case set.
	AllowTables map[string]struct{}
}

// Conn wraps a *sql.DB together with the policies applied to every
// query that runs through it.
type Conn struct {
	db     *sql.DB
	config Config
}

// Open dials the database and verifies the connection with a ping.
// The returned Conn owns the *sql.DB; callers must Close it when
// done.
func Open(cfg Config) (*Conn, error) {
	if cfg.DriverName == "" {
		return nil, errors.New("sqlconn: DriverName is required")
	}
	if cfg.DSN == "" {
		return nil, errors.New("sqlconn: DSN is required")
	}
	if cfg.MaxRows < 0 {
		cfg.MaxRows = 0
	}
	for k := range cfg.AllowTables {
		cfg.AllowTables[strings.ToLower(k)] = struct{}{}
	}
	db, err := sql.Open(cfg.DriverName, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("sqlconn: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlconn: ping: %w", err)
	}
	return &Conn{db: db, config: cfg}, nil
}

// Close releases the underlying connection pool.
func (c *Conn) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// DB exposes the underlying *sql.DB. Callers should rarely need this;
// Query is the higher-level entry point.
func (c *Conn) DB() *sql.DB { return c.db }

// QueryResult is the JSON-friendly shape returned by Query.
type QueryResult struct {
	Columns        []string         `json:"columns"`
	Rows           []map[string]any `json:"rows"`
	RowCount       int              `json:"row_count"`
	Truncated      bool             `json:"truncated,omitempty"`
	ElapsedMS      int64            `json:"elapsed_ms"`
	StatementKind  string           `json:"statement_kind"` // "select" | "show" | "explain" | "other"
}

// Query executes a single statement and returns rows. Mutating
// statements are rejected when ReadOnly is true. The AllowTables set
// (when non-empty) constrains which tables the statement may
// reference.
func (c *Conn) Query(ctx context.Context, sqlText string, args ...any) (*QueryResult, error) {
	trimmed := strings.TrimSpace(sqlText)
	if trimmed == "" {
		return nil, errors.New("sqlconn: empty SQL")
	}
	kind := classify(trimmed)
	if c.config.ReadOnly && !kind.IsReadOnly() {
		return nil, fmt.Errorf("sqlconn: refusing %s statement in read-only mode", kind)
	}
	if len(c.config.AllowTables) > 0 {
		tables := referencedTables(trimmed)
		if len(tables) > 0 {
			for t := range tables {
				if _, ok := c.config.AllowTables[strings.ToLower(t)]; !ok {
					return nil, fmt.Errorf("sqlconn: table %q is not in the allow-list", t)
				}
			}
		}
	}
	if c.config.StatementTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.StatementTimeout)
		defer cancel()
	}

	started := time.Now()
	rows, err := c.db.QueryContext(ctx, trimmed, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlconn: query: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("sqlconn: columns: %w", err)
	}

	out := &QueryResult{
		Columns:       cols,
		Rows:          []map[string]any{},
		StatementKind: kind.String(),
	}
	for rows.Next() {
		// Stop one row early so we can flag truncation cleanly.
		if c.config.MaxRows > 0 && len(out.Rows) >= c.config.MaxRows {
			out.Truncated = true
			break
		}
		row, err := scanRow(rows, cols)
		if err != nil {
			return nil, fmt.Errorf("sqlconn: scan: %w", err)
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlconn: rows: %w", err)
	}
	out.RowCount = len(out.Rows)
	out.ElapsedMS = time.Since(started).Milliseconds()
	return out, nil
}

// ListTables returns the set of (schema, table) pairs visible to the
// connection. The exact driver-level queries are the caller's
// responsibility (see the mysql / postgres connectors).
func (c *Conn) ListTables(ctx context.Context) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, tableListSQL(c.config.DriverName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s, t string
		if err := rows.Scan(&s, &t); err != nil {
			return nil, err
		}
		if t == "" {
			out = append(out, s)
		} else {
			out = append(out, s+"."+t)
		}
	}
	return out, rows.Err()
}

// scanRow reads one row into a generic map keyed by column name.
// Each value is converted into a JSON-friendly representation:
//   - []byte  -> base64 string
//   - time.Time -> RFC3339 string
//   - other   -> as-is
func scanRow(rows *sql.Rows, cols []string) (map[string]any, error) {
	dest := make([]any, len(cols))
	raw := make([]sql.RawBytes, len(cols))
	for i := range raw {
		dest[i] = &raw[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	row := make(map[string]any, len(cols))
	for i, c := range cols {
		row[c] = decodeRaw(raw[i])
	}
	return row, nil
}

func decodeRaw(b sql.RawBytes) any {
	if b == nil {
		return nil
	}
	// Try time.Time detection: drivers usually return time.Time via
	// their own type; the []byte path here is for the bytea/blob
	// fall-through. We can't reliably distinguish a string from a
	// blob, so we keep []byte as a base64 string only when it
	// looks binary (more than 20% non-printable).
	bs := []byte(b)
	if isPrintable(bs) {
		return string(bs)
	}
	return base64.StdEncoding.EncodeToString(bs)
}

func isPrintable(bs []byte) bool {
	if len(bs) == 0 {
		return true
	}
	nonPrintable := 0
	for _, b := range bs {
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		if b < 0x20 || b == 0x7f {
			nonPrintable++
		}
	}
	return nonPrintable*5 < len(bs)
}