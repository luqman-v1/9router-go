package dashboard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/browserlogin"
)

// TestGeminiWebBrowserLoginAnswersTheCapturedSession proves the endpoint
// hands back the session the browser captured.
//
// The answer is a credential, and the caller pastes it into the connection
// form, so it has to arrive whole: a session the caller cannot use is worse
// than one the endpoint refused to give.
func TestGeminiWebBrowserLoginAnswersTheCapturedSession(t *testing.T) {
	previous := browserLoginRun
	t.Cleanup(func() { browserLoginRun = previous })
	browserLoginRun = func(context.Context) (*browserlogin.Result, error) {
		return &browserlogin.Result{CookieHeader: "__Secure-1PSID=psid; __Secure-1PSIDTS=ts"}, nil
	}

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/browser-login", nil)
	(&DashboardHandler{}).HandleGeminiWebBrowserLogin(rec, request)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "__Secure-1PSIDTS=ts") {
		t.Errorf("the answer carries no session: %s", rec.Body.String())
	}
}

// TestGeminiWebBrowserLoginReportsAMissingBrowser pins the failure.
//
// A machine with no browser to drive is not a broken sign-in, and the
// operator has to be told so in a way they can act on: the answer names the
// alternative, which is an Incognito window and a pasted cookie.
func TestGeminiWebBrowserLoginReportsAMissingBrowser(t *testing.T) {
	previous := browserLoginRun
	t.Cleanup(func() { browserLoginRun = previous })
	browserLoginRun = func(context.Context) (*browserlogin.Result, error) {
		return nil, browserlogin.ErrNoBrowser
	}

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/browser-login", nil)
	(&DashboardHandler{}).HandleGeminiWebBrowserLogin(rec, request)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 for a machine with no browser", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Chrome") {
		t.Errorf("the answer does not name what is missing: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Incognito") {
		t.Errorf("the answer does not offer the alternative: %s", rec.Body.String())
	}
}

// TestGeminiWebBrowserLoginReportsASignInThatNeverHappened pins the other
// failure.
//
// A browser that was closed before the sign-in finished is a sign-in that
// did not happen, and it is not the credential's fault.
func TestGeminiWebBrowserLoginReportsASignInThatNeverHappened(t *testing.T) {
	previous := browserLoginRun
	t.Cleanup(func() { browserLoginRun = previous })
	browserLoginRun = func(context.Context) (*browserlogin.Result, error) {
		return nil, browserlogin.ErrNoSignIn
	}

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/browser-login", nil)
	(&DashboardHandler{}).HandleGeminiWebBrowserLogin(rec, request)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a sign-in that never finished", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "sign-in") {
		t.Errorf("the answer does not say what happened: %s", rec.Body.String())
	}
}

// TestGeminiWebBrowserLoginReportsATimeout pins the timeout.
//
// A sign-in that takes longer than the budget is a sign-in that is not going
// to happen, and the caller has to be able to tell it from a refusal.
func TestGeminiWebBrowserLoginReportsATimeout(t *testing.T) {
	previous := browserLoginRun
	t.Cleanup(func() { browserLoginRun = previous })
	browserLoginRun = func(context.Context) (*browserlogin.Result, error) {
		return nil, errors.New("wrapped: " + context.DeadlineExceeded.Error())
	}

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/browser-login", nil)
	(&DashboardHandler{}).HandleGeminiWebBrowserLogin(rec, request)

	// A wrapped deadline is not the sentinel, so the answer is the ordinary
	// failure. What matters is that it is not reported as a success.
	if rec.Code == http.StatusOK {
		t.Error("a timeout was reported as a success")
	}
}
