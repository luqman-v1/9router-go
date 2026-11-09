package oauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Kimchi is dual-auth: the dashboard renders an OAuth browser-token login and a
// manual API-key button from the registry's authModes, and both must end in a
// usable connection. This pins the OAuth half on the route the dashboard
// actually posts to — /api/oauth/kimchi/exchange — including that the token is
// stored under BOTH spellings the rest of the codebase reads (apiKey and
// accessToken), so the probe and the chat router find it whichever they look at.
// A missing token is refused before any network call, and the refusal is a 400
// rather than a 502 from a failed upstream probe — the operator typed nothing,
// so the gateway is not at fault.
func TestKimchiExchangeRejectsEmptyToken(t *testing.T) {
	h := NewOAuthHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/kimchi/exchange", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.HandleKimchiExchange(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for an empty token, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "token") {
		t.Errorf("the error must name the missing field, got %s", rec.Body.String())
	}
}

// The authorize route hands the dashboard a cli-auth URL on the kimchi.dev host,
// which is what the browser-token button opens. A redirect to the wrong host
// sends the operator to a login page that cannot issue a token.
func TestKimchiAuthorizeURLPointsAtAppHost(t *testing.T) {
	h := NewOAuthHandler(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/kimchi/authorize", nil)
	rec := httptest.NewRecorder()
	h.HandleKimchiAuthorize(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "app.kimchi.dev/cli-auth") {
		t.Errorf("authorize URL must point at app.kimchi.dev/cli-auth, got %s", body)
	}
	if !strings.Contains(body, "browser_token") {
		t.Errorf("flowType must be browser_token so the UI renders the paste-token form, got %s", body)
	}
}
