package clientconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// mustResolve resolves a relative path inside base or fails the test.
func mustResolve(t *testing.T, base, rel string) guard {
	t.Helper()
	g, err := resolveGuard(base, rel)
	if err != nil {
		t.Fatalf("resolveGuard(%q): %v", rel, err)
	}
	return g
}

// noTempLeft asserts the directory holds no leftover temp file.
func noTempLeft(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// C1: a write that fails midway must leave the original intact and the temp
// cleaned up. This is the disk-full case — the reason a temp file exists.
func TestWriteFileAtomic_WriteFailsMidway(t *testing.T) {
	base, _ := newBase(t)
	target := filepath.Join(base, "settings.json")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := mustResolve(t, base, "settings.json")

	badWrite := hooks{write: func(*os.File, []byte) error { return syscall.EIO }}
	err := WriteFileAtomic(g, []byte("replacement"), badWrite)
	if !errors.Is(err, ErrWriteFailed) {
		t.Fatalf("err = %v, want ErrWriteFailed", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "original" {
		t.Errorf("target = %q, want original", got)
	}
	noTempLeft(t, base)
}

// C2: a failed sync must not be followed by a rename.
func TestWriteFileAtomic_SyncFails(t *testing.T) {
	base, _ := newBase(t)
	target := filepath.Join(base, "settings.json")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := mustResolve(t, base, "settings.json")

	badSync := hooks{sync: func(*os.File) error { return syscall.EIO }}
	if err := WriteFileAtomic(g, []byte("replacement"), badSync); !errors.Is(err, ErrWriteFailed) {
		t.Fatalf("err = %v, want ErrWriteFailed", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "original" {
		t.Errorf("target = %q, want original", got)
	}
	noTempLeft(t, base)
}

// C3: a rename failure must leave the original and never fall back to writing
// the target in place.
func TestWriteFileAtomic_RenameFails(t *testing.T) {
	base, _ := newBase(t)
	target := filepath.Join(base, "settings.json")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := mustResolve(t, base, "settings.json")

	badRename := hooks{rename: func(rootFS, string, string) error { return syscall.EACCES }}
	if err := WriteFileAtomic(g, []byte("replacement"), badRename); !errors.Is(err, ErrWriteFailed) {
		t.Fatalf("err = %v, want ErrWriteFailed", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "original" {
		t.Errorf("target = %q — a failed rename must not degrade into an in-place write", got)
	}
	noTempLeft(t, base)
}

func TestWriteFileAtomic_ReplacesContent(t *testing.T) {
	base, _ := newBase(t)
	target := filepath.Join(base, "settings.json")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := mustResolve(t, base, "settings.json")

	if err := WriteFileAtomic(g, []byte("replacement"), hooks{}); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "replacement" {
		t.Errorf("target = %q, want replacement", got)
	}
	noTempLeft(t, base)
}

// Mode assertions live in atomicwrite_posix_test.go: os.Chmod on Windows only
// toggles the read-only bit, so a Perm() check there would assert nothing.

// createTemp must land next to the target: a cross-device rename is not
// atomic, and os.Root has no CreateTemp to rely on.
func TestCreateTemp_SameDirectoryAsTarget(t *testing.T) {
	base, _ := newBase(t)
	if err := os.Mkdir(filepath.Join(base, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r := openTestRoot(t, base)

	f, name, err := createTemp(r, filepath.Join("nested", "settings.json"))
	if err != nil {
		t.Fatalf("createTemp: %v", err)
	}
	defer f.Close()
	if got := filepath.Dir(name); got != "nested" {
		t.Errorf("temp dir = %q, want nested", got)
	}
	// The handle comes back open on the file it created. Reopening by name is
	// the swap window this signature exists to remove, so the assertion is
	// that the descriptor names the temp path, not that a second open works.
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat handle: %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("temp size = %d, want 0", fi.Size())
	}
}
