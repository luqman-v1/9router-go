//go:build integration

package integration

import (
	"net/http"
	"testing"

	json "encoding/json/v2"
)

// museUpstream is the fake a muse connection answers with: a Responses-style
// event stream, because a chat-completions client is served the translated
// stream.
func museUpstream(t *testing.T, env *Env) *Upstream {
	t.Helper()
	up := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	})
	priority := 1
	data := `{"apiKey":"sk-muse","baseUrl":"` + up.URL + `/chat/completions"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-muse", "muse", "apikey", "Muse", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	return up
}

// TestMuseResponsesLaneClampsDisabledReasoning pins the mandatory-reasoning
// clamp on a Responses-native lane. The muse provider never runs ApplyJSON —
// its executor does the level mapping — so an explicit disable used to reach
// the upstream untouched, and a provider that mandates reasoning answered 400
// ("Reasoning is mandatory for this endpoint and cannot be disabled"). The
// clamp has to happen at the dispatch chokepoint, before the executor runs.
func TestMuseResponsesLaneClampsDisabledReasoning(t *testing.T) {
	env := newEnv(t)
	up := museUpstream(t, env)

	body := ChatBody("muse/muse-spark-1.3-contributor", false)
	body["reasoning_effort"] = "none"
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := up.Last(t)
	var wire map[string]any
	if err := json.Unmarshal(sent.Body, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	reasoning, _ := wire["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "minimal" {
		t.Fatalf("the upstream must receive the clamped level, got %s", truncate(sent.Body))
	}
	if _, has := wire["reasoning_effort"]; has {
		t.Fatalf("the disable dialect must not survive next to the clamped one: %s", truncate(sent.Body))
	}
}

// TestMuseResponsesLaneClampsTheResponsesDialectDisable pins the same clamp
// for the Responses dialect: a client that disables reasoning with
// reasoning:{effort:"none"} is clamped the same way a client that disables
// it with reasoning_effort is.
func TestMuseResponsesLaneClampsTheResponsesDialectDisable(t *testing.T) {
	env := newEnv(t)
	up := museUpstream(t, env)

	body := ChatBody("muse/muse-spark-1.3-contributor", false)
	body["reasoning"] = map[string]any{"effort": "none"}
	res := env.Post(t, "/v1/chat/completions", body)
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := up.Last(t)
	var wire map[string]any
	if err := json.Unmarshal(sent.Body, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	reasoning, _ := wire["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "minimal" {
		t.Fatalf("the Responses dialect must be clamped too, got %s", truncate(sent.Body))
	}
	if _, has := wire["reasoning_effort"]; has {
		t.Fatalf("the disable dialect must not survive next to the clamped one: %s", truncate(sent.Body))
	}
}

// TestMuseResponsesLaneAppliesTheModelSuffix pins the suffix on a
// Responses-native lane: the named effort must reach the upstream instead of
// the upstream default. The suffix is a request override that never enters
// the body, so a lane that skips the thinking pass drops it silently.
func TestMuseResponsesLaneAppliesTheModelSuffix(t *testing.T) {
	env := newEnv(t)
	up := museUpstream(t, env)

	res := env.Post(t, "/v1/chat/completions", ChatBody("muse/muse-spark-1.3-contributor(high)", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	sent := up.Last(t)
	if got := sent.Model(t); got != "muse-spark-1.3-contributor" {
		t.Errorf("upstream model = %q, want the suffix-free id", got)
	}
	var wire map[string]any
	if err := json.Unmarshal(sent.Body, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	reasoning, _ := wire["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != "high" {
		t.Errorf("the suffix effort did not reach the upstream: %s", truncate(sent.Body))
	}
}
