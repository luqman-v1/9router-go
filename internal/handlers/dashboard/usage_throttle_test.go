package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"9router/proxy/internal/fetchgate"
)

// usageFetchGap is the floor these tests assert on. The production gate is
// 250ms; a test-sized gap keeps the suite fast while still sitting far above
// the scheduler noise a zero-gap burst would show.
const usageFetchGap = 40 * time.Millisecond

// hitRecorder timestamps every request a fake provider receives, labelled by
// the endpoint that received it. The gap between consecutive hits on one
// endpoint is the observable the gate exists to widen.
type hitRecorder struct {
	mu      sync.Mutex
	entries []hit
}

type hit struct {
	label string
	at    time.Time
}

func (h *hitRecorder) mark(label string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, hit{label: label, at: time.Now()})
}

// times returns the hit instants for one endpoint, earliest first.
func (h *hitRecorder) times(label string) []time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []time.Time
	for _, e := range h.entries {
		if e.label == label {
			out = append(out, e.at)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func (h *hitRecorder) count(label string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var n int
	for _, e := range h.entries {
		if e.label == label {
			n++
		}
	}
	return n
}

// fastQuotaGate swaps the production gate for a test-sized one and restores it
// afterwards. The gate is process-wide on purpose — that is what bounds a burst
// spanning several connections — so tests asserting on it must not run in
// parallel with each other.
func fastQuotaGate(t *testing.T) {
	t.Helper()
	prev := quotaFetchGate
	quotaFetchGate = fetchgate.New(usageFetchGap, 0)
	t.Cleanup(func() { quotaFetchGate = prev })
}

// assertSpaced fails when the reads were not paced — the exact shape that got
// ten accounts on one IP rate-limited in issue #30.
//
// It asserts on the MEAN gap (span / (n-1)), not on each gap in isolation. A
// per-gap check measures wall-clock wake-ups, and the previous goroutine's
// wake-up lateness is charged to the next gap: under -race with the rest of the
// suite loaded, a correctly-behaving gate reads 33ms against a 40ms floor (and
// 29.77ms against a 30ms one), at indices following no pattern. Averaging
// cancels that noise instead of buying margin against it.
//
// The floor sits between the two outcomes rather than at the configured gap.
// Measured on this machine with the 40ms gate: paced reads average ~39.8ms
// apart, unpaced ones ~4.3ms. 20ms clears the unpaced case by ~5x and clears
// the paced one by ~2x, so the assertion survives a loaded scheduler in either
// direction. Requiring the exact 40ms would assert the timer fired on time,
// which is not what the gate promises.
func assertSpaced(t *testing.T, hits []time.Time, minGap time.Duration) {
	t.Helper()
	if len(hits) < 2 {
		return
	}
	gaps := len(hits) - 1
	mean := hits[len(hits)-1].Sub(hits[0]) / time.Duration(gaps)
	if floor := minGap / 2; mean < floor {
		t.Errorf("%d upstream reads averaged %s apart (span %s), want at least %s — the burst was not paced",
			len(hits), mean, hits[len(hits)-1].Sub(hits[0]), floor)
	}
}

// One tick of the quota tracker fans out to every visible connection in
// parallel. Each of those requests must reach its provider spaced out, not in
// a single burst — the failure mode reported in issue #30.
func TestHandleGetConnectionUsage_SpacesBurstAcrossConnections(t *testing.T) {
	fastQuotaGate(t)

	rec := &hitRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/usage", func(w http.ResponseWriter, r *http.Request) {
		rec.mark("usage")
		_, _ = w.Write([]byte(`{"limits":{"session":{"usage":0.1},"weekly":{"usage":0.2}}}`))
	})
	mux.HandleFunc("/api/me", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Plan":"pro"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	prevUsage, prevMe := ollamaUsageURL, ollamaMeURL
	ollamaUsageURL, ollamaMeURL = srv.URL+"/api/usage", srv.URL+"/api/me"
	t.Cleanup(func() { ollamaUsageURL, ollamaMeURL = prevUsage, prevMe })

	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	var ids []string
	for i := range 4 {
		id := fmt.Sprintf("ollama-%d", i)
		if err := repo.CreateProviderConnection(context.Background(), id, "ollama", "api_key", id, fmt.Sprintf("key-%d", i)); err != nil {
			t.Fatalf("seed connection %s: %v", id, err)
		}
		ids = append(ids, id)
	}

	router := setupTestRouter(repo)

	// Exactly what QuotaTrackerView does on auto-refresh: one parallel fan-out.
	codes := fireConcurrentUsageReads(t, router, ids)
	for _, id := range ids {
		if codes[id] != http.StatusOK {
			t.Errorf("%s: status %d, want 200", id, codes[id])
		}
	}

	hits := rec.times("usage")
	if len(hits) < len(ids) {
		t.Fatalf("provider saw %d reads for %d connections", len(hits), len(ids))
	}
	assertSpaced(t, hits, usageFetchGap)
}

// The Antigravity branch does not go through fetchProviderUsage, so the gate
// has to cover it too: it is the provider in the #30 report, and one read
// costs two RPCs.
func TestHandleGetConnectionUsage_SpacesAntigravityBurst(t *testing.T) {
	fastQuotaGate(t)

	rec := &hitRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1internal:loadCodeAssist", func(w http.ResponseWriter, r *http.Request) {
		rec.mark("loadCodeAssist")
		_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj","currentTier":{"name":"Free"}}`))
	})
	mux.HandleFunc("/v1internal:fetchAvailableModels", func(w http.ResponseWriter, r *http.Request) {
		rec.mark("fetchAvailableModels")
		_, _ = w.Write([]byte(`{"models":{"gemini-3.8-flash-high":{"quotaInfo":{"remainingFraction":0.5}}}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	prevProd, prevDaily := antigravityDashboardBaseURL, antigravityDailyBaseURL
	antigravityDashboardBaseURL, antigravityDailyBaseURL = srv.URL, srv.URL
	t.Cleanup(func() {
		antigravityDashboardBaseURL, antigravityDailyBaseURL = prevProd, prevDaily
	})

	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	var ids []string
	for i := range 4 {
		id := fmt.Sprintf("ag-%d", i)
		data := fmt.Sprintf(`{"accessToken":"tok-%d","projectId":"proj-%d"}`, i, i)
		if err := repo.CreateProviderConnectionFull(context.Background(), id, "antigravity", "oauth", id, nil, data); err != nil {
			t.Fatalf("seed connection %s: %v", id, err)
		}
		ids = append(ids, id)
	}

	router := setupTestRouter(repo)

	codes := fireConcurrentUsageReads(t, router, ids)
	for _, id := range ids {
		if codes[id] != http.StatusOK {
			t.Errorf("%s: status %d, want 200", id, codes[id])
		}
	}

	// One account's two RPCs stay back to back — that is upstream's own
	// sequence, and a single account is not the burst. What has to be spaced
	// is the same call repeated for the next account on the same egress IP.
	for _, label := range []string{"loadCodeAssist", "fetchAvailableModels"} {
		hits := rec.times(label)
		if len(hits) != len(ids) {
			t.Fatalf("%s: provider saw %d calls for %d connections", label, len(hits), len(ids))
		}
		assertSpaced(t, hits, usageFetchGap)
	}
}

// A client that goes away while queued must not trigger the fetch at all: the
// slot is released and the handler returns instead of spending an upstream
// request on a response nobody reads.
func TestHandleGetConnectionUsage_QueuedClientGoneFetchesNothing(t *testing.T) {
	fastQuotaGate(t)

	rec := &hitRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mark("any")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	prevProd, prevDaily := antigravityDashboardBaseURL, antigravityDailyBaseURL
	antigravityDashboardBaseURL, antigravityDailyBaseURL = srv.URL, srv.URL
	t.Cleanup(func() {
		antigravityDashboardBaseURL, antigravityDailyBaseURL = prevProd, prevDaily
	})

	repo, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)

	if err := repo.CreateProviderConnectionFull(context.Background(), 
		"ag-cancel", "antigravity", "oauth", "ag-cancel", nil, `{"accessToken":"tok","projectId":"proj"}`,
	); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	router := setupTestRouter(repo)

	// Occupy the gate so the next request has to queue behind it.
	if err := quotaFetchGate.Acquire(t.Context()); err != nil {
		t.Fatalf("occupy gate: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodGet, "/api/usage/ag-cancel", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(recorder, req)
	}()

	// Let the request reach the gate before pulling the client away.
	time.Sleep(usageFetchGap / 4)
	cancel()
	<-done

	if rec.count("any") != 0 {
		t.Errorf("provider was called %d times for a request the client abandoned", rec.count("any"))
	}
}

func fireConcurrentUsageReads(t *testing.T, router chi.Router, ids []string) map[string]int {
	t.Helper()

	var (
		mu    sync.Mutex
		codes = make(map[string]int, len(ids))
		wg    sync.WaitGroup
	)
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/usage/"+id, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			mu.Lock()
			codes[id] = rec.Code
			mu.Unlock()
		}()
	}
	wg.Wait()
	return codes
}
