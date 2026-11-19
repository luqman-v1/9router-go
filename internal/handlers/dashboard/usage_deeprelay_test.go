package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubDeepRelayBalance repoints the wallet endpoint at a test server.
func stubDeepRelayBalance(t *testing.T, url string) {
	t.Helper()
	previous := deeprelayBalanceURL
	deeprelayBalanceURL = url
	t.Cleanup(func() { deeprelayBalanceURL = previous })
}

// TestFetchDeepRelayUsage_BalanceIsCreditRow covers the documented balance
// contract: balance_dollars becomes a credit balance row, not a percentage,
// because the wallet has no spend cap to divide by.
func TestFetchDeepRelayUsage_BalanceIsCreditRow(t *testing.T) {
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"balance_cents":12345,"balance_dollars":123.45,"auto_pay_enabled":false,"burn_cents_per_hour":null}`))
	}))
	t.Cleanup(srv.Close)
	stubDeepRelayBalance(t, srv.URL)

	res := fetchDeepRelayUsage(context.Background(), "deeprelay_live_x")
	if res.message != "" {
		t.Fatalf("unexpected message: %s", res.message)
	}
	if sawAuth != "Bearer deeprelay_live_x" {
		t.Errorf("Authorization = %q, want bearer key", sawAuth)
	}
	if res.plan != "DeepRelay" {
		t.Errorf("plan = %q, want DeepRelay", res.plan)
	}
	row, ok := res.quotas["Wallet Balance"].(map[string]any)
	if !ok {
		t.Fatalf("missing Wallet Balance row: %v", res.quotas)
	}
	if row["total"] != 123.45 {
		t.Errorf("total = %v, want 123.45", row["total"])
	}
	if row["isCreditBalance"] != true || row["currency"] != "$" {
		t.Errorf("row = %v, want credit balance in dollars", row)
	}
}

// TestFetchDeepRelayUsage_FallsBackToCents keeps the row populated when the
// server omits balance_dollars but still reports cents.
func TestFetchDeepRelayUsage_FallsBackToCents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"balance_cents":500}`))
	}))
	t.Cleanup(srv.Close)
	stubDeepRelayBalance(t, srv.URL)

	res := fetchDeepRelayUsage(context.Background(), "k")
	row, ok := res.quotas["Wallet Balance"].(map[string]any)
	if !ok {
		t.Fatalf("missing Wallet Balance row: %v (message %q)", res.quotas, res.message)
	}
	if row["total"] != 5.0 {
		t.Errorf("total = %v, want 5.00 from 500 cents", row["total"])
	}
}

func TestFetchDeepRelayUsage_RejectedKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	stubDeepRelayBalance(t, srv.URL)

	res := fetchDeepRelayUsage(context.Background(), "bad")
	if res.plan != "DeepRelay" || res.message == "" {
		t.Errorf("want a DeepRelay message on 401, got plan=%q message=%q", res.plan, res.message)
	}
}

func TestFetchDeepRelayUsage_NoCredential(t *testing.T) {
	res := fetchDeepRelayUsage(context.Background(), "   ")
	if res.message == "" {
		t.Error("expected a message when no API key is present")
	}
}

func TestDeepRelayIsUsageEligible(t *testing.T) {
	if !strSliceContains(usageSupportedProviders, "deeprelay") {
		t.Error("deeprelay missing from usageSupportedProviders")
	}
	if !strSliceContains(usageApikeyProviders, "deeprelay") {
		t.Error("deeprelay missing from usageApikeyProviders")
	}
	if !isUsageEligibleConnection(testUsageConn("deeprelay", "apikey")) {
		t.Error("deeprelay connection should be usage eligible")
	}
}
