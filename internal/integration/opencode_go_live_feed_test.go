//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/samber/lo"
)

// OpenCode Zen and Go publish their catalogs as OpenAI-style feeds whose limits
// live under limit.context / limit.output (the models.dev shape), not
// context_length. The live-feed resolver must read that shape and publish the
// provider's own window — the go lane's hy4-preview is 1024000 on the feed while
// the static family floor guessed 128000. Runs through the real router against a
// fake upstream; the connection's baseUrl points the catalogue at it.
func TestOpenCodeGoLiveFeedPublishesProviderWindow(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[
				{"id":"hy4-preview","name":"Hy4 Preview","limit":{"context":1024000,"output":64000}},
				{"id":"muse-spark-1.3-contributor","name":"Muse Spark","limit":{"context":1048576,"output":131072}},
				{"id":"beam-free","name":"Beam Free","limit":{"context":1048576,"output":131072}}
			]}`))
			return
		}
		chatCompletionResponder()(w, r)
	})
	env.AddConnection(t, "conn-ocg-feed", "opencode-go", "OpenCode Go Feed", upstream, "sk-ocg")

	listed := env.Get(t, "/v1/models")
	if listed.Status != http.StatusOK {
		t.Fatalf("/v1/models status = %d: %s", listed.Status, truncate(listed.Body))
	}
	var catalog modelsResponse
	listed.Decode(t, &catalog)

	entry, ok := lo.Find(catalog.Data, func(e modelEntry) bool { return e.ID == "ocg/hy4-preview" })
	if !ok {
		t.Fatalf("ocg/hy4-preview absent from /v1/models; got %v", catalog.ids())
	}
	if entry.ContextLength == nil || *entry.ContextLength != 1024000 {
		t.Errorf("context_length = %v, want 1024000 (the go feed's limit.context)", entry.ContextLength)
	}
	if entry.MaxCompletionTokens == nil || *entry.MaxCompletionTokens != 64000 {
		t.Errorf("max_completion_tokens = %v, want 64000 (the go feed's limit.output)", entry.MaxCompletionTokens)
	}
	if !lo.Contains(catalog.ids(), "ocg/beam-free") {
		t.Errorf("model list = %v, want the feed's beam-free too", catalog.ids())
	}
}
