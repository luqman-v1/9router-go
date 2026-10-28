package headroom

// Live probe against a real Headroom proxy. Skipped unless NINEROUTER_HEADROOM_LIVE
// is set, because it needs a proxy process the test cannot start.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestLiveCompressAgainstRealProxy(t *testing.T) {
	url := os.Getenv("NINEROUTER_HEADROOM_LIVE")
	if url == "" {
		t.Skip("set NINEROUTER_HEADROOM_LIVE=http://127.0.0.1:8787 to run")
	}
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = fmt.Sprintf("2026-10-21 00:%02d:%02d INFO worker[%d] processed batch item=%d status=ok duration=%dms",
			i%60, (i*7)%60, i%8, i, 10+i%40)
	}
	body, _ := json.Marshal(map[string]any{
		"model":    "gpt-4o",
		"messages": []any{map[string]any{"role": "user", "content": "Here is a long build log:\n" + strings.Join(lines, "\n") + "\nWhat failed?"}},
	})

	c := NewClient(url, "", nil)
	out, compressed, err := c.Compress(context.Background(), body, "gpt-4o", 8000)
	t.Logf("in=%d out=%d compressed=%v err=%v", len(body), len(out), compressed, err)
	if err != nil {
		t.Fatalf("real proxy returned an error: %v", err)
	}
	if !compressed {
		t.Fatalf("expected the log body to compress; got no change (in=%d)", len(body))
	}
	if len(out) >= len(body) {
		t.Fatalf("bytes did not shrink: in=%d out=%d", len(body), len(out))
	}

	// The guard must not have removed the user turn.
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("compressed body is not JSON: %v", err)
	}
	msgs, _ := m["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("message count changed: %d", len(msgs))
	}
}

func TestLiveGuardStopsRealProxy(t *testing.T) {
	url := os.Getenv("NINEROUTER_HEADROOM_LIVE")
	if url == "" {
		t.Skip("set NINEROUTER_HEADROOM_LIVE to run")
	}
	// Same long log, but carried by a Read tool result. If the guard works the
	// proxy is never contacted; the body comes back byte-identical.
	log := strings.Repeat("INFO worker processed batch item status=ok duration=10ms\n", 200)
	body, _ := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": "Read", "arguments": "{}"}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "c1", "content": log},
		},
	})

	c := NewClient(url, "", nil)
	out, compressed, err := c.Compress(context.Background(), body, "gpt-4o", 8000)
	if err != nil {
		t.Fatalf("guard must fail open: %v", err)
	}
	if compressed {
		t.Fatalf("a Read result was compressed by the real proxy: in=%d out=%d", len(body), len(out))
	}
	if string(out) != string(body) {
		t.Fatalf("body changed despite the skip")
	}
	t.Logf("guard held against the real proxy: %d bytes untouched", len(body))
}
