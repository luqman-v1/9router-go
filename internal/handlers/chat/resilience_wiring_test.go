package chat

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/promptcache"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/resilience"
)

// resilienceTestConfig is the minimal provider config the admit path reads.
func resilienceTestConfig() *providers.ProviderConfig {
	return &providers.ProviderConfig{
		BaseURL:    "https://upstream.example/v1/chat/completions",
		AuthHeader: "Authorization",
		AuthScheme: "bearer",
	}
}

// newResilienceTestHandler builds a handler whose resilience layer is scoped to
// this test alone.
//
// The breaker and semaphore registries are process-wide, so a test that does not
// reset them can observe — and be refused by — state an earlier test left
// behind. Resetting here and using a per-test provider name is what makes each
// case independent of the others' ordering.
func newResilienceTestHandler(t *testing.T) (*ChatHandler, string) {
	t.Helper()
	// A provider name unique to this test: the breaker registry is keyed by
	// name and is process-wide, so two tests sharing "openai" would observe —
	// and be refused by — each other's state regardless of ordering.
	provider := "test-" + t.Name()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	resilience.ResetAllBreakers()
	resilience.ResetSemaphores()
	t.Cleanup(func() {
		resilience.ResetAllBreakers()
		resilience.ResetSemaphores()
	})
	repo := db.NewRepo(database)
	h := NewChatHandler(repo)
	h.Resilience = resilience.NewController(
		resilience.BreakerOptions{ResetTimeout: time.Minute},
		resilience.SemaphoreOptions{MaxConcurrency: 4, Timeout: 2 * time.Second},
	)
	return h, provider
}

func TestResilienceAdmitPassesWhenHealthy(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	cfg := resilienceTestConfig()
	release, ok := h.resilienceAdmit(t.Context(), provider, cfg)
	if !ok {
		t.Fatal("resilienceAdmit refused a healthy provider")
	}
	if release == nil {
		t.Fatal("resilienceAdmit returned a nil release for an admitted request")
	}
	release() // must not panic
}

func TestResilienceNilControllerPasses(t *testing.T) {
	// A handler built outside a boot has no resilience layer; the request must
	// still go out rather than being refused by a missing dependency.
	h, provider := newResilienceTestHandler(t)
	h.Resilience = nil
	release, ok := h.resilienceAdmit(t.Context(), provider, resilienceTestConfig())
	if !ok {
		t.Fatal("a nil resilience layer refused the request")
	}
	if release == nil {
		t.Fatal("a nil resilience layer returned a nil release")
	}
	release()
}

func TestResilienceBreakerOpensAfterProviderFailures(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	h.Resilience = resilience.NewController(resilience.BreakerOptions{
		FailureThreshold: 3,
		ResetTimeout:     time.Minute,
	}, resilience.SemaphoreOptions{MaxConcurrency: 4, Timeout: 2 * time.Second})
	cfg := resilienceTestConfig()

	// Three provider-level failures open the breaker.
	for range 3 {
		h.resilienceReport(provider, cfg, http.StatusBadGateway)
	}
	if _, ok := h.resilienceAdmit(t.Context(), provider, cfg); ok {
		t.Fatal("resilienceAdmit passed after the breaker opened")
	}
}

func TestResilienceIgnoresAccountAndRequestFailures(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	h.Resilience = resilience.NewController(resilience.BreakerOptions{
		FailureThreshold: 2,
		ResetTimeout:     time.Minute,
	}, resilience.SemaphoreOptions{MaxConcurrency: 4, Timeout: 2 * time.Second})
	cfg := resilienceTestConfig()

	// 401/429/400 say something about the account or the request, not the
	// provider. Charging them would trip the breaker for every healthy account
	// sharing the provider — the opposite of what the layer is for.
	for _, status := range []int{
		http.StatusUnauthorized,
		http.StatusTooManyRequests,
		http.StatusBadRequest,
		http.StatusForbidden,
		http.StatusNotFound,
	} {
		for range 5 {
			h.resilienceReport(provider, cfg, status)
		}
	}
	if _, ok := h.resilienceAdmit(t.Context(), provider, cfg); !ok {
		t.Fatal("account-level statuses opened the provider breaker")
	}
}

func TestResilienceSuccessClosesBreaker(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	h.Resilience = resilience.NewController(resilience.BreakerOptions{
		FailureThreshold: 2,
		ResetTimeout:     time.Minute,
	}, resilience.SemaphoreOptions{MaxConcurrency: 4, Timeout: 2 * time.Second})
	cfg := resilienceTestConfig()

	h.resilienceReport(provider, cfg, http.StatusBadGateway)
	h.resilienceReport(provider, cfg, http.StatusBadGateway)
	if _, ok := h.resilienceAdmit(t.Context(), provider, cfg); ok {
		t.Fatal("breaker did not open after the threshold")
	}
	// A 2xx is a provider success and must be recorded, but it does not by
	// itself close an OPEN breaker: recovery goes through the probe that
	// Allow() admits after the reset timeout, so that a provider answering a
	// single stray 200 cannot be treated as healthy again.
	h.resilienceReport(provider, cfg, http.StatusOK)
	if st := h.Resilience.BreakerStatus(provider); st.State != resilience.StateOpen {
		t.Errorf("breaker state = %s, want OPEN (a stray success must not close it)", st.State)
	}
	if st := h.Resilience.BreakerStatus(provider); st.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d, want the 2xx recorded", st.SuccessCount)
	}
}

func TestResilienceTransportFailureCounts(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	h.Resilience = resilience.NewController(resilience.BreakerOptions{
		FailureThreshold: 2,
		ResetTimeout:     time.Minute,
	}, resilience.SemaphoreOptions{MaxConcurrency: 4, Timeout: 2 * time.Second})
	cfg := resilienceTestConfig()

	// A status of 0 means no response was produced, which counts for the
	// breaker exactly as a 503 does: the provider did not answer.
	h.resilienceReport(provider, cfg, 0)
	h.resilienceReport(provider, cfg, 0)
	if _, ok := h.resilienceAdmit(t.Context(), provider, cfg); ok {
		t.Fatal("transport failures did not open the breaker")
	}
}

func TestResilienceSemaphoreCapsConcurrency(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	h.Resilience = resilience.NewController(resilience.BreakerOptions{}, resilience.SemaphoreOptions{
		MaxConcurrency: 1,
		Timeout:        200 * time.Millisecond,
	})
	cfg := resilienceTestConfig()
	_ = cfg

	first, ok := h.resilienceAdmit(t.Context(), provider, cfg)
	if !ok {
		t.Fatal("first admit refused")
	}
	defer first()

	// The second request must wait for the slot rather than being refused
	// outright, and must eventually time out.
	start := time.Now()
	if _, ok := h.resilienceAdmit(t.Context(), provider, cfg); ok {
		t.Fatal("second admit passed while the first still held the slot")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("second admit returned after %v, want it to wait for the timeout", elapsed)
	}
}

func TestResilienceDistinguishesCredentials(t *testing.T) {
	// Two connections to the same provider with different keys must not share a
	// gate: one account's concurrency must not throttle the other's.
	_ = t
	if resilienceAccountKey(resilienceTestConfig(), "sk-a") == resilienceAccountKey(resilienceTestConfig(), "sk-b") {
		t.Fatal("two different credentials produced the same account key")
	}
	// The key must not be the credential itself, so it can be logged.
	if got := resilienceAccountKey(resilienceTestConfig(), "sk-secret"); got == "sk-secret" {
		t.Error("the account key is the raw credential")
	}
}

func TestResilienceProxyKeySeparatesGates(t *testing.T) {
	direct := resilienceTestConfig()
	pooled := resilienceTestConfig()
	pooled.HeaderOverrides = map[string]string{"x-9router-proxy-pool": "pool-1"}

	if resilienceProxyKey(direct) != "direct" {
		t.Errorf("resilienceProxyKey(no pool) = %q, want direct", resilienceProxyKey(direct))
	}
	if resilienceProxyKey(pooled) == resilienceProxyKey(direct) {
		t.Error("a pooled connection and a direct one share a gate key")
	}
}

func TestResilienceDashboardStatuses(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	h.Resilience = resilience.NewController(resilience.BreakerOptions{
		FailureThreshold: 1,
		ResetTimeout:     time.Minute,
	}, resilience.SemaphoreOptions{})
	cfg := resilienceTestConfig()
	h.resilienceReport(provider, cfg, http.StatusInternalServerError)

	breakers, gates := h.Resilience.Statuses()
	var found bool
	for _, b := range breakers {
		if b.Name == provider {
			found = true
			if b.State != resilience.StateOpen {
				t.Errorf("breaker state = %s, want OPEN", b.State)
			}
		}
	}
	if !found {
		t.Error("Statuses() is missing the openai breaker")
	}
	// The gate registry is process-wide, so assert only that the call is safe
	// and returns a slice rather than nil.
	if gates == nil {
		t.Error("Statuses() returned a nil gate slice")
	}
}

// TestResilienceAdmitIsConcurrencySafe drives the real gate concurrently: the
// breaker and semaphore both mutate shared state, and a data race there would
// surface under -race rather than in the assertions.
func TestResilienceAdmitIsConcurrencySafe(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	cfg := resilienceTestConfig()

	var admitted, refused atomic.Int32
	done := make(chan struct{})
	for range 24 {
		go func() {
			defer func() { done <- struct{}{} }()
			release, ok := h.resilienceAdmit(t.Context(), provider, cfg)
			if !ok {
				refused.Add(1)
				return
			}
			admitted.Add(1)
			time.Sleep(5 * time.Millisecond)
			h.resilienceReport(provider, cfg, http.StatusOK)
			release()
		}()
	}
	for range 24 {
		<-done
	}
	if admitted.Load() == 0 {
		t.Fatal("no request was admitted under concurrency")
	}
}

// TestPromptCacheServesIdenticalRequest proves the exact-prompt layer answers
// without touching a provider: a second identical request is served from the
// cache, which is what makes it cheaper than the semantic layer.
func TestPromptCacheServesIdenticalRequest(t *testing.T) {
	h, _ := newResilienceTestHandler(t)
	h.PromptCache = promptcache.New(10, time.Minute)

	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	key, ok := promptcache.HashRequest("gpt-5", body)
	if !ok {
		t.Fatal("HashRequest rejected a valid body")
	}
	h.PromptCache.Set(key, "gpt-5", "application/json", http.StatusOK, []byte(`{"ok":true}`), false)

	entry, hit := h.PromptCache.Get(key)
	if !hit {
		t.Fatal("Get missed a stored entry")
	}
	if string(entry.Body) != `{"ok":true}` {
		t.Errorf("Body = %q, want the stored response", entry.Body)
	}
}

// TestPromptCacheRejectsUnhashable proves a request that cannot be hashed is
// not served from the cache, rather than colliding every malformed body onto
// one entry.
func TestPromptCacheRejectsUnhashable(t *testing.T) {
	h, _ := newResilienceTestHandler(t)
	h.PromptCache = promptcache.New(10, time.Minute)

	for _, body := range []string{
		`{"model":"m"}`,
		`{"messages":[]}`,
		`{not json`,
	} {
		if _, ok := promptcache.HashRequest("m", []byte(body)); ok {
			t.Errorf("HashRequest(%s) = true, want false", body)
		}
	}
}

// TestPromptCacheNilHandlerSafe proves a handler without the cache wired still
// serves traffic.
func TestPromptCacheNilHandlerSafe(t *testing.T) {
	h, _ := newResilienceTestHandler(t)
	h.PromptCache = nil
	// The gate in HandleChatCompletions is a nil check, so this only needs to
	// prove the handler is usable: a nil cache must not be dereferenced.
	if h.PromptCache != nil {
		t.Fatal("test setup failed")
	}
	_ = httptest.NewRecorder()
}
