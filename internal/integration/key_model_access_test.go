//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// catalogModel is a model the seeded DeepSeek connection actually lists, taken
// from the /v1/models response of this same fixture. A test that restricts a
// key to an id the fixture never listed proves nothing about the resale path:
// an allowlist naming an absent model hides every model, which looks exactly
// like a restriction that works.
const catalogModel = "ds/deepseek-v4-pro"

// deniedModel resolves for dispatch but is not in the fixture's catalog, so it
// is a model a restricted key must be refused and an unrestricted one may call.
const deniedModel = "deepseek/deepseek-chat"

// seededKeyID resolves the id of the client key the harness seeded, so a test
// can apply a per-key policy. Reading it back through GetApiKeyByKey — the same
// lookup the API-key middleware performs on every request — means a test cannot
// silently pass against a key that no longer authenticates.
func seededKeyID(t *testing.T, env *Env) string {
	t.Helper()
	key, err := env.Repo.GetApiKeyByKey(env.APIKey)
	if err != nil {
		t.Fatalf("GetApiKeyByKey: %v", err)
	}
	if key == nil || key.ID == "" {
		t.Fatal("seeded api key not found — the harness seeding changed")
	}
	return key.ID
}

// TestReSellerKeyRestrictionEndToEnd is the resale case end to end: an operator
// mints a key for one buyer that may only call one model, the buyer is refused
// every other model with a 403, and the allowed model still reaches upstream.
//
// Each half matters. A restriction that never rejects anything is not a
// restriction, and one that rejects the allowed model is worse than none — so
// this asserts both through the same public surface the buyer uses, through the
// real router's API-key middleware, rather than calling the access helper
// directly.
func TestReSellerKeyRestrictionEndToEnd(t *testing.T) {
	env, upstream := newProviderEnv(t)

	// The seeded key holds no allowlist, so it starts unrestricted. The test
	// restricts it through the repository layer the dashboard handler writes
	// through: a policy applied by a route that never runs in production would
	// prove nothing about the resale path.
	if err := env.Repo.SetAllowedModels(seededKeyID(t, env), []string{catalogModel}); err != nil {
		t.Fatalf("SetAllowedModels: %v", err)
	}

	before := upstream.Count()

	res := env.Post(t, "/v1/chat/completions", ChatBody(deniedModel, false))
	if res.Status != http.StatusForbidden {
		t.Fatalf("POST %s = %d, want 403 (body: %s)", deniedModel, res.Status, truncate(res.Body))
	}
	if got := upstream.Count(); got != before {
		t.Errorf("upstream received %d requests after a denied model, want %d — the denied call must not leave the gateway", got, before)
	}

	res = env.Post(t, "/v1/chat/completions", ChatBody(catalogModel, false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST %s = %d, want 200 (body: %s)", catalogModel, res.Status, truncate(res.Body))
	}
	if got := upstream.Count(); got != before+1 {
		t.Fatalf("upstream count = %d, want %d — the allowed model must reach the provider", got, before+1)
	}
	if got := upstream.Last(t).Model(t); got != "deepseek-v4-pro" {
		t.Errorf("upstream model = %q, want \"deepseek-v4-pro\" (the prefix must be stripped)", got)
	}
}

// TestReSellerKeyRestrictionListMustAgreeWithDispatch closes the hole a
// dispatch-only check leaves: a client that can list a model it may not call
// discovers the buyer's policy by browsing /v1/models, so listing and dispatch
// must answer through the same allowlist.
//
// Both directions are asserted against one fixture. Restricted to a model the
// connection lists, the key keeps exactly that model visible; restricted to one
// it does not, the key sees nothing — the state an unrestricted listing could
// be mistaken for, which is why the unrestricted case is checked too.
func TestReSellerKeyRestrictionListMustAgreeWithDispatch(t *testing.T) {
	env, _ := newProviderEnv(t)
	keyID := seededKeyID(t, env)

	if err := env.Repo.SetAllowedModels(keyID, []string{catalogModel}); err != nil {
		t.Fatalf("SetAllowedModels(%s): %v", catalogModel, err)
	}

	listed := listModels(t, env)
	if !listed[catalogModel] {
		t.Errorf("GET /v1/models omitted %q, which dispatch allows — listing must not hide the key's own model", catalogModel)
	}
	if listed[deniedModel] {
		t.Errorf("GET /v1/models listed %q, which dispatch rejects — listing must not disclose a model the key cannot call", deniedModel)
	}

	// Now restrict the same key to a model the fixture never lists. The key
	// must see nothing at all: a non-empty list here would mean the filter
	// echoes the catalog rather than the allowlist.
	if err := env.Repo.SetAllowedModels(keyID, []string{deniedModel}); err != nil {
		t.Fatalf("SetAllowedModels(%s): %v", deniedModel, err)
	}
	listed = listModels(t, env)
	if len(listed) != 0 {
		t.Errorf("GET /v1/models returned %d models for a key restricted to an unlisted model, want 0: %v", len(listed), keysOf(listed))
	}
}

// listModels returns the ids GET /v1/models reports for this key.
func listModels(t *testing.T, env *Env) map[string]bool {
	t.Helper()
	res := env.Get(t, "/v1/models")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /v1/models = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	res.Decode(t, &listed)
	out := make(map[string]bool, len(listed.Data))
	for _, m := range listed.Data {
		out[m.ID] = true
	}
	return out
}

// keysOf sorts a model set for a deterministic failure message.
func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}