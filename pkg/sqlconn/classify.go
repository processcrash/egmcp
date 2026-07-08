package sqlconn

import (
	"regexp"
	"strings"
)

// statementKind tags a statement so callers (and the read-only check)
// can decide quickly whether it is safe.
type statementKind int

const (
	kindOther statementKind = iota
	kindSelect
	kindShow
	kindExplain
)

func (k statementKind) IsReadOnly() bool {
	switch k {
	case kindSelect, kindShow, kindExplain:
		return true
	}
	return false
}

func (k statementKind) String() string {
	switch k {
	case kindSelect:
		return "select"
	case kindShow:
		return "show"
	case kindExplain:
		return "explain"
	}
	return "other"
}

// classify inspects the leading keyword(s) of a statement.
//
// This is intentionally a tiny check: it strips leading comments and
// whitespace and inspects the first keyword. It's adequate to catch
// the common accidents ("oops, I meant SELECT but I sent DELETE")
// without trying to be a SQL parser.
func classify(sqlText string) statementKind {
	s := stripCommentsAndWhitespace(sqlText)
	upper := strings.ToUpper(s)

	// Find the first significant token.
	first := firstWord(upper)
	switch first {
	case "SELECT", "WITH", "TABLE", "VALUES":
		return kindSelect
	case "SHOW":
		return kindShow
	case "EXPLAIN", "DESCRIBE", "DESC":
		return kindExplain
	}
	return kindOther
}

// tableListSQL is intentionally driver-aware: we keep the well-known
// information_schema queries here rather than inline at the call site.
func tableListSQL(driverName string) string {
	switch driverName {
	case "mysql":
		return `SELECT TABLE_SCHEMA, TABLE_NAME FROM information_schema.TABLES
                WHERE TABLE_SCHEMA NOT IN ('information_schema','mysql','performance_schema','sys')
                ORDER BY TABLE_SCHEMA, TABLE_NAME`
	case "pgx", "postgres":
		return `SELECT table_schema, table_name FROM information_schema.tables
                WHERE table_schema NOT IN ('pg_catalog','information_schema')
                ORDER BY table_schema, table_name`
	default:
		return `SELECT name, NULL FROM sqlite_master WHERE type='table'`
	}
}

// stripCommentsAndWhitespace removes SQL line comments (-- ...) and
// block comments (/* ... */) from the beginning of the input and
// trims surrounding whitespace.
func stripCommentsAndWhitespace(s string) string {
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "--") {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
				continue
			}
			return ""
		}
		if strings.HasPrefix(s, "/*") {
			if i := strings.Index(s, "*/"); i >= 0 {
				s = s[i+2:]
				continue
			}
			return ""
		}
		break
	}
	return s
}

func firstWord(s string) string {
	for i, r := range s {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return s[:i]
		}
	}
	return s
}

// tableRefRE captures `from`/`join` clauses' table references. It's
// deliberately simple — it ignores subqueries and complex CTEs but
// catches the overwhelming majority of real-world queries.
var tableRefRE = regexp.MustCompile(`(?i)\b(?:from|join)\s+("([^"]+)"|` + "`([^`]+)`" + `|([a-zA-Z_][\w.]*))`)

// referencedTables extracts the set of table names referenced by a
// statement. The set is best-effort and may miss dynamically
// constructed identifiers; that is acceptable for a defence-in-depth
// check.
func referencedTables(sqlText string) map[string]struct{} {
	matches := tableRefRE.FindAllStringSubmatch(sqlText, -1)
	out := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		// m[2], m[3], m[4] are the three capture groups; exactly
		// one is non-empty.
		name := m[2]
		if name == "" {
			name = m[3]
		}
		if name == "" {
			name = m[4]
		}
		if name == "" {
			continue
		}
		// Strip schema prefix when present.
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		out[name] = struct{}{}
	}
	return out
}