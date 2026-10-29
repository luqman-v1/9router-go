package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func TestRouteHeaders_SingleModel(t *testing.T) {
	resetRouteHeadersConfigForTesting()
	os.Unsetenv("NINEROUTER_ROUTE_HEADERS")
	rec := httptest.NewRecorder()
	injectRouteHeaders(rec, "anthropic", "claude-3.5-sonnet", "", 1)

	resp := rec.Result()
	if resp.Header.Get("x-9router-provider") != "anthropic" {
		t.Errorf("expected x-9router-provider=anthropic, got %s", resp.Header.Get("x-9router-provider"))
	}
	if resp.Header.Get("x-9router-model") != "claude-3.5-sonnet" {
		t.Errorf("expected x-9router-model=claude-3.5-sonnet, got %s", resp.Header.Get("x-9router-model"))
	}
	if resp.Header.Get("x-9router-combo") != "" {
		t.Errorf("expected x-9router-combo to be empty, got %s", resp.Header.Get("x-9router-combo"))
	}
	if resp.Header.Get("x-9router-attempts") != "1" {
		t.Errorf("expected x-9router-attempts=1, got %s", resp.Header.Get("x-9router-attempts"))
	}
}

func TestRouteHeaders_ComboAndAttempts(t *testing.T) {
	resetRouteHeadersConfigForTesting()
	os.Unsetenv("NINEROUTER_ROUTE_HEADERS")
	rec := httptest.NewRecorder()
	injectRouteHeaders(rec, "openai", "gpt-4o", "my-combo", 3)

	resp := rec.Result()
	if resp.Header.Get("x-9router-provider") != "openai" {
		t.Errorf("expected x-9router-provider=openai, got %s", resp.Header.Get("x-9router-provider"))
	}
	if resp.Header.Get("x-9router-model") != "gpt-4o" {
		t.Errorf("expected x-9router-model=gpt-4o, got %s", resp.Header.Get("x-9router-model"))
	}
	if resp.Header.Get("x-9router-combo") != "my-combo" {
		t.Errorf("expected x-9router-combo=my-combo, got %s", resp.Header.Get("x-9router-combo"))
	}
	if resp.Header.Get("x-9router-attempts") != "3" {
		t.Errorf("expected x-9router-attempts=3, got %s", resp.Header.Get("x-9router-attempts"))
	}
}

func TestRouteHeaders_Disabled(t *testing.T) {
	resetRouteHeadersConfigForTesting()
	t.Setenv("NINEROUTER_ROUTE_HEADERS", "0")
	rec := httptest.NewRecorder()
	injectRouteHeaders(rec, "anthropic", "claude-3", "combo", 1)

	resp := rec.Result()
	if resp.Header.Get("x-9router-provider") != "" {
		t.Errorf("expected headers suppressed when disabled, got x-9router-provider=%s", resp.Header.Get("x-9router-provider"))
	}

	resetRouteHeadersConfigForTesting()
	t.Setenv("NINEROUTER_ROUTE_HEADERS", "false")
	rec2 := httptest.NewRecorder()
	injectRouteHeaders(rec2, "anthropic", "claude-3", "combo", 1)
	resp2 := rec2.Result()
	if resp2.Header.Get("x-9router-provider") != "" {
		t.Errorf("expected headers suppressed when NINEROUTER_ROUTE_HEADERS=false")
	}
}

func TestRouteHeaders_ClearOnFailure(t *testing.T) {
	resetRouteHeadersConfigForTesting()
	os.Unsetenv("NINEROUTER_ROUTE_HEADERS")
	rec := httptest.NewRecorder()
	injectRouteHeaders(rec, "deepseek", "deepseek-chat", "combo-a", 2)

	if rec.Header().Get("x-9router-provider") != "deepseek" {
		t.Fatalf("expected provider header before clear")
	}

	clearRouteHeaders(rec)
	resp := rec.Result()
	if resp.Header.Get("x-9router-provider") != "" {
		t.Errorf("expected x-9router-provider cleared, got %s", resp.Header.Get("x-9router-provider"))
	}
	if resp.Header.Get("x-9router-model") != "" {
		t.Errorf("expected x-9router-model cleared, got %s", resp.Header.Get("x-9router-model"))
	}
	if resp.Header.Get("x-9router-combo") != "" {
		t.Errorf("expected x-9router-combo cleared, got %s", resp.Header.Get("x-9router-combo"))
	}
	if resp.Header.Get("x-9router-attempts") != "" {
		t.Errorf("expected x-9router-attempts cleared, got %s", resp.Header.Get("x-9router-attempts"))
	}
}

func TestRouteHeaders_DispatchIntegration(t *testing.T) {
	resetRouteHeadersConfigForTesting()
	os.Unsetenv("NINEROUTER_ROUTE_HEADERS")

	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	defer upstream.Close()

	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	rec := httptest.NewRecorder()
	ctx := context.Background()

	err := h.tryForwardWithConnection(forwardRequestParams{
		Ctx:          ctx,
		W:            rec,
		Provider:     "openai",
		Model:        "gpt-4o",
		ComboName:    "test-combo",
		Attempts:     2,
		StartedAt:    time.Now(),
		ConnectionID: "conn-test",
		ConnData:     &ConnectionData{APIKey: "sk-test", BaseURL: upstream.URL},
		Body:         []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		IsStream:     false,
		Endpoint:     "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("tryForwardWithConnection failed: %v", err)
	}

	resp := rec.Result()
	if got := resp.Header.Get("x-9router-provider"); got != "openai" {
		t.Errorf("expected x-9router-provider=openai, got %q", got)
	}
	if got := resp.Header.Get("x-9router-model"); got != "gpt-4o" {
		t.Errorf("expected x-9router-model=gpt-4o, got %q", got)
	}
	if got := resp.Header.Get("x-9router-combo"); got != "test-combo" {
		t.Errorf("expected x-9router-combo=test-combo, got %q", got)
	}
	if got := resp.Header.Get("x-9router-attempts"); got != "2" {
		t.Errorf("expected x-9router-attempts=2, got %q", got)
	}
}
