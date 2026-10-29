//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// inbandErrorChunk mimics a provider relaying its dead downstream inside the
// event stream on HTTP 200: an empty choices array plus an error object, the
// exact shape OpenRouter emitted for Stealth (`provider_unavailable`) that
// surfaced to operators as "502 JSON error injected into SSE stream".
const inbandErrorChunk = `data: {"id":"gen-sick-1","object":"chat.completion.chunk","created":1791132819,"model":"sick-model","provider":"Stealth","choices":[],"error":{"code":502,"message":"sick downstream unavailable","metadata":{"error_type":"provider_unavailable"}}}` + "\n\n"

// sickStreamResponder answers 200 with only the provider error event, then
// closes. No content, no terminal, no DONE: the turn never had an answer.
func sickStreamResponder() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(inbandErrorChunk))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// TestComboSkipsInbandErrorOnRetry pins the smarter-combo contract: a member
// whose upstream injects an error event into the SSE stream must not read as
// a served success. The client sees the gateway's own error frame, the sick
// connection is locked, and the immediate retry is served cleanly by the next
// member. Round-robin is preserved: rotation continues, it just skips the
// locked connection while its cooldown holds.
//
// The contract holds with a model response timeout configured too: an in-band
// error commits the candidate before the timer fires, and that commit must
// still reach the normal lock-and-fail-over path (PR #750 review).
//
// Note: headers are already committed when a mid-stream error is detected, so
// the combo loop may still append the next member's answer after the error
// frame in the first response (pre-existing post-commit behavior, same as a
// mid-stream socket reset). The pinned contract is narrower and stable: the
// first turn carries a parseable error frame instead of a silent success, and
// the retry carries no error frame at all.
func TestComboSkipsInbandErrorOnRetry(t *testing.T) {
	cases := []struct {
		name      string
		timeoutMs int
	}{
		{name: "response timeout off"},
		{name: "response timeout on", timeoutMs: 30000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertComboSkipsInbandErrorOnRetry(t, tc.timeoutMs)
		})
	}
}

func assertComboSkipsInbandErrorOnRetry(t *testing.T, timeoutMs int) {
	env := newEnv(t)

	sick := env.NewUpstream(t, sickStreamResponder())
	well := env.NewUpstream(t, streamResponder())

	env.AddConnection(t, "conn-sick", "deepseek", "Sick Account", sick, "sk-sick")
	env.AddConnection(t, "conn-well", "groq", "Well Account", well, "sk-well")
	env.AddCombo(t, "combo-1", "wombo", []string{"deepseek/sick-model", "groq/good-model"})
	if timeoutMs > 0 {
		if err := env.Repo.UpdateSettingsRaw(map[string]any{"comboStrategies": map[string]any{
			"wombo": map[string]any{"firstTokenTimeoutMs": timeoutMs},
		}}); err != nil {
			t.Fatalf("seed combo response timeout: %v", err)
		}
	}

	first := env.Post(t, "/v1/chat/completions", ChatBody("wombo", true))
	if first.Status != http.StatusOK {
		t.Fatalf("first POST /v1/chat/completions = %d, want 200 with an in-band error frame (body: %s)",
			first.Status, truncate(first.Body))
	}
	firstBody := string(first.Body)
	if !strings.Contains(firstBody, `"code":"upstream_error"`) {
		t.Errorf("first stream = %q, want the gateway error frame, not a silent success", truncate(first.Body))
	}
	for _, raw := range []string{`"provider":"Stealth"`, "provider_unavailable"} {
		if strings.Contains(firstBody, raw) {
			t.Errorf("first stream relays the raw provider chunk %q, want it swallowed: %q", raw, truncate(first.Body))
		}
	}
	if got := sick.Count(); got != 1 {
		t.Fatalf("sick upstream received %d requests, want 1", got)
	}

	// The immediate retry must not touch the sick connection again: no error
	// frame, just the healthy member's answer.
	second := env.Post(t, "/v1/chat/completions", ChatBody("wombo", true))
	if second.Status != http.StatusOK {
		t.Fatalf("retry POST /v1/chat/completions = %d, want 200 from the healthy member (body: %s)",
			second.Status, truncate(second.Body))
	}
	secondBody := string(second.Body)
	if !strings.Contains(secondBody, "streamed ") {
		t.Errorf("retry stream missing the healthy member content: %q", truncate(second.Body))
	}
	if strings.Contains(secondBody, `"code":"upstream_error"`) {
		t.Errorf("retry stream carries an error frame, want the sick member locked out: %q", truncate(second.Body))
	}
	if got := sick.Count(); got != 1 {
		t.Errorf("sick upstream received %d requests after the retry, want still 1 (locked out)", got)
	}
	if got := well.Last(t).Model(t); got != "good-model" {
		t.Errorf("healthy upstream model = %q, want the combo's second member model", got)
	}
}
