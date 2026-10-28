package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// geminiWebRotateFake is an accounts.google.com stand-in.
//
// One handler serves both rotation endpoints, so a test exercises the real
// sequence: the sentinel POST, and the iframe page and the POST that follows
// it. The frames are the ones a live session produced.
type geminiWebRotateFake struct {
	mu sync.Mutex

	// calls records the path and method of every request served, in order.
	calls []string
	// cookies are the Set-Cookie headers the renewal answers with.
	cookies []string
	// status overrides the HTTP status the renewal answers with.
	status int
	// pageBody is what the iframe page answers with. Empty means the page
	// carries no rotation id.
	pageBody string
	// pageCookies are the Set-Cookie headers the page answers with.
	pageCookies []string
}

func newGeminiWebRotateFake() *geminiWebRotateFake {
	return &geminiWebRotateFake{
		status:   http.StatusOK,
		pageBody: `init('rot-abc-123', 540, 540);`,
	}
}

func (f *geminiWebRotateFake) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
}

func (f *geminiWebRotateFake) callPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *geminiWebRotateFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	if strings.Contains(r.URL.Path, "RotateCookiesPage") {
		for _, cookie := range f.pageCookies {
			http.SetCookie(w, &http.Cookie{Name: cookieName(cookie), Value: cookieValue(cookie), Path: "/"})
		}
		w.Write([]byte(f.pageBody))
		return
	}
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	// The cookies are written before the status: a header written after
	// WriteHeader is dropped, which would make the renewal look like it
	// issued nothing.
	for _, cookie := range f.cookies {
		http.SetCookie(w, &http.Cookie{Name: cookieName(cookie), Value: cookieValue(cookie), Path: "/"})
	}
	w.WriteHeader(status)
}

func cookieName(cookie string) string {
	name, _, _ := strings.Cut(cookie, "=")
	return name
}

func cookieValue(cookie string) string {
	_, value, _ := strings.Cut(cookie, "=")
	return value
}

// TestGeminiWebRotationSchedule pins the gap between two renewals.
//
// The site answers a second call within the same minute with HTTP 429, so an
// account that is renewed too soon locks itself out. The gap the site asks
// for is the one that is used, with the grace added on top.
func TestGeminiWebRotationSchedule(t *testing.T) {
	geminiWebRotationMu.Lock()
	geminiWebRotationDue = map[string]time.Time{}
	geminiWebRotationMu.Unlock()

	const account = "conn-schedule"
	if !geminiWebRotationIsDue(account) {
		t.Error("an account that was never renewed is not due")
	}

	geminiWebRotationNote(account, 9*time.Minute)
	if geminiWebRotationIsDue(account) {
		t.Error("an account renewed a moment ago is due")
	}

	// The schedule holds the next renewal, so a lookup finds it in the
	// future rather than in the past.
	geminiWebRotationMu.Lock()
	next := geminiWebRotationDue[account]
	geminiWebRotationMu.Unlock()
	if !next.After(time.Now().Add(9 * time.Minute)) {
		t.Errorf("the next renewal is at %v, want it after the gap the site asked for", next)
	}

	geminiWebRotationForget(account)
	if !geminiWebRotationIsDue(account) {
		t.Error("an account whose schedule was dropped is not due")
	}
}

// TestGeminiWebRotationDropsTheScheduleOfADeletedConnection pins the pruning.
//
// A connection that is deleted leaves its entry behind otherwise, and the
// map would grow by one entry per connection that ever existed.
func TestGeminiWebRotationDropsTheScheduleOfADeletedConnection(t *testing.T) {
	geminiWebRotationMu.Lock()
	geminiWebRotationDue = map[string]time.Time{
		"live":      time.Now(),
		"deleted-1": time.Now(),
		"deleted-2": time.Now(),
	}
	geminiWebRotationMu.Unlock()

	stale := geminiWebRotationDueKeys(map[string]bool{"live": true})
	if len(stale) != 2 {
		t.Fatalf("the stale schedules = %v, want the two deleted connections", stale)
	}
	for _, id := range stale {
		geminiWebRotationForget(id)
	}
	geminiWebRotationMu.Lock()
	remaining := len(geminiWebRotationDue)
	geminiWebRotationMu.Unlock()
	if remaining != 1 {
		t.Errorf("the schedule map holds %d entries, want only the live connection", remaining)
	}
}

// TestGeminiWebRotationRenewsTheCredential proves the renewal reaches the
// endpoint and the renewed value comes back.
//
// The endpoints live on another host from the site, so the test points them
// at its own server: a renewal that could not be pointed anywhere would only
// ever be able to assert that a request was attempted.
func TestGeminiWebRotationRenewsTheCredential(t *testing.T) {
	fake := newGeminiWebRotateFake()
	fake.cookies = []string{"__Secure-1PSIDTS=new-timestamp"}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	post := server.URL + "/RotateCookies"
	page := server.URL + "/RotateCookiesPage"
	origin := server.URL
	restore := func(postURL, pageURL, rotateOrigin string) {
		geminiWebRotatePostURL, geminiWebRotatePageURL, geminiWebRotateOrigin = postURL, pageURL, rotateOrigin
	}
	previousPost, previousPage, previousOrigin := geminiWebRotatePostURL, geminiWebRotatePageURL, geminiWebRotateOrigin
	restore(post, page, origin)
	t.Cleanup(func() { restore(previousPost, previousPage, previousOrigin) })

	credential := "__Secure-1PSID=psid-value; __Secure-1PSIDTS=old-timestamp"
	session := geminiWebSessionForCredential(credential)
	result := geminiWebRotate(context.Background(), session, credential)

	if !result.Rotated {
		t.Fatalf("the rotation renewed nothing (status %d)", result.Status)
	}
	if !strings.Contains(result.Cookie, "__Secure-1PSIDTS=new-timestamp") {
		t.Errorf("the renewed credential = %q, want the new timestamp", result.Cookie)
	}
	if !strings.Contains(result.Cookie, "__Secure-1PSID=psid-value") {
		t.Errorf("the renewed credential = %q, want the cookies the site did not rotate kept", result.Cookie)
	}
	paths := fake.callPaths()
	if len(paths) == 0 || !strings.Contains(paths[0], "/RotateCookies") {
		t.Errorf("the renewal calls = %v, want the sentinel post first", paths)
	}
}

// TestGeminiWebRotationFallsBackToTheIframePage proves the second path is
// used when the first one renews nothing.
//
// The site answers a second call within the same minute with HTTP 429, so
// the paths are a fallback rather than a choice: the first one is tried, and
// the iframe path runs only when it came back without a renewed cookie.
func TestGeminiWebRotationFallsBackToTheIframePage(t *testing.T) {
	fake := newGeminiWebRotateFake()
	fake.status = http.StatusTooManyRequests
	fake.pageCookies = []string{"__Secure-1PSIDTS=page-timestamp"}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	previousPost, previousPage, previousOrigin := geminiWebRotatePostURL, geminiWebRotatePageURL, geminiWebRotateOrigin
	geminiWebRotatePostURL = server.URL + "/RotateCookies"
	geminiWebRotatePageURL = server.URL + "/RotateCookiesPage"
	geminiWebRotateOrigin = server.URL
	t.Cleanup(func() {
		geminiWebRotatePostURL, geminiWebRotatePageURL, geminiWebRotateOrigin = previousPost, previousPage, previousOrigin
	})

	credential := "__Secure-1PSID=psid-value; __Secure-1PSIDTS=old-timestamp"
	session := geminiWebSessionForCredential(credential)
	result := geminiWebRotate(context.Background(), session, credential)

	if !result.Rotated {
		t.Fatalf("the rotation renewed nothing (status %d)", result.Status)
	}
	if !strings.Contains(result.Cookie, "__Secure-1PSIDTS=page-timestamp") {
		t.Errorf("the renewed credential = %q, want the timestamp the page issued", result.Cookie)
	}
	paths := fake.callPaths()
	sawPage := false
	for _, path := range paths {
		if strings.Contains(path, "RotateCookiesPage") {
			sawPage = true
		}
	}
	if !sawPage {
		t.Errorf("the renewal calls = %v, want the iframe page to be read", paths)
	}
}
