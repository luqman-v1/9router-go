//go:build integration

package db

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// TestJSONShimDifferential runs the same JSON expressions on SQLite and PG16
// and compares the values (§4.H.4). Skipped unless TEST_PG_DSN is set.
func TestJSONShimDifferential(t *testing.T) {
	pgDSN := os.Getenv("TEST_PG_DSN")
	if pgDSN == "" {
		t.Skip("TEST_PG_DSN not set; skipping PG differential")
	}
	ctx := context.Background()

	pgDB, err := sql.Open("pgx", pgDSN)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer func() { _ = pgDB.Close() }()
	if err := pgDB.PingContext(ctx); err != nil {
		t.Fatalf("pg unreachable: %v", err)
	}
	if err := schemaPostgres(ctx, pgDB); err != nil {
		t.Fatalf("pg schema: %v", err)
	}

	sqliteDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = sqliteDB.Close() }()

	cases := []struct {
		name  string
		query string
		args  []any
		check func(t *testing.T, sq, pg sql.NullString)
	}{
		{
			name:  "json_valid_ok",
			query: "SELECT CAST(json_valid(?) AS TEXT)",
			args:  []any{`{"a":1}`},
			check: func(t *testing.T, sq, pg sql.NullString) {
				// SQLite: 1/0; PG: true/false. Both must be truthy.
				if sq.String != "1" || (pg.String != "true" && pg.String != "t") {
					t.Errorf("json_valid ok: sqlite=%q pg=%q", sq.String, pg.String)
				}
			},
		},
		{
			name:  "json_valid_bad",
			query: "SELECT CAST(json_valid(?) AS TEXT)",
			args:  []any{`not json`},
			check: func(t *testing.T, sq, pg sql.NullString) {
				if sq.String != "0" || (pg.String != "false" && pg.String != "f") {
					t.Errorf("json_valid bad: sqlite=%q pg=%q", sq.String, pg.String)
				}
			},
		},
		{
			name:  "json_extract_scalar",
			query: "SELECT COALESCE(json_extract(?, '$.cached_tokens'), 'MISSING')",
			args:  []any{`{"cached_tokens":42}`},
			check: func(t *testing.T, sq, pg sql.NullString) {
				if sq.String != "42" || pg.String != "42" {
					t.Errorf("json_extract scalar: sqlite=%q pg=%q", sq.String, pg.String)
				}
			},
		},
		{
			name:  "json_extract_missing",
			query: "SELECT COALESCE(json_extract(?, '$.nope'), 'MISSING')",
			args:  []any{`{"a":1}`},
			check: func(t *testing.T, sq, pg sql.NullString) {
				if sq.String != "MISSING" || pg.String != "MISSING" {
					t.Errorf("json_extract missing: sqlite=%q pg=%q", sq.String, pg.String)
				}
			},
		},
		{
			name:  "json_extract_nonjson",
			query: "SELECT COALESCE(json_extract(?, '$.a'), 'NULL')",
			args:  []any{`plain text`},
			check: func(t *testing.T, sq, pg sql.NullString) {
				if sq.String != "NULL" || pg.String != "NULL" {
					t.Errorf("json_extract nonjson: sqlite=%q pg=%q", sq.String, pg.String)
				}
			},
		},
		{
			name:  "json_set_path",
			query: "SELECT json_set(?, '$.backoffLevel', ?)",
			args:  []any{`{"a":1}`, `5`},
			check: func(t *testing.T, sq, pg sql.NullString) {
				if !strings.Contains(sq.String, "backoffLevel") || !strings.Contains(pg.String, "backoffLevel") {
					t.Errorf("json_set path: sqlite=%q pg=%q", sq.String, pg.String)
				}
			},
		},
		{
			name:  "json_array_build",
			query: "SELECT json_array(?, ?)",
			args:  []any{`a`, `b`},
			check: func(t *testing.T, sq, pg sql.NullString) {
				if !strings.Contains(sq.String, "a") || !strings.Contains(pg.String, "a") {
					t.Errorf("json_array: sqlite=%q pg=%q", sq.String, pg.String)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sq, pg sql.NullString

			if err := sqliteDB.QueryRow(tc.query, tc.args...).Scan(&sq); err != nil {
				t.Fatalf("sqlite: %v", err)
			}
			pgQuery := rebindQuery(tc.query)
			if err := pgDB.QueryRow(pgQuery, tc.args...).Scan(&pg); err != nil {
				t.Fatalf("pg: %v", err)
			}
			tc.check(t, sq, pg)
		})
	}
}
