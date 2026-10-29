//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// TestClaudeDisabledThinkingReachesUpstreamAsMinimal pins the Claude disable
// dialect across the request conversion. A /v1/messages client asking for no
// reasoning must keep that intent through the Claude→OpenAI conversion, so a
// provider that mandates reasoning clamps it to minimal — instead of the
// request silently asking for the provider default.
func TestClaudeDisabledThinkingReachesUpstreamAsMinimal(t *testing.T) {
	env := newEnv(t)
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// A chat-completions client is served the translated stream, so the
		// answer has to arrive as the delta events a real turn emits.
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	})
	priority := 1
	data := `{"apiKey":"sk-muse","baseUrl":"` + up.URL + `/chat/completions"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-muse", "muse", "apikey", "Muse", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}

	body := claudeMessagesBody("muse/muse-spark-1.3-contributor", false)
	body["thinking"] = map[string]any{"type": "disabled"}
	res := env.Post(t, "/v1/messages", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := up.Last(t)
	if !strings.Contains(string(sent.Body), "minimal") {
		t.Errorf("the upstream must receive the clamped level, got %s", truncate(sent.Body))
	}
}
