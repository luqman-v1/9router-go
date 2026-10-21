//go:build integration

package integration

import (
	"net/http"
	"strings"
	"testing"
)

// TestMuseResponsesLaneClampsDisabledReasoning pins the mandatory-reasoning
// clamp on a Responses-native lane. The muse provider never runs ApplyJSON —
// its executor does the level mapping — so an explicit disable used to reach
// the upstream untouched, and a provider that mandates reasoning answered 400
// ("Reasoning is mandatory for this endpoint and cannot be disabled"). The
// clamp has to happen at the dispatch chokepoint, before the executor runs.
func TestMuseResponsesLaneClampsDisabledReasoning(t *testing.T) {
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

	body := ChatBody("muse/muse-spark-1.3-contributor", false)
	body["reasoning_effort"] = "none"
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := up.Last(t)
	if !strings.Contains(string(sent.Body), "minimal") {
		t.Errorf("the upstream must receive the clamped level, got %s", truncate(sent.Body))
	}
}
