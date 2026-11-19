//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/samber/lo"
)

// The reported case: nous/qwen4-max advertised context_length 131072 while
// the provider's own /v1/models declares 1000000. Two gaps produced it — the
// qwen pattern rows carried no window (the substring table's 131072/8192
// floor won), and the import UI read only snake_case limit keys, dropping the
// camelCase contextLength/maxOutputTokens the normalized nous catalogue
// emits. This pins the end state on the real router: the family window
// publishes without any declaration, and an imported row that carries the
// provider's live numbers outranks every table.
func TestNousQwen4MaxPublishesRealWindow(t *testing.T) {
	env := newEnv(t)

	// The connection points at a fake upstream (never the real provider). Its
	// /models feed answers 404 here, so the live resolver falls back to the
	// static catalog plus the custom rows below — the imported-row path.
	upstream := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			http.Error(w, "no catalog", http.StatusNotFound)
			return
		}
		chatCompletionResponder()(w, r)
	})
	env.AddConnection(t, "conn-nous", "nous", "Nous", upstream, "sk-nous")

	// The row the fixed import writes: the live catalogue's own limits,
	// persisted as the operator-visible declaration.
	imported := `{"providerAlias":"nous","id":"qwen/qwen4-max","type":"llm","name":"Qwen4 Max","contextWindow":1000000,"maxOutput":131072}`
	if err := env.Repo.SetKV("customModels", "nous|qwen/qwen4-max|llm", imported); err != nil {
		t.Fatalf("save imported custom model: %v", err)
	}
	// A sibling with no declaration must still publish the family window,
	// not the 131k name guess.
	bare := `{"providerAlias":"nous","id":"qwen/qwen3.8-max-prime","type":"llm","name":"Qwen3.8 Max Prime"}`
	if err := env.Repo.SetKV("customModels", "nous|qwen/qwen3.8-max-prime|llm", bare); err != nil {
		t.Fatalf("save bare custom model: %v", err)
	}

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
		t.Errorf("context_length = %v, want 1000000 (the provider's declared window)", entry.ContextLength)
	}
	if entry.MaxCompletionTokens == nil || *entry.MaxCompletionTokens != 131072 {
		t.Errorf("max_completion_tokens = %v, want 131072 (top_provider.max_completion_tokens)", entry.MaxCompletionTokens)
	}

	sibling, ok := lo.Find(catalog.Data, func(e modelEntry) bool { return e.ID == "nous/qwen/qwen3.8-max-prime" })
	if !ok {
		t.Fatalf("nous/qwen/qwen3.8-max-prime absent from /v1/models; got %v", catalog.ids())
	}
	if sibling.ContextLength == nil || *sibling.ContextLength != 1000000 {
		t.Errorf("undeclared sibling context_length = %v, want 1000000 (upstream *qwen*max* window)", sibling.ContextLength)
	}
	if sibling.MaxCompletionTokens == nil || *sibling.MaxCompletionTokens != 65536 {
		t.Errorf("undeclared sibling max_completion_tokens = %v, want 65536 (upstream *qwen*max* output)", sibling.MaxCompletionTokens)
	}
}
