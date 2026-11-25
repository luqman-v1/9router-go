//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/samber/lo"
)

// The provider's own /v1/models feed is the authoritative source for a context
// window. nous declares context_length 1000000 for qwen/qwen4-max; the gateway
// must publish that number on /v1/models without any import or custom row —
// the live feed replaces the static catalog, exactly like the kiro/grok-cli
// resolvers. Before the models-feed resolver existed, nous fell through to the
// static registry and the substring table's 131072 guess.
func TestNousLiveFeedPublishesProviderWindow(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[
				{"id":"qwen/qwen4-max","name":"Qwen4 Max","context_length":1000000,"top_provider":{"context_length":1000000,"max_completion_tokens":131072}},
				{"id":"qwen/qwen3.8-flash","name":"Qwen3.8 Flash","context_length":1000000,"top_provider":{"max_completion_tokens":131072}}
			]}`))
			return
		}
		chatCompletionResponder()(w, r)
	})
	env.AddConnection(t, "conn-nous-feed", "nous", "Nous Feed", upstream, "sk-nous")

	listed := env.Get(t, "/v1/models")
	if listed.Status != http.StatusOK {
		t.Fatalf("/v1/models status = %d: %s", listed.Status, truncate(listed.Body))
	}
	var catalog modelsResponse
	listed.Decode(t, &catalog)

	entry, ok := lo.Find(catalog.Data, func(e modelEntry) bool { return e.ID == "nous/qwen/qwen4-max" })
	if !ok {
		t.Fatalf("nous/qwen/qwen4-max absent from /v1/models; got %v", catalog.ids())
	}
	if entry.ContextLength == nil || *entry.ContextLength != 1000000 {
		t.Errorf("context_length = %v, want 1000000 (the provider feed's own declaration)", entry.ContextLength)
	}
	if entry.MaxCompletionTokens == nil || *entry.MaxCompletionTokens != 131072 {
		t.Errorf("max_completion_tokens = %v, want 131072 (top_provider.max_completion_tokens)", entry.MaxCompletionTokens)
	}

	// The feed replaced the static catalog: a model the registry never listed
	// is discoverable because the provider serves it.
	if !lo.Contains(catalog.ids(), "nous/qwen/qwen3.8-flash") {
		t.Errorf("model list = %v, want the feed's qwen3.8-flash too", catalog.ids())
	}
}
