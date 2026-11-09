package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/middleware"
)

// Provider-scope enforcement, request side. These tests cover the properties the
// plan promises and that nothing else can prove: a refusal is a failed attempt
// rather than an ending, a request remembers which providers were full, a refusal
// never parks a healthy account, and an unreadable policy fails open.

// seedLimitedProviderNode inserts a custom provider node carrying a policy.
func seedLimitedProviderNode(t *testing.T, repo *db.Repo, id string, limits db.ProviderLimits) {
	t.Helper()
	if _, err := repo.CreateProviderNode(id, "openai-compatible", id,
		`{"prefix":"`+id+`","apiType":"openai-compatible","baseUrl":"https://example.test/v1"}`,
	); err != nil {
		t.Fatalf("CreateProviderNode %s: %v", id, err)
	}
	if err := repo.SetProviderLimits(context.Background(), id, limits); err != nil {
		t.Fatalf("SetProviderLimits %s: %v", id, err)
	}
}

// newProviderLimitHandler builds a chat handler with a real repository and a real
// limiter, because the enforcement under test is exactly the seam between them.
func newProviderLimitHandler(t *testing.T) (*ChatHandler, *db.Repo) {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)
	handler.RateLimiter = middleware.NewRateLimiter(time.Minute)
	return handler, repo
}

func TestAcquireProviderAttempt_UnlimitedProviderReturnsNoSlot(t *testing.T) {
	handler, _ := newProviderLimitHandler(t)
	ctx := withProviderLimitState(context.Background())

	// A registry provider (no providerNodes row) is the common case: every
	// request to deepseek, groq, claude and so on goes through here.
	slot, err := handler.acquireProviderAttempt(ctx, "deepseek", "deepseek-chat", "conn-1", []byte(`{"messages":[]}`))
	if slot != nil || err != nil {
		t.Fatalf("unlimited provider = slot %v err %v, want nil/nil", slot, err)
	}
}

func TestAcquireProviderAttempt_RefusalIsARateLimitErrorNamingTheAttempt(t *testing.T) {
	handler, repo := newProviderLimitHandler(t)
	seedLimitedProviderNode(t, repo, "node-nara", db.ProviderLimits{RPM: 1})
	ctx := withProviderLimitState(context.Background())

	slot, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{"messages":[]}`))
	if slot == nil || err != nil {
		t.Fatalf("first attempt = slot %v err %v, want an admitted slot", slot, err)
	}
	slot.Release()

	_, err = handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{"messages":[]}`))
	if err == nil {
		t.Fatal("second attempt was admitted while the provider's window was full")
	}

	// The existing writers answer a *upstreamError, and a provider refusal has to
	// reach them with the 429 and a readable body rather than a 502.
	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("refusal = %T, want something errors.As can read as *upstreamError", err)
	}
	if ue.StatusCode != 429 {
		t.Errorf("refusal status = %d, want 429", ue.StatusCode)
	}
	body := string(ue.Body)
	for _, want := range []string{`"attempts"`, `"scope":"provider"`, "node-nara", `"reason":"rpm"`} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal body misses %s: %s", want, body)
		}
	}

	attempt, ok := providerLimitRefusal(err)
	if !ok {
		t.Fatalf("providerLimitRefusal(%v) = not a provider refusal", err)
	}
	if attempt.Provider != "node-nara" || attempt.Axis != "rpm" || attempt.Limit != 1 {
		t.Errorf("attempt = %+v, want node-nara/rpm/1", attempt)
	}
}

// TestAcquireProviderAttempt_RefusedProviderIsNotWaitedOnTwice is the failover
// cost the request-scoped state exists to pay once: the first refusal waits out
// the queue timeout, and every later attempt on that provider must return at once
// so the pass can reach its other candidates.
func TestAcquireProviderAttempt_RefusedProviderIsNotWaitedOnTwice(t *testing.T) {
	handler, repo := newProviderLimitHandler(t)
	seedLimitedProviderNode(t, repo, "node-nara", db.ProviderLimits{RPM: 1, QueueTimeoutMs: 120})
	ctx := withProviderLimitState(context.Background())

	slot, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{}`))
	if slot == nil || err != nil {
		t.Fatalf("first attempt = slot %v err %v, want an admitted slot", slot, err)
	}
	slot.Release()

	start := time.Now()
	if _, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{}`)); err == nil {
		t.Fatal("expected a refusal once the window was full")
	}
	first := time.Since(start)
	if first < 100*time.Millisecond {
		t.Errorf("first refusal took %v, want it to wait out the 120ms queue timeout", first)
	}

	start = time.Now()
	if _, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{}`)); err == nil {
		t.Fatal("expected the provider to stay refused for the rest of the request")
	}
	if second := time.Since(start); second > 60*time.Millisecond {
		t.Errorf("second refusal took %v, want it to return at once from the request's own memory", second)
	}
}

// TestAcquireProviderAttempt_MemoisesThePolicyForTheRequest: a combo that tries
// three models on one node reads the policy once, so an operator raising the limit
// mid-request does not change what this pass enforces. The next request reads the
// new value.
func TestAcquireProviderAttempt_MemoisesThePolicyForTheRequest(t *testing.T) {
	handler, repo := newProviderLimitHandler(t)
	seedLimitedProviderNode(t, repo, "node-nara", db.ProviderLimits{RPM: 1})
	ctx := withProviderLimitState(context.Background())

	slot, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{}`))
	if slot == nil || err != nil {
		t.Fatalf("first attempt = slot %v err %v, want an admitted slot", slot, err)
	}
	slot.Release()

	if err := repo.SetProviderLimits(context.Background(), "node-nara", db.ProviderLimits{RPM: 500}); err != nil {
		t.Fatalf("SetProviderLimits: %v", err)
	}
	if _, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{}`)); err == nil {
		t.Error("the same request re-read the policy; the pass it started with should hold")
	}

	fresh := withProviderLimitState(context.Background())
	if slot, err := handler.acquireProviderAttempt(fresh, "node-nara", "glm-5.3", "conn-1", []byte(`{}`)); slot == nil || err != nil {
		t.Errorf("a new request = slot %v err %v, want it to pick up the raised limit", slot, err)
	}
}

// TestAcquireProviderAttempt_FailsOpenWhenThePolicyCannotBeRead: an unreadable
// policy must not take an endpoint offline. Treating the read failure as "no
// headroom" would turn one storage blip into an outage for every custom provider.
func TestAcquireProviderAttempt_FailsOpenWhenThePolicyCannotBeRead(t *testing.T) {
	handler, _ := newProviderLimitHandler(t)
	ctx := withProviderLimitState(context.Background())
	if err := handler.Repo.RawDB().Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	slot, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{}`))
	if slot != nil || err != nil {
		t.Fatalf("unreadable policy = slot %v err %v, want the attempt to proceed", slot, err)
	}
}

// TestRecordProviderLimit_DebugTrailWithoutACooldown pins the two halves of D7.1:
// the refusal is visible (connection lastError, so the node-health table shows it)
// and it does NOT park the account (no cooldown), because the endpoint was busy for
// a moment and the credential is fine.
func TestRecordProviderLimit_DebugTrailWithoutACooldown(t *testing.T) {
	handler, repo := newProviderLimitHandler(t)
	seedLimitedProviderNode(t, repo, "node-nara", db.ProviderLimits{RPM: 1})
	if err := repo.CreateProviderConnectionFull("conn-nara", "node-nara", "apikey", "Nara",
		nil, `{"apiKey":"sk-test-nara"}`); err != nil {
		t.Fatalf("CreateProviderConnectionFull: %v", err)
	}
	ctx := withProviderLimitState(context.Background())
	attempt := providerLimitAttempt{Provider: "node-nara", Axis: "rpm", Limit: 1, RetryAfter: 3}

	handler.recordProviderLimit(ctx, providerLimitStateFrom(ctx), "node-nara", "glm-5.3", "conn-nara", attempt)

	conn, err := repo.GetProviderConnectionByID("conn-nara")
	if err != nil {
		t.Fatalf("GetProviderConnectionByID: %v", err)
	}
	if conn == nil {
		t.Fatal("connection disappeared")
	}
	if !strings.Contains(conn.Data, "rpm") {
		t.Errorf("lastError does not name the axis that refused: %s", conn.Data)
	}
	if _, parked := db.ConnectionCooldownUntil(conn.Data); parked {
		t.Error("a provider limit refusal parked the connection in cooldown; a busy endpoint is not a dead credential")
	}

	attempts := providerLimitStateFrom(ctx).snapshotAttempts()
	if len(attempts) != 1 || attempts[0].Provider != "node-nara" {
		t.Errorf("debug trail = %+v, want one entry for node-nara", attempts)
	}
}

// seedNodeUsage inserts one usageHistory row attributed to a provider node, so a
// lifetime quota has something to measure.
func seedNodeUsage(t *testing.T, repo *db.Repo, nodeID string, tokens int) {
	t.Helper()
	if _, err := repo.RawDB().Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		 VALUES (?, ?, 'glm-5.3', 'conn-1', 'upstream-cred', '/v1/chat/completions', ?, 0, 0.01, 'success', '{}', '')`,
		time.Now().UTC().Format(time.RFC3339), nodeID, tokens,
	); err != nil {
		t.Fatalf("seed node usage: %v", err)
	}
}

// TestAcquireProviderAttempt_RefusesAnExpiredContract: node state is checked
// before congestion and before the limiter, so a node with an expired contract is
// refused even when it declares no RPM/TPM/concurrency at all. The status is 403 —
// no amount of waiting ends a contract.
func TestAcquireProviderAttempt_RefusesAnExpiredContract(t *testing.T) {
	handler, repo := newProviderLimitHandler(t)
	seedLimitedProviderNode(t, repo, "node-nara", db.ProviderLimits{
		ContractExpiresAt: time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339),
	})
	ctx := withProviderLimitState(context.Background())

	slot, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{}`))
	if slot != nil || err == nil {
		t.Fatalf("attempt on an expired contract = slot %v err %v, want a refusal", slot, err)
	}
	var ue *upstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("refusal = %T, want something errors.As can read as *upstreamError", err)
	}
	if ue.StatusCode != 403 {
		t.Errorf("status = %d, want 403 (waiting cannot end a contract)", ue.StatusCode)
	}
	body := string(ue.Body)
	for _, want := range []string{"provider_contract_expired", "node-nara", `"reason":"contract"`} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal body misses %s: %s", want, body)
		}
	}
}

// TestAcquireProviderAttempt_RefusesASpentLifetimeQuota also pins the boundary: the
// quota blocks at exactly the limit, not one token past it.
func TestAcquireProviderAttempt_RefusesASpentLifetimeQuota(t *testing.T) {
	tests := []struct {
		name     string
		used     int
		wantDeny bool
	}{
		{name: "one token short stays usable", used: 99},
		{name: "exactly the quota is spent", used: 100, wantDeny: true},
		{name: "past the quota is spent", used: 150, wantDeny: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, repo := newProviderLimitHandler(t)
			seedLimitedProviderNode(t, repo, "node-nara", db.ProviderLimits{TokenQuota: 100})
			seedNodeUsage(t, repo, "node-nara", tt.used)
			ctx := withProviderLimitState(context.Background())

			slot, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{}`))
			if !tt.wantDeny {
				if slot != nil || err != nil {
					t.Fatalf("usable node = slot %v err %v, want nil/nil", slot, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("a spent quota was admitted (slot %v)", slot)
			}
			var ue *upstreamError
			if !errors.As(err, &ue) || ue.StatusCode != 403 {
				t.Fatalf("refusal = %v, want a 403 upstream error", err)
			}
			if body := string(ue.Body); !strings.Contains(body, "provider_token_quota_exhausted") {
				t.Errorf("refusal body does not name the exhausted quota: %s", body)
			}
		})
	}
}

// TestAcquireProviderAttempt_QuotaResetRestoresTheNode: the reset is the operator's
// lever, and it has to work without restarting anything.
func TestAcquireProviderAttempt_QuotaResetRestoresTheNode(t *testing.T) {
	handler, repo := newProviderLimitHandler(t)
	seedLimitedProviderNode(t, repo, "node-nara", db.ProviderLimits{TokenQuota: 100})
	seedNodeUsage(t, repo, "node-nara", 100)

	ctx := withProviderLimitState(context.Background())
	if _, err := handler.acquireProviderAttempt(ctx, "node-nara", "glm-5.3", "conn-1", []byte(`{}`)); err == nil {
		t.Fatal("a spent quota was admitted")
	}

	if _, err := repo.ResetProviderQuota(context.Background(), "node-nara"); err != nil {
		t.Fatalf("ResetProviderQuota: %v", err)
	}
	fresh := withProviderLimitState(context.Background())
	slot, err := handler.acquireProviderAttempt(fresh, "node-nara", "glm-5.3", "conn-1", []byte(`{}`))
	if slot != nil || err != nil {
		t.Fatalf("after a quota reset = slot %v err %v, want the node usable again", slot, err)
	}
}

// TestProviderNodeBlocked_FailsOpenWhenUsageCannotBeRead isolates the usage read:
// the policy is cached, so the failure under test is the ledger sum and not the
// policy read. A storage blip must not take a node out of rotation.
func TestProviderNodeBlocked_FailsOpenWhenUsageCannotBeRead(t *testing.T) {
	handler, repo := newProviderLimitHandler(t)
	ctx := withProviderLimitState(context.Background())
	state := providerLimitStateFrom(ctx)
	limits := db.ProviderLimits{TokenQuota: 1}
	state.cacheLimits("node-nara", limits)
	if err := repo.RawDB().Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	if refusal, blocked := handler.providerNodeBlocked(ctx, state, "node-nara", limits); blocked {
		t.Errorf("blocked on an unreadable ledger (%+v), want the node usable", refusal)
	}
}

// TestGetBestConnection_SkipsAnUnusableNode pins the half of the "one decision,
// two callers" rule that selection owns: the node must not be picked at all, and
// when it is the only candidate the request must fail with the node's own reason
// rather than an anonymous "no active connections".
func TestGetBestConnection_SkipsAnUnusableNode(t *testing.T) {
	handler, repo := newProviderLimitHandler(t)
	if err := repo.CreateProviderConnectionFull("conn-nara", "node-nara", "apikey", "Nara",
		nil, `{"apiKey":"sk-test-nara"}`); err != nil {
		t.Fatalf("CreateProviderConnectionFull: %v", err)
	}
	ctx := withProviderLimitState(context.Background())

	seedLimitedProviderNode(t, repo, "node-nara", db.ProviderLimits{
		ContractExpiresAt: time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	})
	conn, _, err := handler.getBestConnectionWithContext(ctx, "node-nara", "", nil, "glm-5.3")
	if err != nil || conn == nil {
		t.Fatalf("a usable node = conn %v err %v, want the connection", conn, err)
	}

	// Expire it, and ask with a fresh request state (the verdict is per request).
	// The node row is updated rather than re-created: providerNodes.id is a primary
	// key, and a second CreateProviderNode is a constraint error, not a second node.
	if err := repo.SetProviderLimits(context.Background(), "node-nara", db.ProviderLimits{
		ContractExpiresAt: time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("SetProviderLimits: %v", err)
	}
	expired := withProviderLimitState(context.Background())
	conn, _, err = handler.getBestConnectionWithContext(expired, "node-nara", "", nil, "glm-5.3")
	if err == nil {
		t.Fatalf("an expired node still produced a connection: %v", conn)
	}
	var ue *upstreamError
	if !errors.As(err, &ue) || ue.StatusCode != 403 {
		t.Fatalf("selection error = %v, want a typed 403 refusal", err)
	}
	if body := string(ue.Body); !strings.Contains(body, "provider_contract_expired") {
		t.Errorf("selection refusal does not name the cause: %s", body)
	}
}

// A node whose contract has ended must still be the provider the request
// resolves to. resolvePrefixProvider asks getBestConnection, which now refuses,
// and treating that refusal as "this prefix is not a node" sends the request to
// a provider no connection is registered under — the client then reads "no
// active connections for provider: <prefix>" and the real reason never surfaces.
func TestResolveModel_UnusableNodeStillResolvesToTheNode(t *testing.T) {
	handler, repo := newProviderLimitHandler(t)
	if err := repo.CreateProviderConnectionFull("conn-nara", "node-nara", "apikey", "Nara",
		new(1), `{"apiKey":"sk-nara"}`); err != nil {
		t.Fatalf("CreateProviderConnectionFull: %v", err)
	}
	seedLimitedProviderNode(t, repo, "node-nara", db.ProviderLimits{
		ContractExpiresAt: time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339),
	})

	info, err := handler.resolveModel("node-nara/glm-5.3")
	if err != nil {
		t.Fatalf("resolveModel: %v", err)
	}
	if info.Provider != "node-nara" {
		t.Fatalf("resolved provider = %q, want the node id — a fall-through hides the node's own refusal",
			info.Provider)
	}
	if info.ConnectionID != "" {
		t.Errorf("ConnectionID = %q, want none: the node has no usable connection to pin", info.ConnectionID)
	}
}
