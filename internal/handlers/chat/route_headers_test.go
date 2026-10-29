package chat

import (
	"net/http/httptest"
	"os"
	"testing"
)

func TestRouteHeaders_SingleModel(t *testing.T) {
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
	t.Setenv("NINEROUTER_ROUTE_HEADERS", "0")
	rec := httptest.NewRecorder()
	injectRouteHeaders(rec, "anthropic", "claude-3", "combo", 1)

	resp := rec.Result()
	if resp.Header.Get("x-9router-provider") != "" {
		t.Errorf("expected headers suppressed when disabled, got x-9router-provider=%s", resp.Header.Get("x-9router-provider"))
	}

	t.Setenv("NINEROUTER_ROUTE_HEADERS", "false")
	rec2 := httptest.NewRecorder()
	injectRouteHeaders(rec2, "anthropic", "claude-3", "combo", 1)
	resp2 := rec2.Result()
	if resp2.Header.Get("x-9router-provider") != "" {
		t.Errorf("expected headers suppressed when NINEROUTER_ROUTE_HEADERS=false")
	}
}
