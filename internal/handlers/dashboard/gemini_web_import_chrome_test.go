package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"9router/proxy/internal/browsercookies"
)

// stubBrowserCookiesRead stands in for the reading of a browser's cookies.
func stubBrowserCookiesRead(t *testing.T, result *browsercookies.Result, err error) {
	t.Helper()
	previous := browserCookiesRead
	t.Cleanup(func() { browserCookiesRead = previous })
	browserCookiesRead = func(context.Context, string) (*browsercookies.Result, error) {
		return result, err
	}
}

// stubBrowserCookiesFindDB stands in for the search for a browser's database.
//
// The search is a walk of the machine's own directories, and a test cannot
// rely on what is installed on the machine it runs on.
func stubBrowserCookiesFindDB(t *testing.T, path string, err error) {
	t.Helper()
	previous := browserCookiesFindDB
	t.Cleanup(func() { browserCookiesFindDB = previous })
	browserCookiesFindDB = func(string) (string, error) {
		return path, err
	}
}

// TestGeminiWebImportChromeAnswersTheSession proves the endpoint hands back
// the session it read.
//
// The answer is a credential the caller pastes into the connection form, so
// it has to arrive whole: a session the caller cannot use is worse than one
// the endpoint refused to give.
func TestGeminiWebImportChromeAnswersTheSession(t *testing.T) {
	stubBrowserCookiesFindDB(t, "/a-browser/Default/Network/Cookies", nil)
	stubBrowserCookiesRead(t, &browsercookies.Result{
		Header: "__Secure-1PSID=psid; __Secure-1PSIDTS=ts",
	}, nil)

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/import-chrome", strings.NewReader("{}"))
	(&DashboardHandler{}).HandleGeminiWebImportChrome(rec, request)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "__Secure-1PSIDTS=ts") {
		t.Errorf("the answer carries no session: %s", rec.Body.String())
	}
}

// TestGeminiWebImportChromeWarnsAboutARunningBrowser proves the trade is
// stated.
//
// A browser that is running keeps rotating the same session the router is
// about to renew, and the two cancel each other out — which signs the
// account out. The answer has to say so rather than hand over a session that
// is about to expire in silence.
func TestGeminiWebImportChromeWarnsAboutARunningBrowser(t *testing.T) {
	// A lock file next to the database is what a running browser leaves.
	dir := t.TempDir()
	profile := filepath.Join(dir, "Default")
	if err := os.MkdirAll(filepath.Join(profile, "Network"), 0o755); err != nil {
		t.Fatalf("make the profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profile, "SingletonLock"), nil, 0o600); err != nil {
		t.Fatalf("write the lock: %v", err)
	}
	dbPath := filepath.Join(profile, "Network", "Cookies")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatalf("write the database: %v", err)
	}

	stubBrowserCookiesFindDB(t, dbPath, nil)
	stubBrowserCookiesRead(t, &browsercookies.Result{
		Header: "__Secure-1PSID=psid",
	}, nil)

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/import-chrome", strings.NewReader("{}"))
	(&DashboardHandler{}).HandleGeminiWebImportChrome(rec, request)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "running") {
		t.Errorf("the answer does not warn about the running browser: %s", rec.Body.String())
	}
}

// TestGeminiWebImportChromeReportsAMissingBrowser proves the failure.
//
// A machine with no browser on it is not a broken read, and the operator has
// to be able to tell the two apart.
func TestGeminiWebImportChromeReportsAMissingBrowser(t *testing.T) {
	stubBrowserCookiesFindDB(t, "", errors.New("no browser cookie database was found for \"chrome\""))
	stubBrowserCookiesRead(t, nil, nil)

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/import-chrome", strings.NewReader("{}"))
	(&DashboardHandler{}).HandleGeminiWebImportChrome(rec, request)

	// No database at all is a 404: there is nothing to read.
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when there is no browser to read", rec.Code)
	}
}

// TestGeminiWebImportChromeReportsASealedDatabase proves the failure.
//
// A browser whose cookies are sealed and whose key cannot be read is a
// different problem from a browser with no session, and the two have
// different fixes. The status has to tell them apart.
func TestGeminiWebImportChromeReportsASealedDatabase(t *testing.T) {
	stubBrowserCookiesFindDB(t, "/a-browser/Default/Network/Cookies", nil)
	stubBrowserCookiesRead(t, nil, fmt.Errorf("the key could not be read: %w", browsercookies.ErrNoKey))

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/import-chrome", strings.NewReader("{}"))
	(&DashboardHandler{}).HandleGeminiWebImportChrome(rec, request)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 for a database whose key cannot be read", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "key") {
		t.Errorf("the answer does not name what is missing: %s", rec.Body.String())
	}
}

// TestGeminiWebImportChromeReportsAnEmptyProfile proves the failure.
//
// A browser that has never visited Google holds no session, and that is not
// a browser whose cookies could not be read.
func TestGeminiWebImportChromeReportsAnEmptyProfile(t *testing.T) {
	stubBrowserCookiesFindDB(t, "/a-browser/Default/Network/Cookies", nil)
	stubBrowserCookiesRead(t, &browsercookies.Result{Header: ""}, nil)

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/import-chrome", strings.NewReader("{}"))
	(&DashboardHandler{}).HandleGeminiWebImportChrome(rec, request)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a profile with no session", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "gemini.google.com") {
		t.Errorf("the answer does not say what to do: %s", rec.Body.String())
	}
}

// TestGeminiWebImportChromeTakesAProfile proves the profile is asked for.
//
// An operator who knows which browser holds the session should be able to
// name it, rather than hoping the guess is the right one.
func TestGeminiWebImportChromeTakesAProfile(t *testing.T) {
	var asked string
	previous := browserCookiesFindDB
	t.Cleanup(func() { browserCookiesFindDB = previous })
	browserCookiesFindDB = func(hint string) (string, error) {
		asked = hint
		return "/a-browser/Default/Network/Cookies", nil
	}
	stubBrowserCookiesRead(t, &browsercookies.Result{Header: "SID=abc"}, nil)

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/import-chrome",
		strings.NewReader(`{"profile":"chrome:Profile 4"}`))
	(&DashboardHandler{}).HandleGeminiWebImportChrome(rec, request)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if asked != "chrome:Profile 4" {
		t.Errorf("the search was asked for %q, want %q", asked, "chrome:Profile 4")
	}
}

// TestGeminiWebImportChromeRefusesABrokenBody proves the body is checked.
//
// A body that is not JSON is the caller's mistake, and the answer has to say
// so rather than reading the first browser on the machine.
func TestGeminiWebImportChromeRefusesABrokenBody(t *testing.T) {
	stubBrowserCookiesFindDB(t, "/a-browser/Default/Network/Cookies", nil)
	stubBrowserCookiesRead(t, &browsercookies.Result{Header: "SID=abc"}, nil)

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/providers/gemini-web/import-chrome",
		strings.NewReader("not json"))
	(&DashboardHandler{}).HandleGeminiWebImportChrome(rec, request)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a body that is not JSON", rec.Code)
	}
}
