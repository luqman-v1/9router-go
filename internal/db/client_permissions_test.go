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

	// WAL and shared-memory siblings are created by the driver after the
	// chmod, so they carry the process umask rather than the intended mode.
	// They hold the same page images as the main file, so a reader with
	// directory access can recover credentials from them. This is the part
	// of the contract most likely to be broken by an unrelated change, which
	// is why it is asserted rather than assumed.
	for _, suffix := range []string{"-wal", "-shm"} {
		sibling := path + suffix
		if _, err := os.Stat(sibling); err != nil {
			// Not every open produces both; only assert what exists.
			continue
		}
		assertMode(t, "db file"+suffix, sibling, 0o600)
	}
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
		t.Fatalf("OpenDatabase (first): %v", err)
	}
	db.Close()

	// Loosen both, the way a careless copy or an older build would leave them.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod file loose: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod dir loose: %v", err)
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
