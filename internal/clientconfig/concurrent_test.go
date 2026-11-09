package clientconfig

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// E1: two writers racing on the same target is accepted behaviour, not an
// error to prevent. Each gets its own backup thanks to O_EXCL, and the rename
// means the target is always one complete body — never a blend.
//
// Last-writer-wins is deliberate: a lock file would add stale-lock and
// deadlock failure modes to a command a human runs by hand, for a problem that
// does not damage anything.
func TestWriteFileAtomic_ConcurrentWritersLeaveOneCompleteFile(t *testing.T) {
	base, _ := newBase(t)
	target := filepath.Join(base, "settings.json")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	bodies := []string{strings.Repeat("a", 4096), strings.Repeat("b", 4096)}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, body := range bodies {
		g, err := resolveGuard(base, "settings.json")
		if err != nil {
			t.Fatalf("resolveGuard: %v", err)
		}
		wg.Add(1)
		go func(g guard, body string) {
			defer wg.Done()
			<-start
			// Both may succeed or one may lose the rename; either outcome is
			// fine as long as nothing is torn.
			_, _ = BackupAndWrite(g, []byte("original"), []byte(body), nil, hooks{})
		}(g, body)
	}
	close(start)
	wg.Wait()

	final, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(final) != bodies[0] && string(final) != bodies[1] {
		t.Errorf("target holds %d bytes that match neither writer — a torn write survived", len(final))
	}
	noTempLeft(t, base)

	backups, err := filepath.Glob(filepath.Join(base, "settings.json.bak.*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(backups) < 1 {
		t.Error("no backup was written")
	}
}
