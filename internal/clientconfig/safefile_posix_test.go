//go:build !windows

package clientconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// A1: an ancestor symlink pointing out of the base is refused as a symlink
// problem, not as a generic escape, and nothing is created at the target.
func TestResolveGuard_AncestorSymlinkOutOfBase(t *testing.T) {
	base, outside := newBase(t)
	if err := os.Symlink(outside, filepath.Join(base, "evil")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := resolveGuard(base, "evil/passwd")
	if !errors.Is(err, ErrSymlinkAncestor) {
		t.Fatalf("err = %v, want ErrSymlinkAncestor", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "passwd")); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("a file appeared outside the base: %v", statErr)
	}
}

// A2: an ancestor symlink that stays inside the base is allowed.
func TestResolveGuard_AncestorSymlinkInsideBase(t *testing.T) {
	base, _ := newBase(t)
	if err := os.Mkdir(filepath.Join(base, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink("sub", filepath.Join(base, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	g, err := resolveGuard(base, "link/config.json")
	if err != nil {
		t.Fatalf("resolveGuard: %v", err)
	}
	// The resolved path runs through "sub", so the guard's relative path does
	// too — writing to g.rel must never silently target the link name.
	if want := filepath.Join("sub", "config.json"); g.rel != want {
		t.Errorf("rel = %q, want %q", g.rel, want)
	}
}

// A7: a symlink deep above a target that does not exist yet is still caught.
func TestResolveGuard_DeepAncestorSymlinkOutOfBase(t *testing.T) {
	base, outside := newBase(t)
	if err := os.Symlink(outside, filepath.Join(base, "a")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := resolveGuard(base, "a/b/c/new.json"); !errors.Is(err, ErrSymlinkAncestor) {
		t.Fatalf("err = %v, want ErrSymlinkAncestor", err)
	}
}

// D1: a symlink as the final component is refused even when it stays inside.
func TestResolveGuard_FinalComponentSymlinkRefused(t *testing.T) {
	base, _ := newBase(t)
	if err := os.WriteFile(filepath.Join(base, "real.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Symlink("real.json", filepath.Join(base, "alias.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := resolveGuard(base, "alias.json"); !errors.Is(err, ErrSymlinkAncestor) {
		t.Fatalf("err = %v, want ErrSymlinkAncestor", err)
	}
}
