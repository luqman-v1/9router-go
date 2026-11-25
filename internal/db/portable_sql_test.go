package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestPortableSQL walks the repo and fails if any forbidden engine-specific
// SQL construct reappears outside schema*.go files. Schema files are excluded
// because SQLite DDL legitimately uses AUTOINCREMENT (worker A owns the
// schema-porting work).
//
// Forbidden constructs break PostgreSQL parity:
//   - INSERT OR IGNORE / INSERT OR REPLACE — SQLite-only upsert syntax.
//     Portable form: `INSERT ... ON CONFLICT (...) DO NOTHING | DO UPDATE SET ...`.
//   - AUTOINCREMENT — SQLite column attribute.
//     Portable form: `BIGINT GENERATED ALWAYS AS IDENTITY` on Postgres.
//   - datetime('now') — SQLite function.
//     Portable form: compute the timestamp in Go and pass it as a `?` arg.
func TestPortableSQL(t *testing.T) {
	repoRoot := findRepoRoot(t)

	// Map each forbidden construct to a hint about its portable replacement.
	forbidden := []struct {
		pattern *regexp.Regexp
		hint    string
	}{
		{regexp.MustCompile(`INSERT\s+OR\s+IGNORE\b`), "use `INSERT ... ON CONFLICT (...) DO NOTHING`"},
		{regexp.MustCompile(`INSERT\s+OR\s+REPLACE\b`), "use `INSERT ... ON CONFLICT (...) DO UPDATE SET ...`"},
		{regexp.MustCompile(`INSERT\s+OR\s+ABORT\b`), "use `INSERT ... ON CONFLICT (...) DO NOTHING`"},
		{regexp.MustCompile(`\bAUTOINCREMENT\b`), "use `INTEGER PRIMARY KEY` on SQLite and `BIGINT GENERATED ALWAYS AS IDENTITY` on Postgres"},
		{regexp.MustCompile(`datetime\s*\(\s*'now'\s*\)`), "compute the timestamp in Go and pass it as a `?` arg"},
	}

	var offenders []string
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", "dist", "vendor", ".changes":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_test.go") {
			return nil
		}
		// internal/dbtest is test-support code; its SQLite DDL is fixture-only.
		if strings.Contains(filepath.ToSlash(path), "/internal/dbtest/") {
			return nil
		}
		if strings.HasPrefix(base, "schema") && strings.HasSuffix(base, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repoRoot, path)
		for lineNo, line := range strings.Split(string(data), "\n") {
			// Skip comments mentioning forbidden strings in prose (docs, notes).
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, f := range forbidden {
				if f.pattern.MatchString(line) {
					offenders = append(offenders, rel+":"+strconv.Itoa(lineNo+1)+" matches "+f.pattern.String()+" — "+f.hint)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("found %d engine-specific SQL construct(s); use portable form instead:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// findRepoRoot walks up from the current directory until it finds go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod — are we inside the repo?")
		}
		dir = parent
	}
}
