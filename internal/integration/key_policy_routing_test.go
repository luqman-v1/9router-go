//go:build integration

package integration

import (
	"net/http"
	"testing"
)

// P5 Step 2 drives the access gate through the real router rather than counting
// rows: a migration that lands the right rows still routes wrongly if the
// decision function, the listing filter, or the resolver disagree.
//
// Storage failure is deliberately NOT re-tested here. The auth middleware reads
// the key table before any handler runs, so a closed database answers 500 from
// the middleware and never reaches the gate — the integration fixture cannot
// isolate a gate-only storage failure. That case is pinned where it can be
// isolated: TestModelAccess_FailClosedSemantics in
// internal/handlers/chat/access_failclosed_test.go.

// listIDs decodes /v1/models and returns the advertised ids.
func listIDs(t *testing.T, env *Env) []string {
	t.Helper()
	res := env.Get(t, "/v1/models")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /v1/models = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var list modelsResponse
	res.Decode(t, &list)
	return list.ids()
}

// denyAllKey stores the sentinel a Node "restricted, allow nothing" key maps to.
// The G05 importer writes exactly this value, so the gate is exercised on the
// real payload shape rather than an empty list (which means "allow everything").
func denyAllKey(t *testing.T, env *Env) {
	t.Helper()
	if err := env.Repo.SetAllowedModels("integration-key", []string{"!*"}); err != nil {
		t.Fatalf("set deny-all allowlist: %v", err)
	}
}

// TestDenyAllKeyIsRefusedOnEveryInferenceLane is the restricted-empty contract:
// the sentinel allows nothing, so every lane that takes a model answers 403
// with the access-denial code. A lane that answers anything else is a hole the
// Node importer would open on every restricted-empty key it restores.
func TestDenyAllKeyIsRefusedOnEveryInferenceLane(t *testing.T) {
	env := newEnv(t)
	env.AddCombo(t, "combo-1", "resilient", []string{"deepseek/deepseek-chat"})
	denyAllKey(t, env)

	lanes := []struct {
		name string
		path string
		body any
	}{
		{name: "chat completions", path: "/v1/chat/completions", body: ChatBody("ds/deepseek-chat", false)},
		{name: "messages", path: "/v1/messages", body: claudeMessagesBody("ds/deepseek-chat", false)},
		{name: "responses", path: "/v1/responses", body: map[string]any{"model": "ds/deepseek-chat", "input": "hi"}},
		{name: "combo", path: "/v1/chat/completions", body: ChatBody("resilient", false)},
		{name: "embeddings", path: "/v1/embeddings", body: map[string]any{"model": "ds/deepseek-chat", "input": "hi"}},
		{name: "images", path: "/v1/images/generations", body: map[string]any{"model": "ds/deepseek-chat", "prompt": "a cat"}},
	}

	for _, lane := range lanes {
		t.Run(lane.name, func(t *testing.T) {
			res := env.Post(t, lane.path, lane.body)
			if res.Status != http.StatusForbidden {
				t.Fatalf("POST %s = %d, want 403 (body: %s)", lane.path, res.Status, truncate(res.Body))
			}
			assertAccessDenialCode(t, res)
		})
	}
}

// assertAccessDenialCode checks the semantic code, not just the status: a 403
// produced by some other policy (quota, expiry) must not be mistaken for the
// model-access denial this suite is about.
//
// The pair below is upstream's, reached through errorResponse(FORBIDDEN, …) →
// buildErrorBody → ERROR_TYPES[403], which the port reproduces in
// internal/handlerutil/response.go. There is no "model_not_allowed" code
// anywhere in the tree. The full body — including the message naming the
// requested model — is pinned at the unit level in
// internal/handlers/chat/access_error_shape_test.go; this suite only has to
// prove the gate reaches that writer.
func assertAccessDenialCode(t *testing.T, res *Result) {
	t.Helper()
	var envelope struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	res.Decode(t, &envelope)
	if envelope.Error.Type != "permission_error" || envelope.Error.Code != "insufficient_quota" {
		t.Errorf("error = {%q, %q}, want {permission_error, insufficient_quota} (body: %s)",
			envelope.Error.Type, envelope.Error.Code, truncate(res.Body))
	}
}

// TestDenyAllKeySeesNoModels pins the listing half of the same contract. A key
// that may call nothing must be shown nothing: a catalogue entry it cannot
// dispatch sends the client into a 403 it could have avoided.
func TestDenyAllKeySeesNoModels(t *testing.T) {
	env, _ := newProviderEnv(t)
	denyAllKey(t, env)

	if ids := listIDs(t, env); len(ids) != 0 {
		t.Fatalf("/v1/models = %v, want an empty list for a deny-all key", ids)
	}

	res := env.Get(t, "/v1/models/embedding")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /v1/models/embedding = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var kindList struct {
		Data []map[string]any `json:"data"`
	}
	res.Decode(t, &kindList)
	if len(kindList.Data) != 0 {
		t.Errorf("kind listing = %v, want an empty data array for a deny-all key", kindList.Data)
	}
}

// TestAllowlistedKeyListsExactlyItsModels pins the other direction: filtering
// must not be so eager that a permitted model disappears from the catalogue.
// The allowlist is written in the bare form a resolver call arrives as, so a
// filter that only matched provider-qualified ids would drop it.
func TestAllowlistedKeyListsExactlyItsModels(t *testing.T) {
	env, _ := newProviderEnv(t)
	restrictTestKey(t, env, "ds/deepseek-chat")

	ids := listIDs(t, env)
	if len(ids) == 0 {
		t.Fatal("/v1/models is empty for a key allowed exactly one connected model")
	}
	for _, id := range ids {
		if id != "ds/deepseek-chat" {
			t.Errorf("/v1/models = %v, want only ds/deepseek-chat", ids)
		}
	}

	// The permitted model must still be dispatchable through the real path.
	if res := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false)); res.Status != http.StatusOK {
		t.Fatalf("allowed model = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
}

// TestComboGrantDoesNotGrantDirectLeafAccess pins that a grant is for the combo
// NAME, not its seats. An operator who allows `resilient` is allowing the
// failover behaviour; letting the caller reach the leaf directly would let it
// pin a seat the combo exists to rotate away from, and would silently widen a
// narrower grant into the whole seat tree.
func TestComboGrantDoesNotGrantDirectLeafAccess(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-ds", "deepseek", "DeepSeek", upstream, "sk-upstream")
	env.AddCombo(t, "combo-1", "resilient", []string{"deepseek/deepseek-chat"})
	restrictTestKey(t, env, "resilient")

	if res := env.Post(t, "/v1/chat/completions", ChatBody("resilient", false)); res.Status != http.StatusOK {
		t.Fatalf("granted combo = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
	if res.Status != http.StatusForbidden {
		t.Fatalf("direct leaf = %d, want 403: a combo grant must not widen to its seats (body: %s)",
			res.Status, truncate(res.Body))
	}
	assertAccessDenialCode(t, res)
}

// TestDeniedModelIsNotDescribedByModelsInfo extends the listing rule to the
// single-model lookup. /v1/models/info is how an editor plugin sizes a context
// window, so answering it for a denied model both leaks the catalogue and
// invites a call that will be refused.
func TestDeniedModelIsNotDescribedByModelsInfo(t *testing.T) {
	env := newEnv(t)
	restrictTestKey(t, env, "only-this-model")

	res := env.Get(t, "/v1/models/info?id=ds/deepseek-chat")
	if res.Status != http.StatusForbidden {
		t.Fatalf("GET /v1/models/info for a denied model = %d, want 403 (body: %s)", res.Status, truncate(res.Body))
	}
	assertAccessDenialCode(t, res)
}

// TestAllowlistFollowsTheAliasTheCallerUsed pins that the gate matches the
// id the CLIENT typed, not the id the resolver arrived at. A dashboard alias
// (`team-fast`) and the provider-qualified form (`ds/deepseek-chat`) name the
// same model, so an allowlist written in either spelling must grant the call.
// A gate that only compared the resolved form would refuse the alias spelling
// and allow the other — the policy would follow the resolver's internals
// instead of the operator's intent.
func TestAllowlistFollowsTheAliasTheCallerUsed(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-ds", "deepseek", "DeepSeek", upstream, "sk-upstream")
	if err := env.Repo.SetKV("modelAliases", "team-fast", `"deepseek/deepseek-chat"`); err != nil {
		t.Fatalf("set model alias: %v", err)
	}

	restrictTestKey(t, env, "team-fast")
	res := env.Post(t, "/v1/chat/completions", ChatBody("team-fast", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST with the granted alias = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	// The same grant must not carry over to the underlying provider-qualified
	// id, which the operator did not name.
	restrictTestKey(t, env, "team-fast")
	direct := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
	if direct.Status != http.StatusForbidden {
		t.Fatalf("POST with the underlying id = %d, want 403: the grant named the alias, not the provider id (body: %s)",
			direct.Status, truncate(direct.Body))
	}
	assertAccessDenialCode(t, direct)
}

// TestStrictProxyRefusalIsABadGateway pins the status a strict-pool failure
// reaches the client with. The existing strict-proxy test only asserts the
// request was not answered 200 and that the provider was never reached —
// which a 500 would also satisfy. A 502 is the status that tells the operator
// "this hop failed", so the distinction has to be pinned.
func TestStrictProxyRefusalIsABadGateway(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	env.AddConnection(t, "conn-ds", "deepseek", "DeepSeek", upstream, "sk-upstream")
	poolID := addStrictPool(t, env, "pool-strict")
	assignPool(t, env, "conn-ds", poolID)

	res := env.Post(t, "/v1/chat/completions", ChatBody("ds/deepseek-chat", false))
	if res.Status != http.StatusBadGateway {
		t.Fatalf("POST through a dead strict pool = %d, want 502 (body: %s)", res.Status, truncate(res.Body))
	}
	if upstream.Count() != 0 {
		t.Errorf("the provider answered %d times: strict traffic escaped over the real IP", upstream.Count())
	}
}
