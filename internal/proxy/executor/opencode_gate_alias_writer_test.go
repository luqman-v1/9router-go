package executor

import (
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/translator"
)

func TestOpenCodeGateAliasWriter_StreamingFragments(t *testing.T) {
	aliases := translator.OpenCodeGateAliases{
		ToolNames: map[string]string{"run_command": "terminal"},
		ParamKeys: map[string]string{"old_text": "old_string", "new_text": "new_string"},
	}
	rec := httptest.NewRecorder()
	w := NewOpenCodeGateAliasWriter(rec, aliases)
	frames := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"run_command\",\"arguments\":\"{\\\"old_\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"text\\\":\\\"a\\\",\\\"new_text\\\":\\\"b\\\"}\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"finish_reason\":\"tool_calls\",\"delta\":{}}]}\n\n",
		"data: [DONE]\n\n",
	}
	for _, frame := range frames {
		if _, err := w.Write([]byte(frame)); err != nil {
			t.Fatal(err)
		}
	}
	out := rec.Body.String()
	if !strings.Contains(out, `\"old_string\":\"a\"`) || !strings.Contains(out, `\"new_string\":\"b\"`) {
		t.Errorf("fragmented argument keys not restored: %s", out)
	}
	if !strings.Contains(out, `"name":"terminal"`) {
		t.Errorf("tool name not restored: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("sentinel lost: %s", out)
	}
	// Two argument fragments are replayed as one complete call, followed by
	// the finish frame and the sentinel.
	if strings.Count(out, "data:") != 3 {
		t.Errorf("frame count = %d, want 3: %s", strings.Count(out, "data:"), out)
	}
}

func TestOpenCodeGateAliasWriter_NonStream(t *testing.T) {
	aliases := translator.OpenCodeGateAliases{ParamKeys: map[string]string{"old_text": "old_string"}}
	rec := httptest.NewRecorder()
	w := NewOpenCodeGateAliasWriter(rec, aliases)
	payload := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"arguments":"{\"old_text\":\"x\"}"}}]}}]}`)
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if got := rec.Body.String(); !strings.Contains(got, `\"old_string\":\"x\"`) {
		t.Errorf("non-stream arguments not restored: %s", got)
	}
}
