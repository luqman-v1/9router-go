package oauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/db"
)

func TestHandleDeviceStart_qoderLocal(t *testing.T) {
	// Qoder start is fully local (no network): PKCE + nonce + URLs.
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/device/start", strings.NewReader(`{"provider":"qoder"}`))
	rec := httptest.NewRecorder()
	handler.HandleDeviceStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		`"device_code":`, `"user_code":`, `qoder.com/device/selectAccounts`,
		`"interval":2`, `"session":`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestHandleDeviceStart_bad(t *testing.T) {
	handler := NewOAuthHandler(nil)
	for _, payload := range []string{`{"provider":"google"}`, `{"provider":"kiro","region":"evil;id"}`} {
		req := httptest.NewRequest("POST", "/api/oauth/device/start", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		handler.HandleDeviceStart(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("expected error for %s, got %s", payload, rec.Body.String())
		}
	}
}

func TestHandleDevicePoll_validation(t *testing.T) {
	handler := NewOAuthHandler(nil)
	for _, payload := range []string{`{}`, `{"provider":"qoder"}`, `{"provider":"nope","device_code":"x"}`} {
		req := httptest.NewRequest("POST", "/api/oauth/device/poll", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		handler.HandleDevicePoll(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for %s, got %d", payload, rec.Code)
		}
	}
}

func TestKimiHeaders_deviceId(t *testing.T) {
	h := kimiHeaders("dev-1")
	if h["X-Msh-Device-Id"] != "dev-1" || h["X-Msh-Platform"] != "9router" {
		t.Errorf("bad kimi headers: %v", h)
	}
}

func TestHandleDevicePoll_PendingDoesNotInsert(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewOAuthHandler(repo)

	// Qoder poll with nonexistent/unauthorized device code returns pending
	req := httptest.NewRequest("POST", "/api/oauth/device/poll", strings.NewReader(`{"provider":"qoder","device_code":"pending-code","session":{"verifier":"v"}}`))
	rec := httptest.NewRecorder()
	handler.HandleDevicePoll(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if !strings.Contains(rec.Body.String(), `"status":"pending"`) && !strings.Contains(rec.Body.String(), `"status":"error"`) {
		t.Errorf("expected pending or error status, got: %s", rec.Body.String())
	}

	var count int
	err := repo.RawDB().QueryRow("SELECT COUNT(*) FROM providerConnections").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query providerConnections: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 connections inserted while pending, got %d", count)
	}
}

type deviceRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f deviceRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHandleDevicePoll_SlowDownInterval(t *testing.T) {
	origTransport := deviceClient.Transport
	deviceClient.Transport = deviceRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "application/json")
		rec.WriteHeader(http.StatusOK)
		_, _ = rec.WriteString(`{"error":"slow_down","interval":15}`)
		return rec.Result(), nil
	})
	t.Cleanup(func() { deviceClient.Transport = origTransport })

	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/api/oauth/device/poll", strings.NewReader(`{"provider":"github","device_code":"test-code"}`))
	rec := httptest.NewRecorder()
	handler.HandleDevicePoll(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"pending"`) {
		t.Errorf("expected status:pending in %s", body)
	}
	if !strings.Contains(body, `"interval":15`) {
		t.Errorf("expected interval:15 in %s", body)
	}
}
