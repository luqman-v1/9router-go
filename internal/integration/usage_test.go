//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
)

// TestSuccessfulRequestIsRecordedInUsageHistory pins the accounting path: a
// served completion must leave a usageHistory row with the provider, model,
// endpoint and token counts the dashboard bills from. Losing this write means
// the usage page silently under-reports spend — a revenue-shaped bug that no
// client-side test can see.
func TestSuccessfulRequestIsRecordedInUsageHistory(t *testing.T) {
	env, _ := newProviderEnv(t)

	if res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false)); res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	rows, err := env.Repo.GetRecentUsageHistory(10)
	if err != nil {
		t.Fatalf("read usage history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("usageHistory rows = %d, want exactly 1 for a single request", len(rows))
	}

	row := rows[0]
	if row.Provider != "deepseek" {
		t.Errorf("provider = %q, want deepseek", row.Provider)
	}
	if row.Model != "deepseek-chat" {
		t.Errorf("model = %q, want deepseek-chat (the model actually billed)", row.Model)
	}
	if row.Endpoint != "/v1/chat/completions" {
		t.Errorf("endpoint = %q, want /v1/chat/completions", row.Endpoint)
	}
	if row.Status != "success" {
		t.Errorf("status = %q, want success", row.Status)
	}
	if row.PromptTokens != 11 || row.CompletionTokens != 7 {
		t.Errorf("tokens = %d prompt / %d completion, want 11/7 from the upstream usage block",
			row.PromptTokens, row.CompletionTokens)
	}
	if row.ConnectionID != "conn-deepseek" {
		t.Errorf("connectionId = %q, want conn-deepseek", row.ConnectionID)
	}
	// The stored key is the upstream credential, and it must never be stored raw.
	if row.APIKey == "sk-upstream" {
		t.Error("usageHistory stored the upstream credential in the clear")
	}
}

// TestFailedRequestIsNotCountedAsSpend pins that a request which never produced
// a completion does not inflate the usage numbers. Only requestDetails records
// failures; usageHistory is the billing ledger.
func TestFailedRequestIsNotCountedAsSpend(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, JSONResponder(http.StatusInternalServerError,
		`{"error":{"message":"upstream exploded","type":"server_error"}}`))
	env.AddConnection(t, "conn-deepseek", "deepseek", "DeepSeek Account", upstream, "sk-upstream")

	if res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false)); res.Status != http.StatusInternalServerError {
		t.Fatalf("POST /v1/chat/completions = %d, want 500 (body: %s)", res.Status, truncate(res.Body))
	}

	rows, err := env.Repo.GetRecentUsageHistory(10)
	if err != nil {
		t.Fatalf("read usage history: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("usageHistory rows = %d, want 0: a failed request must not be billed as spend", len(rows))
	}

	// The failure still has to be visible for debugging.
	details, total, err := env.Repo.GetRequestDetailsPaged(context.Background(), 10, 0, "")
	if err != nil {
		t.Fatalf("read request details: %v", err)
	}
	if total == 0 {
		t.Error("requestDetails rows = 0, want the failure recorded for diagnostics")
		return
	}
	if len(details) == 0 {
		t.Error("requestDetails returned no payloads for a recorded failure")
	}
}

// TestUsageStatsAggregatesCompletedRequests pins the endpoint the dashboard's
// usage page reads: after two completions it reports the request count and the
// token totals under the provider bucket.
//
// The period is "all" rather than "today" because "today" is computed from a
// UTC midnight cutoff — a run straddling 00:00:00 UTC would report nothing.
func TestUsageStatsAggregatesCompletedRequests(t *testing.T) {
	env, _ := newProviderEnv(t)

	for range 2 {
		if res := env.Post(t, "/v1/chat/completions", ChatBody("deepseek/deepseek-chat", false)); res.Status != http.StatusOK {
			t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
		}
	}

	res := env.Get(t, "/api/usage/stats?period=all")
	if res.Status != http.StatusOK {
		t.Fatalf("GET /api/usage/stats = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}

	var stats struct {
		TotalRequests     int   `json:"totalRequests"`
		TotalPromptTokens int64 `json:"totalPromptTokens"`
		ByProvider        map[string]struct {
			Requests     int   `json:"requests"`
			PromptTokens int64 `json:"promptTokens"`
		} `json:"byProvider"`
	}
	res.Decode(t, &stats)

	if stats.TotalRequests != 2 {
		t.Errorf("totalRequests = %d, want 2", stats.TotalRequests)
	}
	if stats.TotalPromptTokens != 22 {
		t.Errorf("totalPromptTokens = %d, want 22 (2 requests x 11)", stats.TotalPromptTokens)
	}
	provider, ok := stats.ByProvider["deepseek"]
	if !ok {
		t.Fatalf("byProvider = %+v, want a deepseek bucket", stats.ByProvider)
	}
	if provider.Requests != 2 {
		t.Errorf("deepseek requests = %d, want 2", provider.Requests)
	}
}
