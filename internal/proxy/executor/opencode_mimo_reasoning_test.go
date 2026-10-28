package executor

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/providers"
)

func TestOpencodeMimoThinkingLevels(t *testing.T) {
	full := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	if got := providers.GetThinkingLevels("opencode", "mimo-v2.6-flash-free"); !equalStrings(got, full) {
		t.Errorf("opencode mimo levels = %v, want the full OpenAI enum", got)
	}
	// The unqualified *mimo*v2.6* row still describes Xiaomi's own API
	// (thinking can never be disabled there, so "none" is stripped).
	if got := providers.GetThinkingLevels("xiaomi", "mimo-v2.6-flash"); !equalStrings(got, []string{"low", "medium", "high", "xhigh"}) {
		t.Errorf("xiaomi mimo levels = %v, want the generic set", got)
	}
}

func TestOpencodeMimoCapabilities(t *testing.T) {
	caps := providers.GetCapabilitiesForModel("opencode", "mimo-v2.6-flash-free")
	if !caps.Vision || !caps.AudioInput || !caps.VideoInput || !caps.Reasoning {
		t.Errorf("multimodal reasoning caps missing: %+v", caps)
	}
	if caps.ThinkingFormat != "openai" || caps.ThinkingCanDisable == nil || !*caps.ThinkingCanDisable {
		t.Errorf("thinking control = %q/%v, want openai + can-disable", caps.ThinkingFormat, caps.ThinkingCanDisable)
	}
	if caps.ContextWindow != 1048576 || caps.MaxOutput != 131072 {
		t.Errorf("limits = %d/%d, want 1048576/131072", caps.ContextWindow, caps.MaxOutput)
	}
	// The lane override must not leak into Xiaomi's own API shape.
	xcaps := providers.GetCapabilitiesForModel("xiaomi", "mimo-v2.6-flash")
	if xcaps.ThinkingFormat != "deepseek" {
		t.Errorf("xiaomi mimo format = %q, want deepseek", xcaps.ThinkingFormat)
	}
}

// The free backend validates reasoning_effort against the plain OpenAI enum
// and 400s "auto"; adaptive thinking arrives as auto, so the field is dropped
// while explicit efforts pass through.
func TestForwardOpencode_DropsAdaptiveReasoningEffort(t *testing.T) {
	var seen map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		seen = nil
		_ = json.Unmarshal(data, &seen)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	send := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := &Request{
			Client: srv.Client(),
			Config: &providers.ProviderConfig{BaseURL: srv.URL},
			APIKey: "public",
			Body:   []byte(body),
		}
		if err := ForwardOpencode(rec, req); err != nil {
			t.Fatalf("ForwardOpencode: %v", err)
		}
	}

	send(`{"model":"oc/mimo-v2.6-flash-free","reasoning_effort":"auto","messages":[{"role":"user","content":"hi"}]}`)
	if seen == nil {
		t.Fatal("upstream saw no body")
	}
	if _, ok := seen["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort auto not dropped: %v", seen["reasoning_effort"])
	}

	send(`{"model":"oc/mimo-v2.6-flash-free","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	if got, _ := seen["reasoning_effort"].(string); got != "high" {
		t.Errorf("explicit effort dropped: %v", seen["reasoning_effort"])
	}
}

func TestLiftReasoningAliasesPayload(t *testing.T) {
	// Alias-only delta gains reasoning_content, aliases stay.
	out := liftReasoningAliasesPayload([]byte(`{"choices":[{"index":0,"delta":{"reasoning":"thinking..."}}]}`))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("lifted payload not JSON: %v", err)
	}
	choices := m["choices"].([]any)
	delta := choices[0].(map[string]any)["delta"].(map[string]any)
	if delta["reasoning_content"] != "thinking..." {
		t.Errorf("reasoning_content not lifted: %v", delta)
	}
	if delta["reasoning"] != "thinking..." {
		t.Errorf("alias must stay intact: %v", delta)
	}

	// reasoning_details[] entries join; bare strings and text/content objects.
	out = liftReasoningAliasesPayload([]byte(`{"choices":[{"index":0,"delta":{"reasoning_details":[{"text":"a"},{"content":"b"},"c"]}}]}`))
	_ = json.Unmarshal(out, &m)
	delta = m["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["reasoning_content"] != "abc" {
		t.Errorf("details lift = %v, want abc", delta["reasoning_content"])
	}

	// A delta that already carries reasoning_content is untouched; plain
	// content chunks skip parsing entirely.
	for _, p := range []string{
		`{"choices":[{"index":0,"delta":{"reasoning_content":"kept","reasoning":"x"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"hello \"reasoning\" world"}}]}`,
		`{"choices":[{"index":0,"delta":{}}]}`,
		`not json`,
	} {
		if got := liftReasoningAliasesPayload([]byte(p)); string(got) != p {
			t.Errorf("payload changed: %s -> %s", p, got)
		}
	}
}

func TestSSEToOpenAIJSON_KeepsAliasReasoning(t *testing.T) {
	raw := "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"think\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"text\":\" hard\"}]}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	out, ok := sseToOpenAIJSON([]byte(raw))
	if !ok {
		t.Fatal("aggregation failed")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("aggregated output not JSON: %v", err)
	}
	choices := m["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["reasoning_content"] != "think hard" {
		t.Errorf("aggregated reasoning = %v, want \"think hard\"", msg["reasoning_content"])
	}
	if msg["content"] != "answer" {
		t.Errorf("aggregated content = %v", msg["content"])
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
