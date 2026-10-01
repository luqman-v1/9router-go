package daemon

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestLogTailReadsOnlyTheTail pins the cost of `logs`: the window is read, not
// the file. A log far larger than the window still answers with whole lines
// from the end, and the caller never sees the first byte of the file.
func TestLogTailReadsOnlyTheTail(t *testing.T) {
	isolate(t)

	const (
		lines = 100_000
		width = 32
	)
	var b strings.Builder
	for i := range lines {
		b.WriteString(paddedLine(i, width))
		b.WriteByte('\n')
	}
	if int64(b.Len()) <= logTailMaxBytes {
		t.Fatalf("test log is %d bytes, needs to exceed the %d byte window", b.Len(), logTailMaxBytes)
	}
	writeLog(t, b.String())

	got := LogTail(3)
	want := strings.Join([]string{
		paddedLine(lines-3, width),
		paddedLine(lines-2, width),
		paddedLine(lines-1, width),
	}, "\n")
	if got != want {
		t.Fatalf("LogTail(3) = %q, want %q", got, want)
	}
	if strings.Contains(got, "\n"+paddedLine(0, width)) {
		t.Fatalf("LogTail(3) read from the head of the file: %q", got)
	}
}

// The window is a byte budget, so a caller asking for more lines than fit gets
// the newest ones — starting wherever the window happened to land.
func TestLogTailWindowTruncatesTheOldestLine(t *testing.T) {
	isolate(t)

	const lineBytes = 11
	lineCount := int(logTailMaxBytes/lineBytes) + 10
	var b strings.Builder
	for i := range lineCount {
		b.WriteString("line-")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("y")
		b.WriteByte('\n')
	}
	writeLog(t, b.String())

	got := LogTail(lineCount + 10)
	if !strings.HasSuffix(got, "line-"+strconv.Itoa(lineCount-1)+"y") {
		t.Fatalf("LogTail() must end on the newest line, got %q", lastLine(got))
	}
	if len(got) >= int(logTailMaxBytes) {
		t.Fatalf("LogTail returned %d bytes, past the %d byte window", len(got), logTailMaxBytes)
	}
	// The window lands mid-line; that fragment is dropped rather than shown as
	// an entry nobody wrote, so every line here is whole and the first one is a
	// real line of the log.
	if first, _, _ := strings.Cut(got, "\n"); !strings.HasPrefix(first, "line-") {
		t.Fatalf("LogTail started on a partial line: %q", first)
	}
}

// The cap is the only thing bounding the log: nothing else ever truncates it,
// because the daemon holds the descriptor for as long as it lives. Trimming
// keeps the newest RunLogMaxBytes so the lines an operator is chasing survive,
// and the discarded head lands in gateway.log.1 rather than being deleted.
func TestOpenRunLogKeepsLogBounded(t *testing.T) {
	isolate(t)
	appendLog(t, "tail-marker\n", RunLogMaxBytes+int64(len("tail-marker\n")))

	f, err := openRunLog()
	if err != nil {
		t.Fatalf("openRunLog: %v", err)
	}
	defer f.Close()

	// Measured right after the trim, before anything is appended: the trim is
	// what bounds the file, not what this test writes next.
	if size := mustSize(t); size != RunLogMaxBytes {
		t.Fatalf("log is %d bytes after trimming, want exactly the %d cap", size, RunLogMaxBytes)
	}

	// The marker sat at the very tail of the oversized file, so it is still
	// there afterwards — that is what "keep the newest bytes" has to mean.
	if tail := LogTail(0); !strings.Contains(tail, "tail-marker") {
		t.Fatal("trimming dropped the newest line of the log")
	}

	// The discarded head is kept rather than deleted, so nothing an operator
	// might still want vanishes: it is everything that did not fit under the cap.
	trimmed, err := os.ReadFile(LogPath() + ".1")
	if err != nil {
		t.Fatalf("moved-aside head: %v", err)
	}
	if want := len("tail-marker\n") + 8; len(trimmed) != want {
		t.Fatalf("moved-aside head = %d bytes, want the %d that were discarded", len(trimmed), want)
	}
}

// A log under the cap is appended to, not rotated: the previous run's history is
// what an operator reads after a restart.
func TestOpenRunLogKeepsLogUnderCap(t *testing.T) {
	isolate(t)
	writeLog(t, "first\n")

	f, err := openRunLog()
	if err != nil {
		t.Fatalf("openRunLog: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString("second\n"); err != nil {
		t.Fatalf("append: %v", err)
	}

	if _, err := os.Stat(LogPath() + ".1"); !os.IsNotExist(err) {
		t.Fatalf("a small log must not be rotated, stat err = %v", err)
	}
	if got, want := LogTail(0), "first\nsecond"; got != want {
		t.Fatalf("LogTail(0) = %q, want %q", got, want)
	}
}

// Exactly one previous generation is kept. Each trim overwrites gateway.log.1
// rather than appending to it, so a daemon that trims on every start cannot make
// that file grow into a second full copy of the installation's history.
func TestOpenRunLogReplacesOlderGeneration(t *testing.T) {
	isolate(t)

	for _, filler := range []string{"older", "newer", "newest"} {
		appendLog(t, filler+"\n", RunLogMaxBytes+int64(len(filler)+1))

		f, err := openRunLog()
		if err != nil {
			t.Fatalf("openRunLog: %v", err)
		}
		// Checked before the append: the trim is what bounds the file.
		if size := mustSize(t); size != RunLogMaxBytes {
			t.Fatalf("log is %d bytes after trimming %q, want exactly the %d cap", size, filler, RunLogMaxBytes)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
	}

	if _, err := os.Stat(LogPath() + ".2"); !os.IsNotExist(err) {
		t.Fatalf("only one previous generation may survive, stat err = %v", err)
	}
	previous, err := os.ReadFile(LogPath() + ".1")
	if err != nil {
		t.Fatalf("previous generation: %v", err)
	}
	if int64(len(previous)) > RunLogMaxBytes {
		t.Fatalf("previous generation = %d bytes; it must not accumulate", len(previous))
	}
}

// A failed bind is only ever in the tail: the marker the daemon logged on its
// way out cannot fall off the front of a window this size, while a stale match
// from an earlier run must not decide the outcome of this one.
func TestBindFailureSeenReadsOnlyTheTail(t *testing.T) {
	isolate(t)
	noise := strings.Repeat("noise\n", int(startupLogScanBytes))

	writeLog(t, "startup chatter\n"+noise+string(bindFailureMarkers[0]))
	if !bindFailureSeen() {
		t.Fatalf("a bind failure at the end of the log must be seen")
	}

	writeLog(t, string(bindFailureMarkers[0])+noise)
	if bindFailureSeen() {
		t.Fatalf("a marker older than the window must not decide this start")
	}

	writeLog(t, "startup chatter\n")
	if bindFailureSeen() {
		t.Fatalf("a clean log must not report a bind failure")
	}

	if err := os.Remove(LogPath()); err != nil {
		t.Fatalf("remove log: %v", err)
	}
	if bindFailureSeen() {
		t.Fatalf("a missing log must not report a bind failure")
	}
}

// paddedLine is one line of the wide test log: the number left-aligned in a
// fixed width, so the log has a known size without a random payload.
func paddedLine(i, width int) string {
	return strconv.Itoa(i) + strings.Repeat("x", width-len(strconv.Itoa(i)))
}

func writeLog(t *testing.T, content string) {
	t.Helper()
	if err := os.MkdirAll(Dir(), 0755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}
	if err := os.WriteFile(LogPath(), []byte(content), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

// appendLog builds a log of exactly size bytes that ends with line. The filler is
// real log lines rather than raw padding: `logs` reads by line, so a fixture with
// no newlines would not exercise the path a user actually hits.
func appendLog(t *testing.T, line string, size int64) {
	t.Helper()
	if err := os.MkdirAll(Dir(), 0755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}
	filler := int(size) - len(line)
	if filler < 0 {
		t.Fatalf("size %d is smaller than the marker", size)
	}
	var b strings.Builder
	b.Grow(filler + len(line))
	for b.Len() < filler-1 {
		b.WriteString("filler line\n")
	}
	for b.Len() < filler {
		b.WriteByte('x')
	}
	b.WriteString(line)
	if err := os.WriteFile(LogPath(), []byte(b.String()), 0644); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

func mustSize(t *testing.T) int64 {
	t.Helper()
	fi, err := os.Stat(LogPath())
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	return fi.Size()
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
