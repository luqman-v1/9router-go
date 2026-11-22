// Package dbmigrate implements the SQLite -> PostgreSQL data migration
// tooling behind `9router-go db export` and `9router-go db import`.
//
// Export writes one JSONL file per user table (discovered via sqlite_master)
// with rows keyed by the table's own column order, so the dump is portable
// and diff-friendly. Import replays those files into PostgreSQL inside
// chunked transactions using INSERT ... ON CONFLICT DO NOTHING, making a
// retry after a partial run safe. The SQLite source is only ever opened
// read-only; nothing in this package mutates it.
package dbmigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	coredb "9router/proxy/internal/db"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// BatchSize is the number of rows per INSERT statement during import. 1000
// keeps each statement well under PG's parameter ceiling (65535) for any
// table in this schema while amortising round-trips.
const BatchSize = 1000

// importOrder lists tables in dependency-safe order (logical FK parents
// first). Anything on disk not listed here is imported afterwards in
// alphabetical order so the result is deterministic. Names are matched
// case-insensitively because the schema mixes camelCase and snake_case.
var importOrder = []string{
	"providerconnections", "accounts", "providernodes",
	"proxypools", "aliases",
	"apikeys", "api_key_budgets", "rate_limits",
	"settings", "vault",
	"usage_history", "usage_windows", "requestdetails",
	"cache_analytics", "schema_migrations",
}

// Export dumps every user table of the SQLite database at sqlitePath into
// outDir as <table>.jsonl, one JSON object per row. It returns the per-table
// row counts in the order the tables were written.
func Export(ctx context.Context, sqlitePath, outDir string) ([]string, error) {
	dsn := "file:" + sqlitePath + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: open sqlite %s: %w", sqlitePath, err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("dbmigrate: sqlite %s unreachable: %w", sqlitePath, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("dbmigrate: create out dir: %w", err)
	}
	tables, err := userTables(ctx, db)
	if err != nil {
		return nil, err
	}
	summaries := make([]string, 0, len(tables))
	for _, t := range tables {
		n, err := exportTable(ctx, db, t, filepath.Join(outDir, t+".jsonl"))
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, fmt.Sprintf("%s: %d rows", t, n))
	}
	return summaries, nil
}

// userTables returns the names of every non-internal table, in name order so
// repeated exports are byte-identical.
func userTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: introspect tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("dbmigrate: scan table name: %w", err)
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

// exportTable streams one table to path and returns the row count.
func exportTable(ctx context.Context, db *sql.DB, table, path string) (int, error) {
	// ORDER BY nothing: rowid order is the insertion order and is stable for
	// a read-only file, which keeps repeat exports comparable.
	rows, err := db.QueryContext(ctx, `SELECT * FROM `+quoteIdent(table))
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: select %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: columns %s: %w", table, err)
	}

	f, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: create %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}
	count := 0
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return count, fmt.Errorf("dbmigrate: scan %s: %w", table, err)
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = values[i]
		}
		if err := enc.Encode(row); err != nil {
			return count, fmt.Errorf("dbmigrate: encode %s row %d: %w", table, count, err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("dbmigrate: iterate %s: %w", table, err)
	}
	return count, f.Close()
}

// ReadJSONL reads one export file back into ordered column names plus rows.
// Column order comes from the first record; later records may omit keys
// (the importer fills them from the column default) but may not introduce
// new ones.
//
// ponytail: this loads the whole table into memory. Large ledgers
// (requestDetails with 20 KB payloads) could spike; add a streaming
// ReadJSONLChunks when someone migrates a >1 GB dump.
func ReadJSONL(path string) ([]string, []map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("dbmigrate: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	var (
		cols []string
		seen map[string]bool
		rows []map[string]any
	)
	for {
		var rec map[string]any
		if err := dec.Decode(&rec); err == io.EOF {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("dbmigrate: decode %s: %w", path, err)
		}
		if cols == nil {
			keys := make([]string, 0, len(rec))
			for k := range rec {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			cols = keys
			seen = make(map[string]bool, len(keys))
			for _, k := range keys {
				seen[k] = true
			}
		} else {
			for k := range rec {
				if !seen[k] {
					return nil, nil, fmt.Errorf("dbmigrate: %s: unexpected column %q after header", path, k)
				}
			}
		}
		rows = append(rows, rec)
	}
	return cols, rows, nil
}

// Import replays every *.jsonl file in dir into the PostgreSQL database
// addressed by dsn. Tables are imported in importOrder (unknown tables last,
// alphabetically); each table is created if missing and its rows inserted in
// chunks of BatchSize inside a transaction, so re-running against a
// partially-populated database is safe.
func Import(ctx context.Context, dsn, dir string) ([]string, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: open pg: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("dbmigrate: pg unreachable: %w", err)
	}
	// Bootstrap without the default settings/_meta rows: seeding first would
	// occupy settings(id=1) with '{}' and the import's ON CONFLICT DO NOTHING
	// would then silently discard the exported real value.
	if err := coredb.EnsureSchemaPostgresWithoutSeed(db); err != nil {
		return nil, err
	}

	files, err := migrationFiles(dir)
	if err != nil {
		return nil, err
	}
	summaries := make([]string, 0, len(files))
	for _, table := range files {
		n, err := importTable(ctx, db, table, filepath.Join(dir, table+".jsonl"))
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, fmt.Sprintf("%s: %d rows", table, n))
	}
	// Seed only genuinely empty rows now that the dump has been replayed.
	if err := coredb.EnsureSchemaPostgres(db); err != nil {
		return nil, err
	}
	return summaries, nil
}

// migrationFiles lists the *.jsonl files in dir as table names, ordered by
// importOrder first and alphabetically afterwards.
func migrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("dbmigrate: read dir %s: %w", dir, err)
	}
	rank := make(map[string]int, len(importOrder))
	for i, n := range importOrder {
		rank[n] = i + 1
	}
	var tables []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		tables = append(tables, strings.TrimSuffix(e.Name(), ".jsonl"))
	}
	sort.SliceStable(tables, func(i, j int) bool {
		ri, rj := rank[strings.ToLower(tables[i])], rank[strings.ToLower(tables[j])]
		if ri != rj {
			return ri < rj
		}
		return tables[i] < tables[j]
	})
	return tables, nil
}

// importTable creates the table when missing (JSONB fallback for legacy
// tables the PG schema does not declare) and inserts its rows in chunks.
func importTable(ctx context.Context, db *sql.DB, table, path string) (int, error) {
	cols, rows, err := ReadJSONL(path)
	if err != nil {
		return 0, err
	}
	if len(cols) == 0 {
		return 0, nil
	}
	isLegacy, err := ensureTableForImport(ctx, db, table, cols)
	if err != nil {
		return 0, err
	}
	if err := insertChunks(ctx, db, table, cols, rows, isLegacy); err != nil {
		return 0, err
	}
	if !isLegacy {
		if err := resetSequences(ctx, db, table, cols); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// ensureTableForImport creates the target table if it does not exist yet.
// The PG schema (schema_postgres.go) declares the tables the gateway owns,
// but a SQLite dump can carry legacy tables that only exist in older installs
// (dbVersion from the pre-Go implementation, upstream_leases before it moved
// into its own bootstrap, etc.). For those we fall back to an all-JSONB table
// and return legacy=true so the insert path serialises every value to JSON.
// Tables the PG schema owns are left untouched and legacy=false is returned.
func ensureTableForImport(ctx context.Context, db *sql.DB, table string, cols []string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM information_schema.tables WHERE lower(table_name) = lower($1)`,
		table,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("dbmigrate: probe table %s: %w", table, err)
	}
	if count > 0 {
		return false, nil
	}
	defs := make([]string, 0, len(cols))
	for _, c := range cols {
		defs = append(defs, quoteIdent(c)+" JSONB")
	}
	q := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", quoteIdent(table), strings.Join(defs, ", "))
	if _, err := db.ExecContext(ctx, q); err != nil {
		return false, fmt.Errorf("dbmigrate: create table %s: %w", table, err)
	}
	return true, nil
}

// insertChunks inserts rows in BatchSize chunks inside one transaction and
// realigns any IDENTITY sequences the explicit IDs moved past. legacy marks
// a fallback all-JSONB table: every value is serialised to JSON and cast to
// ::jsonb so strings, numbers, bools and null all encode cleanly.
func insertChunks(ctx context.Context, db *sql.DB, table string, cols []string, rows []map[string]any, legacy bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("dbmigrate: begin tx %s: %w", table, err)
	}
	defer func() { _ = tx.Rollback() }()

	for start := 0; start < len(rows); start += BatchSize {
		if err := insertChunk(ctx, tx, table, cols, rows[start:min(start+BatchSize, len(rows))], legacy); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("dbmigrate: commit %s: %w", table, err)
	}
	if err := resetSequences(ctx, db, table, cols); err != nil {
		return err
	}
	return nil
}

// insertChunk builds and runs one multi-row INSERT ... ON CONFLICT DO NOTHING.
// legacy=true serialises values to JSON and casts placeholders to ::jsonb;
// legacy=false binds values natively and lets pgx encode per the declared
// column type (TEXT, BIGINT, BOOLEAN, ...).
func insertChunk(ctx context.Context, tx *sql.Tx, table string, cols []string, rows []map[string]any, legacy bool) error {
	// SQLite stores booleans as 0/1 integers; pgx cannot encode those into a
	// BOOLEAN column. Normalize by the target column type once per chunk.
	boolCols := make(map[string]bool, len(cols))
	if !legacy {
		for _, c := range cols {
			var dataType string
			err := tx.QueryRowContext(ctx,
				`SELECT data_type FROM information_schema.columns WHERE lower(table_name)=lower($1) AND lower(column_name)=lower($2)`,
				table, c,
			).Scan(&dataType)
			if err == nil && strings.EqualFold(dataType, "boolean") {
				boolCols[strings.ToLower(c)] = true
			}
		}
	}
	var sb strings.Builder
	sb.WriteString("INSERT INTO ")
	sb.WriteString(quoteIdent(table))
	sb.WriteString(" (")
	for i, c := range cols {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(quoteIdent(c))
	}
	sb.WriteString(") VALUES ")
	args := make([]any, 0, len(rows)*len(cols))
	for r, row := range rows {
		if r > 0 {
			sb.WriteString(", ")
		}
		sb.WriteByte('(')
		for i, c := range cols {
			if i > 0 {
				sb.WriteString(", ")
			}
			if legacy {
				fmt.Fprintf(&sb, "$%d::jsonb", len(args)+1)
			} else {
				fmt.Fprintf(&sb, "$%d", len(args)+1)
			}
			if v, ok := row[c]; ok {
				if legacy {
					b, err := json.Marshal(v)
					if err != nil {
						return fmt.Errorf("dbmigrate: encode %s.%s row %d: %w", table, c, r, err)
					}
					args = append(args, string(b))
				} else {
					args = append(args, normalizeValue(v, boolCols[strings.ToLower(c)]))
				}
			} else {
				args = append(args, nil)
			}
		}
		sb.WriteByte(')')
	}
	sb.WriteString(" ON CONFLICT DO NOTHING")
	if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("dbmigrate: insert %s: %w", table, err)
	}
	return nil
}

// normalizeValue adapts SQLite values for PG: 0/1 integers become bool for
// BOOLEAN columns, and NUL bytes are stripped from TEXT values because PG's
// UTF-8 encoding rejects 0x00 (SQLite accepts it in TEXT).
func normalizeValue(v any, isBool bool) any {
	if v == nil {
		return nil
	}
	if isBool {
		switch n := v.(type) {
		case float64:
			return n != 0
		case int64:
			return n != 0
		case int:
			return n != 0
		case bool:
			return n
		}
	}
	if s, ok := v.(string); ok && strings.ContainsRune(s, 0) {
		return strings.ReplaceAll(s, "\x00", "")
	}
	return v
}

// resetSequences bumps any IDENTITY/serial sequence past the largest id this
// import wrote, so later inserts without an explicit id do not collide.
func resetSequences(ctx context.Context, db *sql.DB, table string, cols []string) error {
	hasID := false
	for _, c := range cols {
		if strings.EqualFold(c, "id") {
			hasID = true
			break
		}
	}
	if !hasID {
		return nil
	}
	// Only integer ids back a serial/identity sequence. A TEXT or UUID primary
	// key (e.g. audit_log.id) has no sequence to bump, and running MAX(id) on
	// it fails with "COALESCE types text and integer cannot be matched" on PG.
	var dataType string
	typeQ := `SELECT data_type FROM information_schema.columns WHERE table_name = ` + quoteLiteral(strings.ToLower(table)) + ` AND column_name = 'id'`
	if err := db.QueryRowContext(ctx, typeQ).Scan(&dataType); err != nil || !isIntegerType(dataType) {
		return nil
	}
	q := `SELECT COALESCE(MAX(id), 0) FROM ` + quoteIdent(table)
	var maxID sql.NullInt64
	if err := db.QueryRowContext(ctx, q).Scan(&maxID); err != nil {
		return fmt.Errorf("dbmigrate: max id %s: %w", table, err)
	}
	if !maxID.Valid || maxID.Int64 == 0 {
		return nil
	}
	setval := `SELECT setval(pg_get_serial_sequence(` + quoteLiteral(table) + `, 'id'), ` + strconv.FormatInt(maxID.Int64, 10) + `, true)`
	if _, err := db.ExecContext(ctx, setval); err != nil {
		// A table whose id is not an identity/serial column (TEXT pk) has no
		// sequence to bump; anything else is a real failure.
		var seq sql.NullString
		if e := db.QueryRowContext(ctx, `SELECT pg_get_serial_sequence(`+quoteLiteral(table)+`, 'id')`).Scan(&seq); e == nil && !seq.Valid {
			return nil
		}
		return fmt.Errorf("dbmigrate: reset sequence %s: %w", table, err)
	}
	return nil
}

// isIntegerType reports whether a PG information_schema data_type can back a
// serial/identity sequence. Anything else (text, uuid, ...) has no sequence.
func isIntegerType(t string) bool {
	switch strings.ToLower(t) {
	case "smallint", "integer", "bigint", "smallserial", "serial", "bigserial":
		return true
	}
	return false
}

// quoteIdent lowercases and double-quotes a SQL identifier, escaping
// embedded quotes. The CREATE TABLE DDL in schema_postgres.go writes table
// names unquoted, so PG folds them to lowercase on creation; the import
// reads mixed-case names from the export's JSONL (mirroring the SQLite
// schema), so we fold here too to match what CREATE TABLE actually named
// the relation.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(strings.ToLower(s), `"`, `""`) + `"`
}

// quoteLiteral single-quotes a SQL string literal, escaping embedded quotes.
func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}
