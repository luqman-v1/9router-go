package proxy

import (
	"strings"
	"testing"
)

// End-to-end: a passthrough relay with the reasoning-alias patcher (the
// opencode quirk) reaches the client with reasoning_content added, so
// reasoning-only chunks survive the value checks downstream.
func TestSSECopy_LiftsReasoningAliases(t *testing.T) {
	rec := &mockResponseWriter{}
	upstream := strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"step 1\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n")
	if err := SSECopy(rec, upstream, rec, nil, func(p []byte) []byte {
		if strings.Contains(string(p), `"reasoning":`) {
			return []byte(strings.Replace(string(p), `"reasoning":"step 1"`, `"reasoning":"step 1","reasoning_content":"step 1"`, 1))
		}
		return p
	}); err != nil {
		t.Fatalf("SSECopy: %v", err)
	}
	out := rec.String()
	if !strings.Contains(out, `"reasoning_content":"step 1"`) {
		t.Errorf("patched payload not relayed: %s", out)
	}
	if !strings.Contains(out, `"content":"answer"`) {
		t.Errorf("content frame lost: %s", out)
	}
}
