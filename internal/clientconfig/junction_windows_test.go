//go:build windows

package clientconfig

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeJunction creates a real NTFS junction with `mklink /J`.
//
// os.Symlink is the wrong primitive here: a symlink needs Developer Mode or
// SeCreateSymbolicLinkPrivilege, while a junction needs neither — and a
// junction is a different reparse tag (IO_REPARSE_TAG_MOUNT_POINT), which is
// precisely the case filepath.EvalSymlinks is blind to and this guard exists
// for. mklink requires absolute paths; both arguments are made absolute here.
func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J",
		filepath.Clean(mustAbs(t, link)), filepath.Clean(mustAbs(t, target))).CombinedOutput()
	if err != nil {
		t.Fatalf("mklink /J: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("abs %s: %v", p, err)
	}
	return abs
}
