package handlers

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

// The dashboard's breakdown table can be switched to "Usage by Provider" and
// every column is sortable, Last Used included. A provider bucket therefore
// needs the newest timestamp in it, exactly like the model, account, key and
// endpoint buckets.
func TestApplyRawAggregate_ByProviderCarriesLastUsed(t *testing.T) {
	older := "2026-10-20T02:00:00Z"
	newer := "2026-10-20T03:30:00Z"

	agg := newRawAggregate()
	agg.foldRow(db.UsageHistoryRow{
		Provider: "openai", Model: "gpt-4o", Timestamp: newer,
		PromptTokens: 20, CompletionTokens: 8, Cost: 0.002,
	})
	// Folded second on purpose: the bucket keeps the newest stamp regardless of
	// the order the rows arrive in.
	agg.foldRow(db.UsageHistoryRow{
		Provider: "openai", Model: "gpt-4o", Timestamp: older,
		PromptTokens: 10, CompletionTokens: 5, Cost: 0.001,
	})

	resp := &UsageStatsResponse{
		ByProvider: make(map[string]ProviderUsageItem),
		ByModel:    make(map[string]ModelUsageItem),
		ByAccount:  make(map[string]AccountUsageItem),
		ByApiKey:   make(map[string]ApiKeyUsageItem),
		ByEndpoint: make(map[string]EndpointUsageItem),
	}

	applyRawAggregate(resp, agg, nil, nil, nil)

	item, ok := resp.ByProvider["openai"]
	if !ok {
		t.Fatalf("missing openai in byProvider: %v", resp.ByProvider)
	}
	if item.Requests != 2 {
		t.Errorf("requests = %d, want 2", item.Requests)
	}
	if item.PromptTokens != 30 {
		t.Errorf("promptTokens = %d, want 30", item.PromptTokens)
	}
	if item.LastUsed != newer {
		t.Errorf("lastUsed = %q, want %q", item.LastUsed, newer)
	}

	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal provider item: %v", err)
	}
	// The dashboard reads the field off the wire, so assert the JSON name too —
	// a rename here silently blanks the column again.
	if !strings.Contains(string(encoded), `"lastUsed"`) {
		t.Errorf("byProvider item has no lastUsed field: %s", encoded)
	}
}

// A daily window aggregates usageDaily rows, which are keyed by date and carry
// no time of day. LastUsed stays empty there rather than being invented, and the
// omitempty tag keeps the field out of the payload entirely.
func TestHandleUsageStats_DailyByProviderHasNoLastUsed(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dailyJSON := `{
		"byProvider": {
			"openai": {"requests": 5, "promptTokens": 100, "completionTokens": 50, "cost": 0.002}
		}
	}`
	if _, err := database.Exec(`INSERT INTO usageDaily (dateKey, data) VALUES ('2026-10-14', ?)`, dailyJSON); err != nil {
		t.Fatalf("seed usageDaily: %v", err)
	}

	handler := HandleUsageStats(db.NewRepo(database))
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

	item, ok := resp.ByProvider["openai"]
	if !ok {
		t.Fatalf("missing openai in byProvider: %v", resp.ByProvider)
	}
	if item.Requests != 5 {
		t.Errorf("requests = %d, want 5", item.Requests)
	}
	if item.LastUsed != "" {
		t.Errorf("lastUsed = %q, want empty for a daily window", item.LastUsed)
	}
}
