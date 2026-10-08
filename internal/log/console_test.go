package log

import (
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"
)

func TestCaptureConsole_BuffersAndClears(t *testing.T) {
	ClearConsoleLogs()

	Info("test", "first line")
	Info("test", "second line")

	entries := ConsoleEntries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 buffered entries, got %d", len(entries))
	}
	if !contains(entries, "first line") || !contains(entries, "second line") {
		t.Errorf("buffered entries missing content: %v", entries)
	}

	ClearConsoleLogs()
	if got := len(ConsoleEntries()); got != 0 {
		t.Errorf("expected empty buffer after clear, got %d entries", got)
	}
}

func TestCaptureConsole_RingBufferBounded(t *testing.T) {
	ClearConsoleLogs()
	for range consoleMaxLines + 50 {
		Info("test", "line")
	}
	if got := len(ConsoleEntries()); got > consoleMaxLines {
		t.Errorf("ring buffer exceeded cap: %d > %d", got, consoleMaxLines)
	}
}

// A level the emitter reported must reach the dashboard unchanged: the whole
// point of the typed entry is that the UI stops guessing from rendered text.
func TestCaptureConsole_RecordsEmitterLevel(t *testing.T) {
	ClearConsoleLogs()

	SetLevel(LevelDebug)
	defer SetLevel(LevelInfo)
	Debug("test", "a debug line")
	Info("test", "an info line")
	Warn("test", "a warn line")
	Error("test", "an error line")

	entries := ConsoleEntries()
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}
	want := []Level{LevelDebug, LevelInfo, LevelWarn, LevelError}
	for i, lvl := range want {
		if entries[i].Level != lvl {
			t.Errorf("entry %d level = %s, want %s", i, entries[i].Level, lvl)
		}
	}
}

// The text format prints no timestamp of its own, so arrival time is the only
// thing that keeps rows distinguishable once the buffer is scrolled.
func TestCaptureConsole_StampsArrivalTime(t *testing.T) {
	ClearConsoleLogs()

	before := time.Now()
	Info("test", "stamped line")
	entries := ConsoleEntries()

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Time.Before(before) {
		t.Errorf("entry time %s predates the call at %s", entries[0].Time, before)
	}
}

func TestCaptureConsole_StripsANSI(t *testing.T) {
	ClearConsoleLogs()

	captureConsole(LevelInfo, "\x1b[32mINF\x1b[0m [request] GET / status=200")

	entries := ConsoleEntries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 buffered entry, got %d", len(entries))
	}
	if entries[0].Line != "INF [request] GET / status=200" {
		t.Errorf("expected ANSI codes stripped, got %q", entries[0].Line)
	}
	if entries[0].Level != LevelInfo {
		t.Errorf("level = %s, want info", entries[0].Level)
	}
}

// The dashboard colours, filters and counts by this exact field set, so a
// rename here silently downgrades every row to an unlabelled default.
func TestConsoleEntry_JSONWireShape(t *testing.T) {
	entry := ConsoleEntry{
		Time:  time.Date(2026, 10, 3, 7, 8, 9, 0, time.UTC),
		Level: LevelWarn,
		Line:  `WRN [proxy] quota "nearing"`,
	}

	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got struct {
		Time  string `json:"time"`
		Level string `json:"level"`
		Line  string `json:"line"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Level != "warn" {
		t.Errorf("level = %q, want warn", got.Level)
	}
	if got.Line != entry.Line {
		t.Errorf("line = %q, want %q", got.Line, entry.Line)
	}
	if got.Time != "2026-10-03T07:08:09Z" {
		t.Errorf("time = %q, want RFC3339 UTC stamp", got.Time)
	}
}

func TestSubscribeConsole_DeliversLinesAndClear(t *testing.T) {
	ClearConsoleLogs()

	ch, cancel := SubscribeConsole()
	defer cancel()

	Warn("test", "hello from sub")

	select {
	case ev := <-ch:
		if ev.Kind() != "line" || !contains([]ConsoleEntry{ev.Entry()}, "hello from sub") {
			t.Errorf("expected line event, got kind=%q entry=%q", ev.Kind(), ev.Entry().Line)
		}
		if ev.Entry().Level != LevelWarn {
			t.Errorf("subscriber level = %s, want warn", ev.Entry().Level)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for line event")
	}

	ClearConsoleLogs()
	select {
	case ev := <-ch:
		if ev.Kind() != "clear" {
			t.Errorf("expected clear event, got kind=%q", ev.Kind())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for clear event")
	}
}

func contains(entries []ConsoleEntry, needle string) bool {
	for _, e := range entries {
		if strings.Contains(e.Line, needle) {
			return true
		}
	}
	return false
}
