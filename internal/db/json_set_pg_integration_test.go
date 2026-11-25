//go:build integration

package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestPGJSONSetProductionShapes exercises the exact json_set call shapes the
// gateway issues (int backoffLevel, json('null'), json(?), NULL literals) on a
// live PG. The pre-#1312-fix differential test only passed string arguments,
// which hid three ERROR 42883 failures in production code paths.
func TestPGJSONSetProductionShapes(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("TEST_PG_DSN not set; needs a live PostgreSQL")
	}
	db, err := OpenDatabase(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := EnsureCoreSchema(db); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		q    string
		args []any
	}{
		{"LockConnectionModel", `UPDATE settings SET data = json_set(data, $1, $2, '$.backoffLevel', $3) WHERE id = 1`,
			[]any{"$.locks.x", "2026-01-01", 5}},
		{"RecordConnectionError", `UPDATE settings SET data = json_set(data, '$.backoffLevel', $1, '$.lastError', json($2), '$.status', 'error') WHERE id = 1`,
			[]any{5, `{"msg":"x"}`}},
		{"UnlockConnectionModel", `UPDATE settings SET data = json_set(data, $1, json('null'), '$.backoffLevel', 0) WHERE id = 1`,
			[]any{"$.locks.x"}},
		{"ClearConnectionRateLimit", `UPDATE settings SET data = json_set(data, '$.rateLimitedUntil', NULL, '$.lastError', NULL, '$.status', 'active', '$.backoffLevel', 0) WHERE id = 1`, nil},
		{"OAuthLock", `UPDATE settings SET data = json_set(data, '$.oauthLockedUntil', $1, '$.oauthFailureCount', $2, '$.lastError', json($3)) WHERE id = 1`,
			[]any{"2026-01-01", 3, `{"m":"oauth"}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, c.q, c.args...); err != nil {
				t.Fatal(fmt.Errorf("%s: %w", c.name, err))
			}
		})
	}

	var data string
	if err := db.QueryRowContext(ctx, `SELECT data FROM settings WHERE id=1`).Scan(&data); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"backoffLevel": 0`, `"lastError": {"m": "oauth"}`, `"oauthFailureCount": 3`} {
		if !strings.Contains(data, want) {
			t.Errorf("stored JSON %s missing %s", data, want)
		}
	}
}
