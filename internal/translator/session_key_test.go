package translator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNewSessionKeyIsUniqueUnderConcurrency pins the property the crash
// depended on: two live requests must never receive the same session key.
//
// The key used to be built from time.Now().UnixNano() alone. On Windows the
// wall clock ticks at ~15.6 ms, so 200 concurrent calls produced only two
// distinct nanosecond values — 104 requests shared one key, therefore one
// *StreamState, therefore one map written from two goroutines. That crashed
// the gateway with "concurrent map writes" under load.
func TestNewSessionKeyIsUniqueUnderConcurrency(t *testing.T) {
	const goroutines = 200
	keys := make([]string, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			keys[idx] = NewSessionKey("stream")
		}(i)
	}
	wg.Wait()

	seen := make(map[string]int, goroutines)
	for _, k := range keys {
		if k == "" {
			t.Fatal("empty session key")
		}
		seen[k]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Fatalf("session key %q handed out %d times", k, n)
		}
	}
	if len(seen) != goroutines {
		t.Fatalf("distinct keys = %d, want %d", len(seen), goroutines)
	}
}

// TestNewSessionKeyIsUniqueOnACoarseClock drives the clock with one frozen
// value, which is what a ~15.6 ms Windows tick does under load. This is the
// only test in the file that can fail on Linux: with the clock pinned, the
// counter is the sole thing keeping two keys apart, so deleting it makes every
// key identical here exactly as it did in production on Windows.
func TestNewSessionKeyIsUniqueOnACoarseClock(t *testing.T) {
	frozen := time.Date(2026, 10, 20, 12, 0, 0, 123456789, time.UTC)
	restore := setSessionClock(func() time.Time { return frozen })
	defer restore()

	const calls = 200
	seen := make(map[string]struct{}, calls)
	for i := 0; i < calls; i++ {
		k := NewSessionKey("stream")
		if _, dup := seen[k]; dup {
			t.Fatalf("call %d: key %q already handed out on a frozen clock", i, k)
		}
		seen[k] = struct{}{}
	}
	if len(seen) != calls {
		t.Fatalf("distinct keys = %d, want %d", len(seen), calls)
	}
}

// TestNewSessionKeyIsUniqueOnACoarseClockConcurrently is the same frozen clock
// under contention, so the counter is exercised the way the crash did.
func TestNewSessionKeyIsUniqueOnACoarseClockConcurrently(t *testing.T) {
	frozen := time.Date(2026, 10, 20, 12, 0, 0, 123456789, time.UTC)
	restore := setSessionClock(func() time.Time { return frozen })
	defer restore()

	const goroutines = 200
	keys := make([]string, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			keys[idx] = NewSessionKey("stream")
		}(i)
	}
	wg.Wait()

	seen := make(map[string]int, goroutines)
	for _, k := range keys {
		seen[k]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Fatalf("frozen-clock key %q handed out %d times", k, n)
		}
	}
}

// TestNewSessionKeyKeepsPrefix pins that the pruner and the log lines can
// still tell a stream session from a gemini one.
func TestNewSessionKeyKeepsPrefix(t *testing.T) {
	key := NewSessionKey("gemini-stream")
	if len(key) < len("gemini-stream-") || key[:len("gemini-stream-")] != "gemini-stream-" {
		t.Fatalf("key %q lost its prefix", key)
	}
}

// TestNoSessionKeyFromTimestampAlone fails when a stream session key is built
// from the wall clock alone again. #447 converted two producers to
// NewSessionKey and left two others on the timestamp; nothing failed, because
// the collision only shows up on a coarse clock. This walks the source instead
// of the runtime, so it bites on every platform.
func TestNoSessionKeyFromTimestampAlone(t *testing.T) {
	root := filepath.Join("..", "..")
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		src, readErr := parser.ParseFile(fset, path, nil, 0)
		if readErr != nil {
			return nil
		}
		ast.Inspect(src, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || !isSessionKeyName(ident.Name) || i >= len(assign.Rhs) {
					continue
				}
				if usesWallClockOnly(assign.Rhs[i]) {
					line := fset.File(assign.Pos()).Line(assign.Pos())
					offenders = append(offenders, path+":"+strconv.Itoa(line))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
	for _, o := range offenders {
		t.Errorf("%s builds a session key from time.Now() alone; use translator.NewSessionKey", o)
	}
}

func isSessionKeyName(name string) bool {
	lower := strings.ToLower(name)
	return lower == "sessionkey" || strings.HasSuffix(lower, "sessionkey")
}

// usesWallClockOnly reports whether expr formats a time.Now() value and never
// reaches for the uniqueness counter.
func usesWallClockOnly(expr ast.Expr) bool {
	fromClock := false
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "time" && (sel.Sel.Name == "Now" || sel.Sel.Name == "UnixNano" || sel.Sel.Name == "Unix") {
			fromClock = true
		}
		return true
	})
	return fromClock
}

func setSessionClock(fn func() time.Time) func() {
	prev := sessionNow
	sessionNow = fn
	return func() { sessionNow = prev }
}
