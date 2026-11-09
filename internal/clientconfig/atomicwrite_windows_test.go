//go:build windows

package clientconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A4b: pins why escapesResolved must use os.Readlink. A junction carries
// ModeIrregular rather than ModeSymlink, and EvalSymlinks follows a link only
// when ModeSymlink is set — so EvalSymlinks hands a junction back unchanged and
// an escape check built on it would silently pass. os.Readlink resolves it.
//
// If anyone "simplifies" escapesResolved back to EvalSymlinks, this is the test
// that explains why they cannot.
func TestJunction_EvalSymlinksIsBlindAndReadlinkIsNot(t *testing.T) {
	base, outside := newBase(t)
	junction := filepath.Join(base, "evil")
	makeJunction(t, junction, outside)

	fi, err := os.Lstat(junction)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("junction reports ModeSymlink; the premise of the guard has changed")
	}

	eval, err := filepath.EvalSymlinks(junction)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if eval != junction {
		t.Errorf("EvalSymlinks resolved the junction to %q — the stdlib behaviour this test pins has changed", eval)
	}

	target, err := os.Readlink(junction)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if !filepath.IsAbs(target) {
		t.Errorf("Readlink returned relative target %q; junctions store absolute targets", target)
	}
}

// A3: a junction pointing out of the base is reported as a symlink problem.
// It must not fall through to ErrPathEscape — that would mean the diagnosis
// came from the lexical containment check, which is the check that cannot see
// a junction at all.
func TestResolveGuard_JunctionOutOfBase(t *testing.T) {
	for _, rel := range []string{`evil\x.json`, "evil/x.json"} {
		t.Run(rel, func(t *testing.T) {
			base, outside := newBase(t)
			makeJunction(t, filepath.Join(base, "evil"), outside)

			_, err := resolveGuard(base, rel)
			if !errors.Is(err, ErrSymlinkAncestor) {
				t.Fatalf("err = %v, want ErrSymlinkAncestor", err)
			}
		})
	}
}

// C4: a transient lock clears and the rename succeeds within budget.
func TestRenameWithRetry_TransientLockRecovers(t *testing.T) {
	base, _ := newBase(t)
	r := openTestRoot(t, base)
	if err := os.WriteFile(filepath.Join(base, "tmp"), []byte("new"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var calls atomic.Int32
	flaky := hooks{rename: func(r rootFS, oldRel, newRel string) error {
		if calls.Add(1) <= 2 {
			return windows.ERROR_SHARING_VIOLATION
		}
		return r.Rename(oldRel, newRel)
	}}

	start := time.Now()
	if err := renameWithRetry(r, "tmp", "settings.json", flaky); err != nil {
		t.Fatalf("renameWithRetry: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("rename called %d times, want 3", got)
	}
	budget := time.Duration(0)
	for i := 0; i < 2; i++ {
		budget += renameBackoff[i]
	}
	if elapsed := time.Since(start); elapsed < budget {
		t.Errorf("retries finished in %v, less than the %v backoff they should have waited", elapsed, budget)
	}
}

// C5: a lock that never clears fails with a message the user can act on, and
// the original file is still there — no truncating fallback.
func TestRenameWithRetry_PersistentLockFailsClearly(t *testing.T) {
	base, _ := newBase(t)
	r := openTestRoot(t, base)
	target := filepath.Join(base, "settings.json")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	always := hooks{rename: func(rootFS, string, string) error {
		return windows.ERROR_SHARING_VIOLATION
	}}
	err := renameWithRetry(r, "settings.json", "settings.json", always)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("message %q does not tell the user the file is locked", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "original" {
		t.Errorf("target = %q, want original preserved", got)
	}
}

// A non-lock error is not retried: waiting 385ms to report a real problem only
// makes the user wait longer for the same answer.
func TestRenameWithRetry_NonLockErrorIsImmediate(t *testing.T) {
	base, _ := newBase(t)
	r := openTestRoot(t, base)

	var calls atomic.Int32
	broken := hooks{rename: func(rootFS, string, string) error {
		calls.Add(1)
		return syscall.ERROR_FILE_NOT_FOUND
	}}
	if err := renameWithRetry(r, "a", "b", broken); err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("rename called %d times for a non-retryable error, want 1", got)
	}
}
