package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubOllamaBalance repoints the /api/balance endpoint at a test server.
func stubOllamaBalance(t *testing.T, balanceURL string) {
	t.Helper()
	previous := ollamaBalanceURL
	ollamaBalanceURL = balanceURL
	t.Cleanup(func() { ollamaBalanceURL = previous })
}

func TestFetchOllamaUsage_BalanceFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/usage":
			// The new API shape: activity history only, no limits.
			w.Write([]byte(`{"activity":{}}`))
		case "/api/me":
			w.Write([]byte(`{"Plan":"pro","CreatedAt":"2026-01-01T00:00:00Z"}`))
		case "/api/balance":
			w.Write([]byte(`{"included":{
				"session":{"remaining_percent":85.5,"resets_at":"2026-10-19T18:00:00Z"},
				"weekly":{"remaining_percent":92,"resets_at":"2026-10-25T00:00:00Z"}
			}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	stubOllamaUsage(t, srv.URL+"/api/usage", srv.URL+"/api/me")
	stubOllamaBalance(t, srv.URL+"/api/balance")

	res := fetchOllamaUsage(context.Background(), "k")
	if res.message != "" {
		t.Fatalf("unexpected message: %q", res.message)
	}
	if res.plan != "Pro" {
		t.Errorf("plan = %q, want Pro", res.plan)
	}
	session, ok := res.quotas["Session (5h)"].(map[string]any)
	if !ok {
		t.Fatalf("missing Session (5h) row: %v", res.quotas)
	}
	if session["used"].(float64) != 14.5 || session["remainingPercentage"].(float64) != 85.5 {
		t.Errorf("session row = %v", session)
	}
	if session["resetAt"] != "2026-10-19T18:00:00Z" {
		t.Errorf("session resetAt = %v", session["resetAt"])
	}
	weekly, ok := res.quotas["Weekly (7d)"].(map[string]any)
	if !ok {
		t.Fatalf("missing Weekly (7d) row: %v", res.quotas)
	}
	if weekly["used"].(float64) != 8 || weekly["remainingPercentage"].(float64) != 92 {
		t.Errorf("weekly row = %v", weekly)
	}
}

func TestFetchOllamaUsage_BalanceAllowanceAndPurchased(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/usage":
			w.Write([]byte(`{"activity":{}}`))
		case "/api/me":
			w.Write([]byte(`{"Plan":"free","CreatedAt":"2026-01-01T00:00:00Z"}`))
		case "/api/balance":
			w.Write([]byte(`{"included":{
				"allowance_usd":"2.50","balance_usd":"1.25",
				"period":{"until":"2026-11-01T00:00:00Z"}
			},"purchased":{"balance_usd":"10.00"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	stubOllamaUsage(t, srv.URL+"/api/usage", srv.URL+"/api/me")
	stubOllamaBalance(t, srv.URL+"/api/balance")

	res := fetchOllamaUsage(context.Background(), "k")
	if res.message != "" {
		t.Fatalf("unexpected message: %q", res.message)
	}
	allowance, ok := res.quotas["Allowance ($)"].(map[string]any)
	if !ok {
		t.Fatalf("missing Allowance row: %v", res.quotas)
	}
	if allowance["used"].(float64) != 1.25 || allowance["total"].(float64) != 2.5 {
		t.Errorf("allowance row = %v", allowance)
	}
	if allowance["remainingPercentage"].(float64) != 50 || allowance["unit"] != "USD" {
		t.Errorf("allowance row = %v", allowance)
	}
	if allowance["resetAt"] != "2026-11-01T00:00:00Z" {
		t.Errorf("allowance resetAt = %v", allowance["resetAt"])
	}
	credits, ok := res.quotas["Purchased Credits"].(map[string]any)
	if !ok {
		t.Fatalf("missing Purchased Credits row: %v", res.quotas)
	}
	if credits["total"].(float64) != 10 || credits["isCreditBalance"] != true || credits["currency"] != "$" {
		t.Errorf("credits row = %v", credits)
	}
}

func TestFetchOllamaUsage_BalanceUnreachableKeepsMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/usage":
			w.Write([]byte(`{"activity":{}}`))
		case "/api/me":
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	stubOllamaUsage(t, srv.URL+"/api/usage", srv.URL+"/api/me")
	stubOllamaBalance(t, srv.URL+"/api/balance")

	res := fetchOllamaUsage(context.Background(), "k")
	if res.message == "" {
		t.Fatalf("expected the no-limits message when balance is unavailable")
	}
}
