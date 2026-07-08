package sqlconn

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want statementKind
	}{
		{"SELECT 1", kindSelect},
		{"select 1", kindSelect},
		{"  SELECT * FROM t", kindSelect},
		{"-- comment\nSELECT 1", kindSelect},
		{"/* comment */\nSELECT 1", kindSelect},
		{"WITH x AS (SELECT 1) SELECT * FROM x", kindSelect},
		{"SHOW TABLES", kindShow},
		{"EXPLAIN SELECT 1", kindExplain},
		{"DESCRIBE t", kindExplain},
		{"DESC t", kindExplain},
		{"INSERT INTO t VALUES (1)", kindOther},
		{"UPDATE t SET a = 1", kindOther},
		{"DELETE FROM t", kindOther},
		{"", kindOther},
	}
	for _, c := range cases {
		if got := classify(c.in); got != c.want {
			t.Fatalf("classify(%q): want %v, got %v", c.in, c.want, got)
		}
	}
}

func TestReferencedTables(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]struct{}
	}{
		{"SELECT * FROM users", map[string]struct{}{"users": {}}},
		{"SELECT a.x FROM users AS a JOIN orders ON a.id = orders.user_id",
			map[string]struct{}{"users": {}, "orders": {}}},
		{"SELECT * FROM \"MyTable\"", map[string]struct{}{"MyTable": {}}},
		{"SELECT * FROM `backtick`", map[string]struct{}{"backtick": {}}},
		{"SELECT * FROM dbo.orders", map[string]struct{}{"orders": {}}},
		{"SELECT 1", map[string]struct{}{}},
	}
	for _, c := range cases {
		got := referencedTables(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("referencedTables(%q): got %v, want %v", c.in, got, c.want)
		}
		for k := range c.want {
			if _, ok := got[k]; !ok {
				t.Fatalf("referencedTables(%q): missing %q (got %v)", c.in, k, got)
			}
		}
	}
}

func TestTableListSQL(t *testing.T) {
	for _, driver := range []string{"mysql", "pgx", "postgres", "sqlite"} {
		if tableListSQL(driver) == "" {
			t.Fatalf("missing SQL for %s", driver)
		}
	}
}

func TestIsPrintable(t *testing.T) {
	if !isPrintable([]byte("hello\n")) {
		t.Fatalf("printable bytes flagged as non-printable")
	}
	if isPrintable([]byte{0, 1, 2, 3, 4, 5}) {
		t.Fatalf("non-printable bytes flagged as printable")
	}
}