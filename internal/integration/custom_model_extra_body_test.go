//go:build integration

package integration

import (
	"net/http"
	"testing"

	json "encoding/json/v2"
)

// An operator-declared extraBody on an OpenRouter custom model reaches the
// upstream merged into the client's body, never changes the model, and never
// leaks to a sibling provider serving the same model id.
func TestCustomModelExtraBodyReachesOpenRouterOnly(t *testing.T) {
	env := newEnv(t)
	openrouter := env.NewUpstream(t, chatCompletionResponder())
	sibling := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-openrouter", "openrouter", "OpenRouter", openrouter, "sk-or")
	env.AddConnection(t, "conn-openai", "openai", "OpenAI", sibling, "sk-oa")

	const model = "vendor/model-x:free"
	extraBody := map[string]any{
		"temperature": 0.9,
		"reasoning":   map[string]any{"max_tokens": 64},
	}
	save := env.Post(t, "/api/models/custom", map[string]any{
		"key":   "openrouter|" + model + "|llm",
		"value": map[string]any{"providerAlias": "openrouter", "id": model, "type": "llm", "extraBody": extraBody},
	})
	if save.Status != http.StatusOK {
		t.Fatalf("save openrouter custom model = %d: %s", save.Status, truncate(save.Body))
	}
	// The dashboard refuses extraBody for any other provider, so the sibling
	// row is seeded the way an imported backup would carry it.
	siblingRow, err := json.Marshal(map[string]any{"providerAlias": "openai", "id": model, "type": "llm", "extraBody": extraBody})
	if err != nil {
		t.Fatalf("marshal sibling row: %v", err)
	}
	if err := env.Repo.SetKV("customModels", "openai|"+model+"|llm", string(siblingRow)); err != nil {
		t.Fatalf("seed sibling row: %v", err)
	}
	env.AddCombo(t, "combo-extra", "extra-combo", []string{"openrouter/" + model})

	type sent struct {
		Model       string         `json:"model"`
		Temperature *float64       `json:"temperature"`
		Reasoning   map[string]any `json:"reasoning"`
	}
	decode := func(t *testing.T, req RecordedRequest) sent {
		t.Helper()
		var s sent
		if err := json.Unmarshal(req.Body, &s); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		return s
	}

	// Each case sends exactly one upstream request, so request i belongs to
	// case i: the combo case cannot pass on the direct case's capture.
	for i, requested := range []string{"openrouter/" + model, "extra-combo"} {
		t.Run(requested, func(t *testing.T) {
			body := ChatBody(requested, false)
			body["temperature"] = 0.2
			res := env.Post(t, "/v1/chat/completions", body)
			if res.Status != http.StatusOK {
				t.Fatalf("status = %d: %s", res.Status, truncate(res.Body))
			}
			reqs := openrouter.Requests()
			if len(reqs) != i+1 {
				t.Fatalf("openrouter upstream requests = %d, want %d", len(reqs), i+1)
			}
			got := decode(t, reqs[i])
			if got.Model != model {
				t.Errorf("upstream model = %q, want %q", got.Model, model)
			}
			if got.Temperature == nil || *got.Temperature != 0.9 {
				t.Errorf("temperature = %v, want operator 0.9 over client 0.2", got.Temperature)
			}
			if got.Reasoning["max_tokens"] != float64(64) {
				t.Errorf("reasoning = %v, want the operator's", got.Reasoning)
			}
		})
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("openai/"+model, false))
	if res.Status != http.StatusOK {
		t.Fatalf("sibling status = %d: %s", res.Status, truncate(res.Body))
	}
	if n := sibling.Count(); n != 1 {
		t.Fatalf("sibling upstream requests = %d, want 1", n)
	}
	if got := decode(t, sibling.Last(t)); got.Temperature != nil || got.Reasoning != nil {
		t.Errorf("sibling provider received extraBody: %+v", got)
	}
	if n := openrouter.Count(); n != 2 {
		t.Errorf("openrouter upstream requests = %d after sibling call, want 2", n)
	}
}
