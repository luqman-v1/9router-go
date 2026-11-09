package db

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestOpenDatabaseRestrictsFileAndDirPermissions proves the at-rest
// protection the credential vault's threat model relies on: the database
// holds provider API keys and OAuth tokens, so the file must be private to
// the owning user and its directory must not be traversable by anyone else.
//
// OpenDatabase has asserted these modes since the permission fix, but nothing
// proved it — a regression here is silent on every platform that does not
// happen to be inspected by hand, and it turns a plaintext-credential store
// into a world-readable one.
func TestOpenDatabaseRestrictsFileAndDirPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "data.sqlite")

	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer db.Close()

	assertMode(t, "db file", path, 0o600)
	assertMode(t, "db dir", dir, 0o700)
}

// TestRestrictSQLiteModesCoversSidecars proves the -wal and -shm sidecars end
// up at 0600, which is the part of the contract the open-time chmod cannot
// reach: SQLite creates both files on the first *write*, so at the moment
// OpenDatabase returns they do not exist yet and carry the process umask
// rather than the intended mode. They hold the same page images as the main
// file, so a reader with directory access can recover credentials from them.
//
// The write is the load-bearing step. Without it the sidecars are absent, the
// assertion loop skips them, and the test passes without checking anything.
func TestRestrictSQLiteModesCoversSidecars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "data.sqlite")

	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer db.Close()

	// First write: this is what brings the sidecars into existence.
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS probe_t (id INTEGER)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// The sidecars must exist by now, or the assertions below would silently
	// prove nothing. Assert their presence first so a driver change that stops
	// creating them turns this test red instead of vacuous.
	for _, suffix := range []string{"-wal", "-shm"} {
		sibling := path + suffix
		if _, err := os.Stat(sibling); err != nil {
			t.Fatalf("expected %s to exist after the first write: %v", sibling, err)
		}
	}

	RestrictSQLiteModes(dir, path)

	assertMode(t, "db file", path, 0o600)
	assertMode(t, "db wal", path+"-wal", 0o600)
	assertMode(t, "db shm", path+"-shm", 0o600)
}

// TestRestrictSQLiteModesTightensLooseSidecars proves a sidecar left
// world-readable — by an older build that never chmod'ed it, or by a restore
// that copied the file without its mode — is tightened on the next call rather
// than left as found. This is the case the open-time chmod could never serve.
func TestRestrictSQLiteModesTightensLooseSidecars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "data.sqlite")

	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS probe_t (id INTEGER)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		sibling := path + suffix
		if _, err := os.Stat(sibling); err != nil {
			t.Fatalf("expected %s to exist: %v", sibling, err)
		}
		if err := os.Chmod(sibling, 0o644); err != nil {
			t.Fatalf("chmod %s loose: %v", sibling, err)
		}
	}

	RestrictSQLiteModes(dir, path)

	assertMode(t, "db wal after tighten", path+"-wal", 0o600)
	assertMode(t, "db shm after tighten", path+"-shm", 0o600)
}

// TestOpenDatabaseTightensLoosePermissions proves an existing database that
// was left world-readable — by an older build, a manual copy, or a restore
// from a backup — is tightened on the next open rather than left as found.
// The chmod runs on every open for exactly this case.
func TestOpenDatabaseTightensLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "data.sqlite")

	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	db.Close()

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir loose: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod file loose: %v", err)
	}

	db2, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase (second): %v", err)
	}
	defer db2.Close()

	assertMode(t, "db file after reopen", path, 0o600)
	assertMode(t, "db dir after reopen", dir, 0o700)
}

// TestOpenDatabaseMkdirAllCreatesPrivateDir proves a fresh install — where
// the data directory does not exist yet — ends up private rather than at the
// MkdirAll default, which is subject to the process umask and is commonly
// 0755.
func TestOpenDatabaseMkdirAllCreatesPrivateDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
	// A nested path so the whole chain is created by this call.
	dir := filepath.Join(t.TempDir(), "fresh", "nested")
	path := filepath.Join(dir, "data.sqlite")

	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer db.Close()

	assertMode(t, "fresh db dir", dir, 0o700)
	assertMode(t, "fresh db file", path, 0o600)
}

// assertMode compares a path's mode against want, reporting the full
// permission bits so a failure names what was actually found.
func assertMode(t *testing.T, label, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", label, err)
	}
	got := info.Mode().Perm()
	if got != want {
		t.Errorf("%s mode = %#o, want %#o", label, got, want)
	}
}
