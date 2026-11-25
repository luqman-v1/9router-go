package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
)

// TestParseModelsFeedLiveModels pins the limit extraction: the provider's own
// feed is the authoritative source for a window, in either spelling. The nous
// feed states context_length at the top level and max_completion_tokens under
// top_provider; a normalized catalogue states contextLength/maxOutputTokens.
func TestParseModelsFeedLiveModels(t *testing.T) {
	body := `{"object":"list","data":[
		{"id":"qwen/qwen4-max","name":"Qwen4 Max","context_length":1000000,"top_provider":{"context_length":1000000,"max_completion_tokens":131072}},
		{"id":"vendor/camel","contextLength":262144,"maxOutputTokens":32000},
		{"id":"claude-fable-5","name":"Claude Fable 5","limit":{"context":1000000,"output":128000}},
		{"id":"vendor/bare","name":"No Limits"},
		{"id":"qwen/qwen4-max","name":"duplicate","context_length":1},
		{"id":"","context_length":1000}
	]}`

	got := parseModelsFeedLiveModels([]byte(body))
	want := []LiveModel{
		{ID: "qwen/qwen4-max", Name: "Qwen4 Max", ContextLength: 1000000, MaxOutput: 131072, LimitsAuthoritative: true},
		{ID: "vendor/camel", ContextLength: 262144, MaxOutput: 32000, LimitsAuthoritative: true},
		{ID: "claude-fable-5", Name: "Claude Fable 5", ContextLength: 1000000, MaxOutput: 128000, LimitsAuthoritative: true},
		{ID: "vendor/bare", Name: "No Limits"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", idsOf(got), idsOf(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := parseModelsFeedLiveModels([]byte(`not-json`)); got != nil {
		t.Errorf("unparseable body must yield nothing, got %v", idsOf(got))
	}
}

// TestResolveLiveCatalogModelsFeed pins that a provider with a models-list feed
// (nous) publishes the provider's own declared window, not the static table's
// family guess — the reported nous/qwen4-max 131k-vs-1M case.
func TestResolveLiveCatalogModelsFeed(t *testing.T) {
	resetLiveCatalog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("catalog path = %q, want /v1/models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-nous" {
			t.Errorf("Authorization = %q, want the connection bearer", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"object":"list","data":[
			{"id":"qwen/qwen4-max","name":"Qwen4 Max","context_length":1000000,"top_provider":{"max_completion_tokens":131072}},
			{"id":"qwen/qwen3.8-flash","name":"Qwen3.8 Flash","context_length":1000000,"top_provider":{"max_completion_tokens":131072}}
		]}`))
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))
	h.Client = srv.Client()

	conn := &models.ProviderConnection{ID: "conn-nous-live", Provider: "nous", IsActive: 1}
	connData := &shared.ConnectionData{APIKey: "sk-nous", BaseURL: srv.URL + "/v1/chat/completions"}

	got := h.resolveLiveCatalog(context.Background(), conn, connData, "nous")
	if len(got) != 2 {
		t.Fatalf("resolved %v, want the feed's two models", idsOf(got))
	}
	if got[0].ID != "qwen/qwen4-max" || got[0].ContextLength != 1000000 || got[0].MaxOutput != 131072 || !got[0].LimitsAuthoritative {
		t.Errorf("entry = %+v, want the provider's own 1000000/131072 declaration", got[0])
	}
}
