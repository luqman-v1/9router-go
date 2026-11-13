package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchFreebuffUsage_MapsFreebucksAndActiveSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/freebuff/session" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fb-secret-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "active",
			"currentModel": "deepseek/deepseek-v4-flash",
			"expiresAt": "2026-11-14T05:00:00Z",
			"tier": "free",
			"freebucks": {
				"daily": {
					"limit": 100,
					"spent": 25,
					"remaining": 75,
					"resetAt": "2026-11-14T12:00:00Z"
				},
				"wallet": {
					"balance": 50
				}
			}
		}`))
	}))
	defer srv.Close()

	prevClient := freebuffUsageClient
	freebuffUsageClient = srv.Client()
	defer func() { freebuffUsageClient = prevClient }()

	res := fetchFreebuffUsage(t.Context(), "fb-secret-token", srv.URL)
	if res.message != "" {
		t.Fatalf("unexpected message: %s", res.message)
	}
	if res.plan != "free" {
		t.Errorf("plan = %q, want free", res.plan)
	}

	daily, ok := res.quotas["daily"].(map[string]any)
	if !ok {
		t.Fatalf("missing daily quota row: %+v", res.quotas)
	}
	if daily["used"] != 25.0 || daily["total"] != 100.0 || daily["remaining"] != 75.0 {
		t.Errorf("unexpected daily quota: %+v", daily)
	}
	if daily["currency"] != "Freebucks" {
		t.Errorf("currency = %v, want Freebucks", daily["currency"])
	}

	wallet, ok := res.quotas["wallet"].(map[string]any)
	if !ok {
		t.Fatalf("missing wallet quota row: %+v", res.quotas)
	}
	if wallet["remaining"] != 50.0 || wallet["isCreditBalance"] != true {
		t.Errorf("unexpected wallet quota: %+v", wallet)
	}

	session, ok := res.quotas["active_session"].(map[string]any)
	if !ok {
		t.Fatalf("missing active_session quota row: %+v", res.quotas)
	}
	if session["used"] != 1 || session["total"] != 1 {
		t.Errorf("unexpected active_session: %+v", session)
	}
}

func TestFetchFreebuffUsage_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	prevClient := freebuffUsageClient
	freebuffUsageClient = srv.Client()
	defer func() { freebuffUsageClient = prevClient }()

	res := fetchFreebuffUsage(t.Context(), "bad-token", srv.URL)
	if !strings.Contains(res.message, "sign in again") {
		t.Errorf("expected sign in again message, got %q", res.message)
	}
}

func TestFetchFreebuffUsage_EmptyCredential(t *testing.T) {
	res := fetchFreebuffUsage(t.Context(), "", "https://www.codebuff.com")
	if !strings.Contains(res.message, "no credential") {
		t.Errorf("expected no credential message, got %q", res.message)
	}
}

func TestFreebuffUsage_RegisteredInSupportedProviders(t *testing.T) {
	if !strSliceContains(usageSupportedProviders, "freebuff") {
		t.Error("freebuff missing from usageSupportedProviders")
	}
	if !strSliceContains(usageApikeyProviders, "freebuff") {
		t.Error("freebuff missing from usageApikeyProviders")
	}
}
