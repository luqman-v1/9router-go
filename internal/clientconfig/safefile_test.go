package clientconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// newBase returns an existing base directory inside its own temp dir, plus a
// sibling "outside" directory that nothing may escape into.
func newBase(t *testing.T) (base, outside string) {
	t.Helper()
	root := t.TempDir()
	base = filepath.Join(root, "base")
	outside = filepath.Join(root, "outside")
	for _, dir := range []string{base, outside} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return base, outside
}

func TestResolveGuard_AcceptsNewFileUnderExistingAncestor(t *testing.T) {
	base, _ := newBase(t)
	if err := os.MkdirAll(filepath.Join(base, "a", "b"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	g, err := resolveGuard(base, "a/b/new.json")
	if err != nil {
		t.Fatalf("resolveGuard: %v", err)
	}
	if g.existed {
		t.Error("existed = true for a file that does not exist yet")
	}
	if want := filepath.Join("a", "b", "new.json"); g.rel != want {
		t.Errorf("rel = %q, want %q", g.rel, want)
	}
}

func TestResolveGuard_ExistingFileSetsExisted(t *testing.T) {
	base, _ := newBase(t)
	target := filepath.Join(base, "settings.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	g, err := resolveGuard(base, "settings.json")
	if err != nil {
		t.Fatalf("resolveGuard: %v", err)
	}
	if !g.existed {
		t.Error("existed = false for a file that exists")
	}
}

// A10: a missing base is its own error and is never created implicitly.
func TestResolveGuard_MissingBaseIsTypedAndNotCreated(t *testing.T) {
	base := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := resolveGuard(base, "settings.json")
	if !errors.Is(err, ErrBaseMissing) {
		t.Fatalf("err = %v, want ErrBaseMissing", err)
	}
	if errors.Is(err, ErrPathEscape) {
		t.Error("a missing base must not report as ErrPathEscape")
	}
	if _, statErr := os.Stat(base); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("base was created behind the caller's back: stat err = %v", statErr)
	}
}

// A8: both separator spellings, refused before any filesystem access.
func TestResolveGuard_RejectsDotDot(t *testing.T) {
	base, _ := newBase(t)
	for _, rel := range []string{"../../etc/passwd", `..\..\etc\passwd`, "a/../../x", ".."} {
		t.Run(rel, func(t *testing.T) {
			if _, err := resolveGuard(base, rel); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("err = %v, want ErrPathEscape", err)
			}
		})
	}
}

// A9: device names and alternate data streams, in both separator spellings.
func TestResolveGuard_RejectsDeviceNamesAndADS(t *testing.T) {
	base, _ := newBase(t)
	for _, rel := range []string{"NUL", "nul.json", "x:stream", `a\NUL`, "a/NUL", "COM1", "lpt9.txt"} {
		t.Run(rel, func(t *testing.T) {
			if _, err := resolveGuard(base, rel); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("err = %v, want ErrPathEscape", err)
			}
		})
	}
}

func TestResolveGuard_RejectsAbsoluteAndEmpty(t *testing.T) {
	base, _ := newBase(t)
	for _, rel := range []string{"", "/etc/passwd", ".", "./x", "a//b"} {
		t.Run(rel, func(t *testing.T) {
			if _, err := resolveGuard(base, rel); !errors.Is(err, ErrPathEscape) {
				t.Fatalf("err = %v, want ErrPathEscape", err)
			}
		})
	}
}

func TestResolveGuard_RejectsNonRegularTarget(t *testing.T) {
	base, _ := newBase(t)
	if err := os.Mkdir(filepath.Join(base, "adir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := resolveGuard(base, "adir"); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("err = %v, want ErrNotRegular", err)
	}
}

// F1: a rejected path must never touch anything outside the base.
func TestResolveGuard_DoesNotTouchOutsideCanary(t *testing.T) {
	base, outside := newBase(t)
	canary := filepath.Join(outside, "canary.txt")
	if err := os.WriteFile(canary, []byte("untouched"), 0o600); err != nil {
		t.Fatalf("seed canary: %v", err)
	}
	before, err := os.Stat(canary)
	if err != nil {
		t.Fatalf("stat canary: %v", err)
	}

	for _, rel := range []string{"../outside/canary.txt", "a/../../outside/canary.txt", "NUL"} {
		_, _ = resolveGuard(base, rel)
	}

	after, err := os.Stat(canary)
	if err != nil {
		t.Fatalf("stat canary after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("canary outside the base changed during a rejected resolve")
	}
	if content, _ := os.ReadFile(canary); string(content) != "untouched" {
		t.Errorf("canary content = %q", content)
	}
}
