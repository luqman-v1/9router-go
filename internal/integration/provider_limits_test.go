//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// seedNaraNode stores the custom provider node and one connection pointed at a
// fake upstream, which is the fixture both tests below need. The connection
// carries the node's prefix in providerSpecificData, because that is how a model
// written as "nara/<model>" resolves to this node rather than to a registry
// provider with the same name.
func seedNaraNode(t *testing.T, env *Env, upstream *Upstream, limits db.ProviderLimits) {
	t.Helper()
	if _, err := env.Repo.CreateProviderNode("node-nara", "openai-compatible", "Nara AI",
		`{"prefix":"nara","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create provider node: %v", err)
	}
	priority := 1
	data := `{"apiKey":"sk-nara","baseUrl":"` + upstream.URL + `/chat/completions","providerSpecificData":{"prefix":"nara"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-nara", "node-nara", "apikey", "Nara", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := env.Repo.SetProviderLimits(context.Background(), "node-nara", limits); err != nil {
		t.Fatalf("set provider limits: %v", err)
	}
}

// refusalType pulls error.type out of the gateway's OpenAI-style envelope.
func refusalType(t *testing.T, res *Result) string {
	t.Helper()
	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	res.Decode(t, &body)
	return body.Error.Type
}

// The provider-node capacity limits have to be enforced through the router the
// binary builds, not through a handler a test assembled by hand: the gate needs
// the process-wide rate limiter, and nothing outside a boot hands it over. A unit
// test sets that field itself, so it cannot tell a wired gateway from an inert
// one — which is the exact failure this suite exists to catch, and the shape of
// the bug it caught here (the per-node gate was reachable from no production
// call site).
func TestProviderNodeRateLimitIsEnforcedThroughTheRouter(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	seedNaraNode(t, env, upstream, db.ProviderLimits{RPM: 1})

	first := env.Post(t, "/v1/chat/completions", ChatBody("nara/glm-5.3", false))
	if first.Status != http.StatusOK {
		t.Fatalf("first request = %d, want 200 (body: %s)", first.Status, truncate(first.Body))
	}

	second := env.Post(t, "/v1/chat/completions", ChatBody("nara/glm-5.3", false))
	if second.Status != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429 (body: %s)", second.Status, truncate(second.Body))
	}
	if got := refusalType(t, second); got != "rate_limit_exceeded" {
		t.Fatalf("refusal type = %q, want rate_limit_exceeded (body: %s)", got, truncate(second.Body))
	}
	if got := upstream.Count(); got != 1 {
		t.Fatalf("upstream saw %d requests, want 1 — the refused attempt must not have been forwarded", got)
	}
}

// A spent lifetime quota is refused too, and with the status that keeps the two
// axes apart: 403 for a node state nothing can wait out, versus the 429 above for
// congestion. The spend is produced by the first request rather than written into
// the ledger by hand, so the same test covers the accounting that feeds it.
func TestProviderNodeSpentQuotaIsEnforcedThroughTheRouter(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	// A ceiling of one token: any real completion blows straight through it.
	seedNaraNode(t, env, upstream, db.ProviderLimits{TokenQuota: 1})

	first := env.Post(t, "/v1/chat/completions", ChatBody("nara/glm-5.3", false))
	if first.Status != http.StatusOK {
		t.Fatalf("first request = %d, want 200 (body: %s)", first.Status, truncate(first.Body))
	}
	dispatched := upstream.Count()

	// Usage is recorded after the response is written, so wait for the spend to
	// become visible rather than racing the usage writer and failing for the
	// wrong reason.
	if used := waitForNodeUsage(t, env, "node-nara"); used <= 1 {
		t.Fatalf("recorded spend = %d, want more than the 1-token quota", used)
	}

	res := env.Post(t, "/v1/chat/completions", ChatBody("nara/glm-5.3", false))
	if res.Status != http.StatusForbidden {
		t.Fatalf("request on a spent quota = %d, want 403 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := refusalType(t, res); got != "provider_token_quota_exhausted" {
		t.Fatalf("refusal type = %q, want provider_token_quota_exhausted (body: %s)", got, truncate(res.Body))
	}
	if got := upstream.Count(); got != dispatched {
		t.Fatalf("the refused request reached the provider (%d -> %d dispatches)", dispatched, got)
	}
}

// An expired contract is refused before the limiter is consulted at all, so it
// answers 403 on a node whose RPM is unlimited.
func TestProviderNodeContractExpiryIsEnforcedThroughTheRouter(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	seedNaraNode(t, env, upstream, db.ProviderLimits{ContractExpiresAt: "2020-01-01T00:00:00Z"})

	res := env.Post(t, "/v1/chat/completions", ChatBody("nara/glm-5.3", false))
	if res.Status != http.StatusForbidden {
		t.Fatalf("request on an expired contract = %d, want 403 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := refusalType(t, res); got != "provider_contract_expired" {
		t.Fatalf("refusal type = %q, want provider_contract_expired (body: %s)", got, truncate(res.Body))
	}
	if got := upstream.Count(); got != 0 {
		t.Fatalf("upstream saw %d requests, want 0", got)
	}
}

// A node-state refusal is a fact about the node, not about the credential, so it
// must not cost the connection a cooldown. The fallback loop pins each candidate
// before dialling, which skips the selection-time node-state check, so the 403 is
// produced by the dispatch gate and travels back through the generic
// retryable-status branch — the one that writes LockConnectionModel and
// LockConnectionRateLimit. Intercepting the typed refusal before that branch is
// what keeps a healthy account out of a 120-second corner because an operator's
// contract lapsed.
//
// The two requests matter as much as the row: without the interception the first
// one parks the account, so the second is no longer refused by the node gate at
// all. A second 403 with the same type is what proves the account stayed in
// rotation for the duration of its own node-state problem.
func TestProviderNodeStateRefusalDoesNotParkTheConnection(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	seedNaraNode(t, env, upstream, db.ProviderLimits{ContractExpiresAt: "2020-01-01T00:00:00Z"})

	for attempt := 1; attempt <= 2; attempt++ {
		res := env.Post(t, "/v1/chat/completions", ChatBody("nara/glm-5.3", false))
		if res.Status != http.StatusForbidden {
			t.Fatalf("request %d on an expired contract = %d, want 403 (body: %s)", attempt, res.Status, truncate(res.Body))
		}
		if got := refusalType(t, res); got != "provider_contract_expired" {
			t.Fatalf("request %d refusal type = %q, want provider_contract_expired (body: %s)", attempt, got, truncate(res.Body))
		}
	}

	stored, err := env.Repo.GetProviderConnectionByID("conn-nara")
	if err != nil || stored == nil {
		t.Fatalf("read back conn-nara: %v", err)
	}
	// The cooldown lives in the connection's own data blob, which is what the
	// selector reads, so that is where the absence has to be shown. The lastError
	// row is expected and wanted: it is the node-health line this feature exists
	// to show, and it is display-only — pinnedConnectionIneligible skips an account
	// on rateLimitedUntil and on a model lock, never on that row.
	for _, forbidden := range []string{`"rateLimitedUntil"`, `"modelLock_`} {
		if strings.Contains(stored.Data, forbidden) {
			t.Errorf("connection data carries %s after a node-state refusal: %s", forbidden, stored.Data)
		}
	}
	// And the row must name the status the client saw. A hardcoded 429 here would
	// put "too many requests" on a node whose real condition is a lapsed contract.
	if !strings.Contains(stored.Data, `"status":403`) {
		t.Errorf("lastError does not carry the 403 the client received: %s", stored.Data)
	}
	if got := upstream.Count(); got != 0 {
		t.Fatalf("upstream saw %d requests, want 0", got)
	}
}

// The write routes have to be reachable on the router the binary builds, not
// only on the table the dashboard package mounts in its own tests: those two
// tables are separate registrations, and a route added to one is a 404 on the
// other. This drives the policy editor the dashboard uses and reads the result
// back through the listing the same editor renders from.
func TestProviderNodeLimitsAreEditableThroughTheRouter(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	seedNaraNode(t, env, upstream, db.ProviderLimits{})

	put := env.Put(t, "/api/provider-nodes/node-nara/limits",
		map[string]any{"rpm": 42, "tokenQuota": 1000, "contractExpiresAt": "2027-01-31"})
	if put.Status != http.StatusOK {
		t.Fatalf("PUT limits = %d, want 200 (body: %s)", put.Status, truncate(put.Body))
	}

	var stored struct {
		Limits struct {
			RPM               int    `json:"rpm"`
			TokenQuota        int    `json:"tokenQuota"`
			ContractExpiresAt string `json:"contractExpiresAt"`
		} `json:"limits"`
	}
	put.Decode(t, &stored)
	if stored.Limits.RPM != 42 || stored.Limits.TokenQuota != 1000 {
		t.Fatalf("saved limits = %+v, want rpm 42 and tokenQuota 1000", stored.Limits)
	}
	if stored.Limits.ContractExpiresAt != "2027-01-31T00:00:00Z" {
		t.Errorf("contractExpiresAt = %q, want a date input normalised to RFC3339 UTC", stored.Limits.ContractExpiresAt)
	}

	// The listing the dashboard renders from must show the same row.
	res := env.Get(t, "/api/provider-nodes")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/provider-nodes = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var body struct {
		Nodes []struct {
			ID     string `json:"id"`
			Limits struct {
				RPM        int `json:"rpm"`
				TokenQuota int `json:"tokenQuota"`
			} `json:"limits"`
		} `json:"nodes"`
	}
	res.Decode(t, &body)
	listed := false
	for _, n := range body.Nodes {
		if n.ID != "node-nara" {
			continue
		}
		listed = true
		if n.Limits.RPM != 42 || n.Limits.TokenQuota != 1000 {
			t.Errorf("listed limits = %+v, want the values just saved", n.Limits)
		}
	}
	if !listed {
		t.Fatalf("node-nara is absent from GET /api/provider-nodes: %s", truncate(res.Body))
	}

	reset := env.Post(t, "/api/provider-nodes/node-nara/quota/reset", nil)
	if reset.Status != http.StatusOK {
		t.Fatalf("POST quota reset = %d, want 200 (body: %s)", reset.Status, truncate(reset.Body))
	}
}

// TestProviderNodeLimits_ClearedToZero pins the clearing path through the full
// router (request → handler → DB → response): an operator explicitly setting
// every limit back to zero must not be silently dropped. The previous UI guard
// (hasAnyNodeLimitPatch in the edit-save path) treated an all-zero patch as
// "nothing to write", so the PUT never reached the router and the stored limits
// kept their previous non-zero values.
func TestProviderNodeLimits_ClearedToZero(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	seedNaraNode(t, env, upstream, db.ProviderLimits{RPM: 60, TPM: 90_000, Concurrency: 4})

	// Non-zero values are stored as-is.
	stored, err := env.Repo.GetProviderLimits(context.Background(), "node-nara")
	if err != nil {
		t.Fatalf("GetProviderLimits (seed): %v", err)
	}
	if stored.RPM != 60 || stored.TPM != 90_000 || stored.Concurrency != 4 {
		t.Fatalf("seed limits = %+v, want rpm 60/tpm 90000/concurrency 4", stored)
	}

	// The editor sends an explicit all-zero patch when every field is cleared.
	put := env.Put(t, "/api/provider-nodes/node-nara/limits",
		map[string]any{
			"rpm":         0,
			"tpm":         0,
			"concurrency": 0,
			"tokenQuota":  0,
		})
	if put.Status != http.StatusOK {
		t.Fatalf("PUT /limits (zero patch) = %d, want 200 (body: %s)", put.Status, truncate(put.Body))
	}

	// Reading back must show every axis at 0 (unlimited).
	after, err := env.Repo.GetProviderLimits(context.Background(), "node-nara")
	if err != nil {
		t.Fatalf("GetProviderLimits (after clear): %v", err)
	}
	if after.RPM != 0 || after.TPM != 0 || after.Concurrency != 0 || after.TokenQuota != 0 {
		t.Errorf("limits after zero patch = %+v, want every axis at 0 (unlimited)", after)
	}
}

// nodeUsage reads one node's reported spend from GET /api/provider-nodes — the
// same number the dispatch gate compares against the node's quota, so a test that
// waits on it cannot disagree with the refusal it then expects.
func nodeUsage(t *testing.T, env *Env, nodeID string) int {
	t.Helper()
	res := env.Get(t, "/api/provider-nodes")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/provider-nodes = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var body struct {
		Nodes []struct {
			ID     string `json:"id"`
			Limits struct {
				UsedTokens int `json:"usedTokens"`
			} `json:"limits"`
		} `json:"nodes"`
	}
	res.Decode(t, &body)
	for _, n := range body.Nodes {
		if n.ID == nodeID {
			return n.Limits.UsedTokens
		}
	}
	t.Fatalf("node %s is absent from GET /api/provider-nodes", nodeID)
	return 0
}

// waitForNodeUsage polls the node listing until the spend shows up, bounded so a
// wiring failure fails fast instead of hanging.
func waitForNodeUsage(t *testing.T, env *Env, nodeID string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := 0
	for time.Now().Before(deadline) {
		last = nodeUsage(t, env, nodeID)
		if last > 0 {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
	return last
}
