package log

import (
	"testing"
	"time"
)

func TestCaptureConsole_BuffersAndClears(t *testing.T) {
	ClearConsoleLogs()

	Info("test", "first line")
	Info("test", "second line")

	logs := ConsoleLogs()
	if len(logs) != 2 {
		t.Fatalf("expected 2 buffered lines, got %d", len(logs))
	}
	if !contains(logs, "first line") || !contains(logs, "second line") {
		t.Errorf("buffered lines missing content: %v", logs)
	}

	ClearConsoleLogs()
	if got := len(ConsoleLogs()); got != 0 {
		t.Errorf("expected empty buffer after clear, got %d lines", got)
	}
}

func TestCaptureConsole_RingBufferBounded(t *testing.T) {
	ClearConsoleLogs()
	for range consoleMaxLines + 50 {
		Info("test", "line")
	}
	logs := ConsoleLogs()
	if len(logs) > consoleMaxLines {
		t.Errorf("ring buffer exceeded cap: %d > %d", len(logs), consoleMaxLines)
	}
}

func TestSubscribeConsole_DeliversLinesAndClear(t *testing.T) {
	ClearConsoleLogs()

	ch, cancel := SubscribeConsole()
	defer cancel()

	Info("test", "hello from sub")

	select {
	case ev := <-ch:
		if ev.Kind() != "line" || !contains([]string{ev.Line()}, "hello from sub") {
			t.Errorf("expected line event, got kind=%q line=%q", ev.Kind(), ev.Line())
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

func contains(lines []string, needle string) bool {
	for _, l := range lines {
		if len(l) >= len(needle) && l[len(l)-len(needle):] == needle {
			return true
		}
	}
	return false
}
