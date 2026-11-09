package dashboard

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/browsercookies"
	"9router/proxy/internal/browserlogin"
	"9router/proxy/internal/handlerutil"
)

// The DeepSeek import answers with BOTH halves of the credential: the
// userToken the API authenticates with, and the site's own cookies the WAF
// reads. A response that carried only one of them would leave the caller
// with a credential the site does not accept.
func TestDeepSeekImportChromeAnswersWithTheWholeCredential(t *testing.T) {
	origRead := browserCookiesReadDomain
	origFind := browserCookiesFindDB
	origStorage := deepSeekLocalStorageRead
	t.Cleanup(func() {
		browserCookiesReadDomain = origRead
		browserCookiesFindDB = origFind
		deepSeekLocalStorageRead = origStorage
	})

	browserCookiesFindDB = func(string) (string, error) { return "/a-profile/Default/Cookies", nil }
	browserCookiesReadDomain = func(_ context.Context, _ string, domain string) (*browsercookies.Result, error) {
		if domain != "deepseek" {
			t.Errorf("read domain = %q, want %q", domain, "deepseek")
		}
		return &browsercookies.Result{
			Header: "ds_session_id=session-value; aws-waf-token=waf-value",
		}, nil
	}
	deepSeekLocalStorageRead = func(_, origin, key string) (string, error) {
		if origin != deepSeekLocalStorageOrigin {
			t.Errorf("origin = %q, want %q", origin, deepSeekLocalStorageOrigin)
		}
		if key != deepSeekLocalStorageKey {
			t.Errorf("key = %q, want %q", key, deepSeekLocalStorageKey)
		}
		return "the-user-token", nil
	}

	handler := &DashboardHandler{}
	rec := httptest.NewRecorder()
	handler.HandleDeepSeekWebImportChrome(rec, httptest.NewRequest(http.MethodPost, "/api/providers/deepseek-web/import-chrome", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		UserToken string `json:"userToken"`
		Cookie    string `json:"cookie"`
		Warning   string `json:"warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
	if out.UserToken != "the-user-token" {
		t.Errorf("userToken = %q, want the token read out of the profile", out.UserToken)
	}
	if !strings.Contains(out.Cookie, "aws-waf-token=waf-value") {
		t.Errorf("cookie = %q, want the site's own cookies", out.Cookie)
	}
}

// A profile that holds the cookies but not the token is a profile that is
// not signed in, and saying so is more useful than answering with half a
// credential.
func TestDeepSeekImportChromeReportsAMissingToken(t *testing.T) {
	origRead := browserCookiesReadDomain
	origFind := browserCookiesFindDB
	origStorage := deepSeekLocalStorageRead
	t.Cleanup(func() {
		browserCookiesReadDomain = origRead
		browserCookiesFindDB = origFind
		deepSeekLocalStorageRead = origStorage
	})

	browserCookiesFindDB = func(string) (string, error) { return "/a-profile/Default/Cookies", nil }
	browserCookiesReadDomain = func(context.Context, string, string) (*browsercookies.Result, error) {
		return &browsercookies.Result{Header: "ds_session_id=session-value"}, nil
	}
	deepSeekLocalStorageRead = func(string, string, string) (string, error) {
		return "", browsercookies.ErrNoLocalStorage
	}

	handler := &DashboardHandler{}
	rec := httptest.NewRecorder()
	handler.HandleDeepSeekWebImportChrome(rec, httptest.NewRequest(http.MethodPost, "/api/providers/deepseek-web/import-chrome", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "userToken") {
		t.Errorf("the answer = %q, want it to name what is missing", rec.Body.String())
	}
}

// A profile whose cookies are sealed and cannot be unsealed is a different
// problem from a profile that holds no session, and the operator needs to be
// told which one they have.
func TestDeepSeekImportChromeReportsAnUnreadableKeyring(t *testing.T) {
	origRead := browserCookiesReadDomain
	origFind := browserCookiesFindDB
	t.Cleanup(func() {
		browserCookiesReadDomain = origRead
		browserCookiesFindDB = origFind
	})

	browserCookiesFindDB = func(string) (string, error) { return "/a-profile/Default/Cookies", nil }
	browserCookiesReadDomain = func(context.Context, string, string) (*browsercookies.Result, error) {
		return nil, errors.Join(browsercookies.ErrNoKey, errors.New("the keyring is locked"))
	}

	handler := &DashboardHandler{}
	rec := httptest.NewRecorder()
	handler.HandleDeepSeekWebImportChrome(rec, httptest.NewRequest(http.MethodPost, "/api/providers/deepseek-web/import-chrome", nil))

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body: %s)", rec.Code, rec.Body.String())
	}
}

// The browser sign-in answers with the same two halves, for the same
// reason: one of them is the credential and the other is what the WAF
// reads.
func TestDeepSeekBrowserLoginAnswersWithTheWholeCredential(t *testing.T) {
	orig := deepSeekBrowserLoginRun
	t.Cleanup(func() { deepSeekBrowserLoginRun = orig })

	deepSeekBrowserLoginRun = func(context.Context) (*browserlogin.DeepSeekResult, error) {
		return &browserlogin.DeepSeekResult{UserToken: "the-user-token", CookieHeader: "ds_session_id=session-value"}, nil
	}

	handler := &DashboardHandler{}
	rec := httptest.NewRecorder()
	handler.HandleDeepSeekWebBrowserLogin(rec, httptest.NewRequest(http.MethodPost, "/api/providers/deepseek-web/browser-login", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var out struct {
		UserToken string `json:"userToken"`
		Cookie    string `json:"cookie"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
	if out.UserToken != "the-user-token" {
		t.Errorf("userToken = %q, want the token read out of the browser", out.UserToken)
	}
	if out.Cookie != "ds_session_id=session-value" {
		t.Errorf("cookie = %q, want the site's own cookies", out.Cookie)
	}
}

// The browser sign-in reports a machine with no browser as "not implemented"
// rather than as a bad request: the operator can install a browser, and
// nothing about their request is wrong.
func TestDeepSeekBrowserLoginReportsAMissingBrowser(t *testing.T) {
	orig := deepSeekBrowserLoginRun
	t.Cleanup(func() { deepSeekBrowserLoginRun = orig })

	deepSeekBrowserLoginRun = func(context.Context) (*browserlogin.DeepSeekResult, error) {
		return nil, browserlogin.ErrNoBrowser
	}

	handler := &DashboardHandler{}
	rec := httptest.NewRecorder()
	handler.HandleDeepSeekWebBrowserLogin(rec, httptest.NewRequest(http.MethodPost, "/api/providers/deepseek-web/browser-login", nil))

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body: %s)", rec.Code, rec.Body.String())
	}
}

// The import's own body is not read here — the endpoint takes the profile
// hint as an optional field, and a body that is not there means "the first
// browser on this machine".
func TestDeepSeekImportChromeAcceptsAnEmptyBody(t *testing.T) {
	origRead := browserCookiesReadDomain
	origFind := browserCookiesFindDB
	origStorage := deepSeekLocalStorageRead
	t.Cleanup(func() {
		browserCookiesReadDomain = origRead
		browserCookiesFindDB = origFind
		deepSeekLocalStorageRead = origStorage
	})

	browserCookiesFindDB = func(string) (string, error) { return "/a-profile/Default/Cookies", nil }
	browserCookiesReadDomain = func(context.Context, string, string) (*browsercookies.Result, error) {
		return &browsercookies.Result{Header: "ds_session_id=session-value"}, nil
	}
	deepSeekLocalStorageRead = func(string, string, string) (string, error) { return "the-user-token", nil }

	handler := &DashboardHandler{}
	rec := httptest.NewRecorder()
	handler.HandleDeepSeekWebImportChrome(rec, httptest.NewRequest(http.MethodPost, "/api/providers/deepseek-web/import-chrome", strings.NewReader("")))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

// The answer is JSON, and the token is a credential: it must not end up in
// a log line.
func TestDeepSeekImportChromeDoesNotLogTheCredential(t *testing.T) {
	origRead := browserCookiesReadDomain
	origFind := browserCookiesFindDB
	origStorage := deepSeekLocalStorageRead
	t.Cleanup(func() {
		browserCookiesReadDomain = origRead
		browserCookiesFindDB = origFind
		deepSeekLocalStorageRead = origStorage
	})

	browserCookiesFindDB = func(string) (string, error) { return "/a-profile/Default/Cookies", nil }
	browserCookiesReadDomain = func(context.Context, string, string) (*browsercookies.Result, error) {
		return &browsercookies.Result{Header: "ds_session_id=session-value"}, nil
	}
	deepSeekLocalStorageRead = func(string, string, string) (string, error) { return "the-user-token", nil }

	handler := &DashboardHandler{}
	rec := httptest.NewRecorder()
	handler.HandleDeepSeekWebImportChrome(rec, httptest.NewRequest(http.MethodPost, "/api/providers/deepseek-web/import-chrome", nil))

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
	if out["userToken"] != "the-user-token" {
		t.Errorf("userToken = %v, want the token", out["userToken"])
	}
}

// The envelope the import answers with is a plain object, so a client that
// reads it needs no special case.
func TestDeepSeekImportChromeAnswersWithAJSONObject(t *testing.T) {
	rec := httptest.NewRecorder()
	handlerutil.WriteJSON(rec, http.StatusOK, map[string]any{"userToken": "t", "cookie": "c"})

	var envelope map[string]jsontext.Value
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("the answer is not a JSON object: %v", err)
	}
	if _, ok := envelope["userToken"]; !ok {
		t.Error("the answer has no userToken field")
	}
}
