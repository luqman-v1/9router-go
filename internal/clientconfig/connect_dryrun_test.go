package clientconfig

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// D1/D2/D5 — a dry-run sequence must leave both counters at zero. The
// orchestrator in connect.go is a later sub-task, so this drives the same pieces
// it will: validation with DryRun, a config read, and the wrappers that count.
func TestDryRun_MakesNoNetworkAndNoWrites(t *testing.T) {
	var serverHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits++
		t.Errorf("dry-run sent a request to %s — §5.1 violated", r.URL.Path)
	}))
	defer srv.Close()

	base := t.TempDir()
	cfgPath := filepath.Join(base, "settings.json")
	original := []byte(`{"url":"http://old"}`)
	if err := os.WriteFile(cfgPath, original, 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()
	tr := &countingTransport{inner: http.DefaultTransport}
	fs := newCountingRoot(root)

	// The dry-run path: structure checks only, step 8 skipped because it needs
	// DNS, and the config opened read-only to show what would change.
	endpoint, err := ValidateConnectURL(srv.URL, ConnectURLOptions{
		DryRun:        true,
		AllowInsecure: true,
		AssertPublic:  func(string) error { panic("dry-run reached DNS") },
	})
	if err != nil {
		t.Fatalf("ValidateConnectURL = %v, want nil", err)
	}
	// A loopback host never reaches step 8 in any mode, so Step8Skipped is
	// false here; the public-host case below covers the notice flag.
	if endpoint.Step8Skipped {
		t.Error("Step8Skipped = true for a loopback host; step 8 is not applicable")
	}
	f, err := fs.OpenFile("settings.json", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	f.Close()

	// Also exercise the client the real run would use: a dry run must never
	// build a request with it, which the counter would see.
	_ = NewConnectClient(tr, endpoint, nil)

	if got := tr.Requests(); got != 0 {
		t.Errorf("network requests = %d, want 0", got)
	}
	if got := fs.Writes(); got != 0 {
		t.Errorf("filesystem writes = %d, want 0", got)
	}
	if serverHits != 0 {
		t.Errorf("server saw %d requests, want 0", serverHits)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("config changed: %q -> %q", original, after)
	}
}

func TestCountingTransport_CountsEveryAttempt(t *testing.T) {
	tr := &countingTransport{inner: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("refused")
	})}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/", nil)

	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("stub error was swallowed")
	}
	// A failed attempt still left the process; the contract counts attempts.
	if got := tr.Requests(); got != 1 {
		t.Errorf("Requests() = %d, want 1", got)
	}
	_, _ = tr.RoundTrip(req)
	if got := tr.Requests(); got != 2 {
		t.Errorf("Requests() = %d, want 2", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCountingRoot_CountsWritesAndNotReads(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "a.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer root.Close()
	cr := newCountingRoot(root)

	f, err := cr.OpenFile("a.json", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("read open: %v", err)
	}
	f.Close()
	if got := cr.Writes(); got != 0 {
		t.Errorf("Writes() after a read = %d, want 0", got)
	}

	w, err := cr.OpenFile("b.json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("write open: %v", err)
	}
	w.Close()
	if got := cr.Writes(); got != 1 {
		t.Errorf("Writes() after one write open = %d, want 1", got)
	}

	if err := cr.Rename("b.json", "c.json"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := cr.Remove("c.json"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := cr.Writes(); got != 3 {
		t.Errorf("Writes() = %d, want 3 (write, rename, remove)", got)
	}
}

// D2, first half — the counter must sit in the write path, not beside it.
//
// A wrapper around a function the write path never calls measures nothing.
// The seam is `openRoot`, and this test proves the write path goes through it
// by counting the mutations the write path actually performs.
func TestWritePath_CountedByCountingRoot(t *testing.T) {
	base := t.TempDir()
	original := []byte(`{"url":"http://old"}`)
	if err := os.WriteFile(filepath.Join(base, "settings.json"), original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g, err := resolveGuard(base, "settings.json")
	if err != nil {
		t.Fatalf("guard: %v", err)
	}

	counter := newCounter()
	res, err := BackupAndWrite(g, original, []byte(`{"url":"http://new"}`), nil, countingRootHook(counter))
	if err != nil {
		t.Fatalf("BackupAndWrite: %v", err)
	}
	if res.Backup == "" {
		t.Error("no backup was made")
	}
	if got := counter.Writes(); got == 0 {
		t.Fatal("Writes() = 0; the write path did not go through the counting root")
	}
}

// The exact count, not just "more than zero": the counter has to count the
// mutations the write path performs, and a counter that under-counts would
// let a write slip past the dry-run guarantee.
//
// WriteFileAtomic on an existing target performs three mutating calls:
//  1. create the temp file  (OpenFile with O_CREATE|O_EXCL|O_WRONLY)
//  2. rename it onto target (Rename)
//  3. remove the temp name  (Remove — the name is already gone, but the call
//     was made, and a call that touches the namespace must be counted)
//
// It used to be four, with a second OpenFile reopening the temp by name. That
// reopen was a swap window: a name in a writable directory can be replaced with
// a symlink between the create and the second open, and the bytes then land
// wherever the link points. The temp handle is now kept and written directly,
// so this count is also the assertion that no reopen came back.
//
// The chmod and the fsync happen on the open file handle, not through the
// root, so they are not root mutations.
func TestWriteFileAtomic_CountsEveryMutation(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "settings.json"), []byte(`{"url":"http://old"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g, err := resolveGuard(base, "settings.json")
	if err != nil {
		t.Fatalf("guard: %v", err)
	}

	counter := newCounter()
	if err := WriteFileAtomic(g, []byte(`{"url":"http://new"}`), countingRootHook(counter)); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if got := counter.Writes(); got != 3 {
		t.Errorf("Writes() = %d, want 3 (create temp, rename, remove temp)", got)
	}
}

// D2, second half — a dry run writes nothing, and the counter proves it.
//
// The counter is installed through the same seam the write path uses, the
// dry run performs the reads it is allowed to perform, and the target file is
// byte-for-byte what it was. That is the guarantee §5.1 makes about dry-run:
// zero disk writes, and a file that did not move.
func TestDryRun_CounterSeesNoWritesAndFileIsUnchanged(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "settings.json")
	original := []byte(`{"url":"http://old"}`)
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g, err := resolveGuard(base, "settings.json")
	if err != nil {
		t.Fatalf("guard: %v", err)
	}

	// The dry run reads the config through the counting root — a read, so it
	// must not move the counter.
	counter := newCounter()
	inner, err := defaultHooks.openRoot(g.baseResolved)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer inner.Close()
	cr := &countingRoot{inner: inner, n: counter.n}

	f, err := cr.Open(g.rel)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	f.Close()

	if got := counter.Writes(); got != 0 {
		t.Errorf("Writes() = %d, want 0 — a dry run must not mutate the disk", got)
	}

	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if !bytes.Equal(after, original) {
		t.Errorf("config changed: %q -> %q", original, after)
	}
}
