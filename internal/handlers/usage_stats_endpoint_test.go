package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleUsageStats_PopulatesByEndpoint(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)

	// Insert daily usage containing byEndpoint
	dailyJSON := `{
		"byProvider": {
			"openai": {"requests": 5, "promptTokens": 100, "completionTokens": 50, "cost": 0.002}
		},
		"byEndpoint": {
			"/v1/chat/completions|gpt-4o|openai": {
				"endpoint": "/v1/chat/completions",
				"rawModel": "gpt-4o",
				"provider": "openai",
				"requests": 5,
				"promptTokens": 100,
				"completionTokens": 50,
				"cost": 0.002
			}
		}
	}`
	_, err := database.Exec(`INSERT INTO usageDaily (dateKey, data) VALUES ('2026-10-14', ?)`, dailyJSON)
	if err != nil {
		t.Fatalf("seed usageDaily: %v", err)
	}

	handler := HandleUsageStats(repo)
	req := httptest.NewRequest(http.MethodGet, "/api/usage/stats?period=7d", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp UsageStatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if len(resp.ByEndpoint) == 0 {
		t.Fatalf("expected byEndpoint to be populated, got empty map")
	}

	item, ok := resp.ByEndpoint["/v1/chat/completions|gpt-4o|openai"]
	if !ok {
		t.Fatalf("expected key '/v1/chat/completions|gpt-4o|openai' in byEndpoint: %v", resp.ByEndpoint)
	}
	if item.Endpoint != "/v1/chat/completions" {
		t.Errorf("expected Endpoint='/v1/chat/completions', got %q", item.Endpoint)
	}
	if item.Requests != 5 {
		t.Errorf("expected Requests=5, got %d", item.Requests)
	}
	if item.PromptTokens != 100 {
		t.Errorf("expected PromptTokens=100, got %d", item.PromptTokens)
	}

}

func TestApplyRawAggregate_ByEndpoint(t *testing.T) {
	agg := newRawAggregate()
	row := db.UsageHistoryRow{
		Provider:         "openai",
		Model:            "gpt-4o",
		Endpoint:         "/v1/chat/completions",
		PromptTokens:     10,
		CompletionTokens: 5,
		Cost:             0.001,
	}
	agg.foldRow(row)

	resp := &UsageStatsResponse{
		ByProvider: make(map[string]ProviderUsageItem),
		ByModel:    make(map[string]ModelUsageItem),
		ByAccount:  make(map[string]AccountUsageItem),
		ByApiKey:   make(map[string]ApiKeyUsageItem),
		ByEndpoint: make(map[string]EndpointUsageItem),
	}

	applyRawAggregate(resp, agg, nil, nil, nil, allUsageStatsViews)

	if len(resp.ByEndpoint) != 1 {
		t.Fatalf("expected 1 endpoint in resp.ByEndpoint, got %d", len(resp.ByEndpoint))
	}
	epKey := "/v1/chat/completions|gpt-4o|openai"
	item, ok := resp.ByEndpoint[epKey]
	if !ok {
		t.Fatalf("missing key %s in ByEndpoint", epKey)
	}
	if item.Endpoint != "/v1/chat/completions" {
		t.Errorf("got %s, want /v1/chat/completions", item.Endpoint)
	}
	if item.Requests != 1 {
		t.Errorf("got requests=%d, want 1", item.Requests)
	}
}

func TestUsageWindowCache_SnapshotAndUnfoldByEndpoint(t *testing.T) {
	agg := newRawAggregate()
	row := db.UsageHistoryRow{
		Provider:         "openai",
		Model:            "gpt-4o",
		Endpoint:         "/v1/chat/completions",
		PromptTokens:     10,
		CompletionTokens: 5,
		Cost:             0.001,
	}
	agg.foldRow(row)

	// Verify snapshotOf preserves byEndpoint and endpointMeta
	snap := snapshotOf(agg)
	if len(snap.byEndpoint) != 1 {
		t.Fatalf("snapshotOf lost byEndpoint: got %d, want 1", len(snap.byEndpoint))
	}
	if len(snap.endpointMeta) != 1 {
		t.Fatalf("snapshotOf lost endpointMeta: got %d, want 1", len(snap.endpointMeta))
	}

	// Verify unfoldRow subtracts and removes expired endpoint rows
	agg.unfoldRow(row)
	if len(agg.byEndpoint) != 0 {
		t.Errorf("unfoldRow failed to remove empty endpoint bucket: %v", agg.byEndpoint)
	}
	if len(agg.endpointMeta) != 0 {
		t.Errorf("unfoldRow failed to remove empty endpointMeta: %v", agg.endpointMeta)
	}
}
