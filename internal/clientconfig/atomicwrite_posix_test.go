//go:build !windows

package clientconfig

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// D2: a brand-new file is created owner-only, because it will hold a key.
func TestWriteFileAtomic_NewFileIsOwnerOnly(t *testing.T) {
	base, _ := newBase(t)
	if err := os.WriteFile(filepath.Join(base, "seed"), nil, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := mustResolve(t, base, "new.json")

	if err := WriteFileAtomic(g, []byte("{}"), hooks{}); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	fi, err := os.Stat(filepath.Join(base, "new.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

// D1: an existing file keeps the mode the user chose.
//
// This is the case umask breaks. OpenFile's perm argument is masked, so under
// umask 0277 a 0640 target would come back 0400 read-only without the explicit
// Chmod on the handle. The test sets that umask to pin the fix rather than the
// happy path.
func TestWriteFileAtomic_PreservesExistingMode(t *testing.T) {
	base, _ := newBase(t)
	target := filepath.Join(base, "settings.json")
	if err := os.WriteFile(target, []byte("original"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	g := mustResolve(t, base, "settings.json")

	old := syscall.Umask(0o277)
	t.Cleanup(func() { syscall.Umask(old) })

	if err := WriteFileAtomic(g, []byte("replacement"), hooks{}); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o640 {
		t.Errorf("mode = %o, want 640 preserved under umask 0277", perm)
	}
}

// B4: the backup is 0600 even when umask would strip the write bit.
func TestWriteExclusiveBackup_ModeIgnoresUmask(t *testing.T) {
	base, _ := newBase(t)
	r := openTestRoot(t, base)

	old := syscall.Umask(0o277)
	t.Cleanup(func() { syscall.Umask(old) })

	name, err := writeExclusiveBackup(r, "settings.json", []byte("secret"), defaultNow)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	fi, err := os.Stat(filepath.Join(base, name))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup mode = %o, want 600 despite umask", perm)
	}
}
