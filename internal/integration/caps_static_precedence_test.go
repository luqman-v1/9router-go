//go:build integration

package integration

import (
	"net/http"
	"testing"

	"9router/proxy/internal/auth"
)

// Issue #424: listing the models — the normal startup move for an SDK or a CLI —
// must not republish a custom row's caps under an id the static catalog owns.
// The custom-caps map is process-wide and every capability read consults it
// first, so before this the caps endpoint answered the custom row's smaller
// window from its first call onward, for the lifetime of the process, and the
// dashboard rendered that number for every operator afterwards.
//
// The regression test has to go through the real list route, not call the caps
// handler twice: the publisher being tested is the one /v1/models runs.
func TestModelListDoesNotPoisonTheStaticCatalogCaps(t *testing.T) {
	env := newEnv(t)

	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-ocz", "opencode-zen", "OpenCode Zen", upstream, "sk-ocz")

	// A custom row whose id the static catalog already owns, declaring a far
	// smaller window than the catalog's 1048576.
	const collidingID = "mimo-v2.6-flash-free"
	colliding := `{"providerAlias":"opencode-zen","id":"` + collidingID + `","type":"llm",` +
		`"name":"` + collidingID + `","contextWindow":4096,"maxOutput":1024}`
	if err := env.Repo.SetKV("customModels", "opencode-zen|"+collidingID+"|llm", colliding); err != nil {
		t.Fatalf("save colliding custom model: %v", err)
	}

	readWindow := func(call string) int {
		t.Helper()
		res := env.Get(t, "/api/models/caps?provider=opencode-zen",
			WithHeader(auth.CLITokenHeader, auth.CLIToken()))
		if res.Status != http.StatusOK {
			t.Fatalf("%s: /api/models/caps status = %d: %s", call, res.Status, truncate(res.Body))
		}
		var body struct {
			Caps map[string]struct {
				ContextWindow int `json:"contextWindow"`
			} `json:"caps"`
		}
		res.Decode(t, &body)
		entry, ok := body.Caps[collidingID]
		if !ok {
			t.Fatalf("%s: %s absent from /api/models/caps; got %d entries",
				call, collidingID, len(body.Caps))
		}
		return entry.ContextWindow
	}

	before := readWindow("before /v1/models")
	if before == 4096 {
		t.Fatalf("fixture is not a collision: /api/models/caps already answers the custom 4096 for %s", collidingID)
	}

	// The poisoning move: one model-list request, as a client does at startup.
	listed := env.Get(t, "/v1/models?all=1")
	if listed.Status != http.StatusOK {
		t.Fatalf("/v1/models?all=1 status = %d: %s", listed.Status, truncate(listed.Body))
	}

	for _, call := range []string{"first call after /v1/models", "second call after /v1/models"} {
		if got := readWindow(call); got != before {
			t.Errorf("%s: %s contextWindow = %d, want the static catalog's %d — the list request republished the custom row",
				call, collidingID, got, before)
		}
	}
}
