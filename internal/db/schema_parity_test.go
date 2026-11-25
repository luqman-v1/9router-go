package db

import (
	"fmt"
	"strings"
	"testing"
)

// TestSchemaParity is the §4.H.1 gate: every table declared in the SQLite
// schema (coreSchema + keiRouterTables) must have a PG counterpart in
// postgresTables, and the column names must match case-insensitively.
func TestSchemaParity(t *testing.T) {
	sqliteTables := map[string]map[string]bool{}
	for _, t := range coreSchema() {
		cols := map[string]bool{}
		for _, c := range t.columns {
			cols[strings.ToLower(c[0])] = true
		}
		sqliteTables[strings.ToLower(t.name)] = cols
	}
	for _, t := range keiRouterTables {
		cols := map[string]bool{}
		for _, c := range t.columns {
			cols[strings.ToLower(c[0])] = true
		}
		sqliteTables[strings.ToLower(t.name)] = cols
	}
	for _, t := range usageLedgerTables {
		cols := map[string]bool{}
		for _, c := range t.columns {
			cols[strings.ToLower(c[0])] = true
		}
		sqliteTables[strings.ToLower(t.name)] = cols
	}

	pgTables := map[string]map[string]bool{}
	for _, t := range postgresTables {
		cols := map[string]bool{}
		for _, c := range t.columns {
			cols[strings.ToLower(c.name)] = true
		}
		pgTables[strings.ToLower(t.name)] = cols
	}

	var missing []string
	for name := range sqliteTables {
		if _, ok := pgTables[name]; !ok {
			missing = append(missing, "PG missing table: "+name)
		}
	}
	var extra []string
	for name := range pgTables {
		if _, ok := sqliteTables[name]; !ok {
			extra = append(extra, "PG extra table: "+name)
		}
	}

	for name, scols := range sqliteTables {
		pcols, ok := pgTables[name]
		if !ok {
			continue
		}
		for c := range scols {
			if !pcols[c] {
				missing = append(missing, fmt.Sprintf("PG %s.%s missing", name, c))
			}
		}
		for c := range pcols {
			if !scols[c] {
				extra = append(extra, fmt.Sprintf("PG %s.%s extra", name, c))
			}
		}
	}

	if len(missing) > 0 {
		t.Errorf("schema drift (SQLite has, PG missing):\n%s", strings.Join(missing, "\n"))
	}
	if len(extra) > 0 {
		t.Errorf("schema drift (PG has, SQLite missing):\n%s", strings.Join(extra, "\n"))
	}
}
