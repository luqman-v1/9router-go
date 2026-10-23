package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// refresh_dedup_test.go covers the shared dedup wrapper in Refresh:
// provider + SPENT token keying, in-flight collapsing, a 10s result TTL, and
// the rule that a failure is never cached — the four properties upstream's
// dedupRefresh gives every provider. Before this wrapper existed only
// minimax-code carried them, and the sequential rows here fail on that code:
// two non-concurrent refreshes of one account each post their own exchange.

// dedupTestProvider is a name no production code registers, so a stub under it
// cannot shadow or be shadowed by a real refresher.
const (
	dedupTestProvider     = "zz-test-dedup"
	dedupTestFailProvider = "zz-test-dedup-fail"
)

// countingRefresher registers a refresher under name that performs one real
// form POST to server and counts it. The count is the number of exchanges.
func countingRefresher(t *testing.T, name string, hits *atomic.Int32, status int, body string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	Register(name, func(ctx context.Context, p *Params) (*TokenResult, error) {
		return doFormRefresh(ctx, server.Client(), server.URL, url.Values{})
	})
	t.Cleanup(func() { Unregister(name) })
}

func TestRefreshSharedDedup_Sequential(t *testing.T) {
	var hits atomic.Int32
	countingRefresher(t, dedupTestProvider, &hits, http.StatusOK, `{"access_token":"a","refresh_token":"rt-new","expires_in":60}`)
	resetRefreshCache()

	p := &Params{Provider: dedupTestProvider, RefreshToken: "spent-token"}
	first, err := Refresh(context.Background(), p)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	second, err := Refresh(context.Background(), p)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if first.AccessToken != "a" || second.AccessToken != "a" {
		t.Fatalf("sequential refreshes gave %q then %q, want both %q", first.AccessToken, second.AccessToken, "a")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("reached the token endpoint %d times, want 1: the second refresh must reuse the first's completed result", got)
	}
	// The two callers must not share one pointer: a caller that mutates its
	// answer must not change what the next caller reads.
	if first == second {
		t.Errorf("sequential refreshes returned the same pointer; each caller needs its own copy")
	}
	first.AccessToken = "mutated"
	third, _ := Refresh(context.Background(), p)
	if third.AccessToken != "a" {
		t.Errorf("mutating one caller's answer changed the cached answer: got %q, want %q", third.AccessToken, "a")
	}
}

func TestRefreshSharedDedup_Concurrent(t *testing.T) {
	var hits atomic.Int32
	countingRefresher(t, dedupTestProvider, &hits, http.StatusOK, `{"access_token":"b","expires_in":60}`)
	resetRefreshCache()

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = Refresh(context.Background(), &Params{Provider: dedupTestProvider, RefreshToken: "cd-token"})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("reached the token endpoint %d times, want 1: concurrent callers on one spent token must share one flight", got)
	}
}

// A failure is never cached: the next attempt reaches the endpoint again.
func TestRefreshSharedDedup_FailureNotCached(t *testing.T) {
	var hits atomic.Int32
	countingRefresher(t, dedupTestFailProvider, &hits, http.StatusBadRequest, `{"error":"invalid_grant"}`)
	resetRefreshCache()

	p := &Params{Provider: dedupTestFailProvider, RefreshToken: "spent-token"}
	if _, err := Refresh(context.Background(), p); err == nil {
		t.Fatalf("refresh against a 400 endpoint returned no error")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("reached the token endpoint %d times on the failing call, want 1", got)
	}
	if _, err := Refresh(context.Background(), p); err == nil {
		t.Fatalf("second refresh returned no error")
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("reached the token endpoint %d times after two failing calls, want 2: a failure must leave nothing cached", got)
	}
}

// Provider isolation (AGENTS.md §3.A): two providers that hold the same spent
// token value never share an answer, because the key carries the provider.
func TestRefreshSharedDedup_ProviderIsolation(t *testing.T) {
	var hitsA, hitsB atomic.Int32
	countingRefresher(t, "zz-test-dedup-a", &hitsA, http.StatusOK, `{"access_token":"token-a","expires_in":60}`)
	countingRefresher(t, "zz-test-dedup-b", &hitsB, http.StatusOK, `{"access_token":"token-b","expires_in":60}`)
	resetRefreshCache()

	const shared = "identical-spent-token"
	resA, err := Refresh(context.Background(), &Params{Provider: "zz-test-dedup-a", RefreshToken: shared})
	if err != nil {
		t.Fatalf("provider a: %v", err)
	}
	resB, err := Refresh(context.Background(), &Params{Provider: "zz-test-dedup-b", RefreshToken: shared})
	if err != nil {
		t.Fatalf("provider b: %v", err)
	}
	if resA.AccessToken == resB.AccessToken {
		t.Errorf("two providers shared an answer (%q): the key must carry the provider", resA.AccessToken)
	}
	if hitsA.Load() != 1 || hitsB.Load() != 1 {
		t.Errorf("hits a=%d b=%d, want 1 each: a cross-provider cache hit would hand one provider another's token", hitsA.Load(), hitsB.Load())
	}
}

// An empty spent token cannot be deduped — there is no key — so the call goes
// straight through, matching upstream's `if (!oldToken) return fn()`.
func TestRefreshSharedDedup_EmptyTokenPassesThrough(t *testing.T) {
	var hits atomic.Int32
	countingRefresher(t, dedupTestProvider, &hits, http.StatusOK, `{"access_token":"c","expires_in":60}`)
	resetRefreshCache()

	for i := 0; i < 2; i++ {
		if _, err := Refresh(context.Background(), &Params{Provider: dedupTestProvider, RefreshToken: ""}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("reached the token endpoint %d times with an empty token, want 2 (no key to dedup on)", got)
	}
}

// An unknown provider still reports the same error the unwrapped path did.
func TestRefreshSharedDedup_UnknownProvider(t *testing.T) {
	resetRefreshCache()
	_, err := Refresh(context.Background(), &Params{Provider: "zz-test-not-registered", RefreshToken: "t"})
	if err == nil {
		t.Fatalf("Refresh for an unregistered provider returned no error")
	}
	if want := "no OAuth refresher for: zz-test-not-registered"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// An expired entry is not served and is swept on the way out.
func TestRefreshCache_ExpiredEntryIsNotServed(t *testing.T) {
	resetRefreshCache()
	refreshCacheStore("prov:tok", &TokenResult{AccessToken: "fresh"})

	refreshCacheMu.Lock()
	entry := refreshCache["prov:tok"]
	entry.expiresAt = time.Now().Add(-time.Second)
	refreshCache["prov:tok"] = entry
	refreshCacheMu.Unlock()

	if got := refreshCacheGet("prov:tok"); got != nil {
		t.Errorf("refreshCacheGet served an expired entry: %+v", got)
	}
	refreshCacheMu.Lock()
	_, stillThere := refreshCache["prov:tok"]
	refreshCacheMu.Unlock()
	if stillThere {
		t.Errorf("an expired entry read left the map entry behind; the sweep must delete it")
	}
}

// The map is bounded: a gateway serving many connections must not keep every
// spent token alive (AGENTS.md §4.G). The ceiling evicts the oldest, and the
// expired-entry sweep runs first.
func TestRefreshCache_Bounded(t *testing.T) {
	resetRefreshCache()
	for i := 0; i < refreshCacheMaxEntries+50; i++ {
		key := "prov:tok-" + strconv.Itoa(i)
		refreshCacheStore(key, &TokenResult{AccessToken: "a"})
	}
	refreshCacheMu.Lock()
	got := len(refreshCache)
	refreshCacheMu.Unlock()
	if got > refreshCacheMaxEntries {
		t.Errorf("cache holds %d entries, want at most %d (refreshCacheMaxEntries)", got, refreshCacheMaxEntries)
	}
	// The key just written must still be there: the ceiling evicts the oldest
	// live entry, never the new one.
	last := "prov:tok-" + strconv.Itoa(refreshCacheMaxEntries+49)
	if refreshCacheGet(last) == nil {
		t.Errorf("the entry just stored (%s) was evicted by the ceiling", last)
	}
}

// Guard: errors.Is must still see a wrapped provider error through Refresh.
func TestRefreshSharedDedup_ErrorPassesThrough(t *testing.T) {
	sentinel := errors.New("sentinel")
	Register("zz-test-dedup-err", func(context.Context, *Params) (*TokenResult, error) { return nil, sentinel })
	t.Cleanup(func() { Unregister("zz-test-dedup-err") })
	resetRefreshCache()

	_, err := Refresh(context.Background(), &Params{Provider: "zz-test-dedup-err", RefreshToken: "tok"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Refresh wrapped the provider error; errors.Is(err, sentinel) = false (err: %v)", err)
	}
}
