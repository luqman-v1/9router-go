package chat

import (
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/resilience"
)

// poolConfig returns a config that routes through the named proxy pool. The
// connection records its pool on the config's ProxyPoolID field, which is where
// the resilience layer reads it from.
func poolConfig(pool string) *providers.ProviderConfig {
	return &providers.ProviderConfig{
		BaseURL:     "https://upstream.example/v1/chat/completions",
		AuthHeader:  "Authorization",
		AuthScheme:  "bearer",
		ProxyPoolID: pool,
	}
}

// The breaker must be keyed per egress, not per provider: one dead proxy pool
// must not take the provider's direct path — or its other pools — out of
// rotation. Before this, a pool whose endpoint refused connections opened the
// provider's only breaker and every connection under that provider stopped
// being dispatched, including the ones routing directly.
func TestBreakerScopedPerEgress(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	h.Resilience = resilience.NewController(resilience.BreakerOptions{
		FailureThreshold: 3,
		ResetTimeout:     time.Minute,
	}, resilience.SemaphoreOptions{MaxConcurrency: 4, Timeout: 2 * time.Second})

	direct := resilienceTestConfig()
	poolA := poolConfig("pool-a")
	poolB := poolConfig("pool-b")

	// Pool A fails three times: enough to open its own breaker.
	for range 3 {
		h.resilienceReport(provider, poolA, http.StatusBadGateway)
	}

	if _, ok := h.resilienceAdmit(t.Context(), provider, poolA); ok {
		t.Error("the failing pool's own egress must be refused")
	}
	// Direct traffic and the other pool are untouched: the failures say
	// something about pool A's endpoint, not about the provider.
	if _, ok := h.resilienceAdmit(t.Context(), provider, direct); !ok {
		t.Error("direct traffic must not be refused by a pool's failures")
	}
	if _, ok := h.resilienceAdmit(t.Context(), provider, poolB); !ok {
		t.Error("another pool must not be refused by pool A's failures")
	}
}

// A provider whose direct path is dead keeps its own breaker, and a
// connectionless deployment (everything direct) behaves exactly as before:
// one breaker per provider, keyed on the bare name.
func TestDirectPathBreakerUsesBareProviderName(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	h.Resilience = resilience.NewController(resilience.BreakerOptions{
		FailureThreshold: 2,
		ResetTimeout:     time.Minute,
	}, resilience.SemaphoreOptions{MaxConcurrency: 4, Timeout: 2 * time.Second})
	direct := resilienceTestConfig()
	for range 2 {
		h.resilienceReport(provider, direct, http.StatusBadGateway)
	}
	if _, ok := h.resilienceAdmit(t.Context(), provider, direct); ok {
		t.Fatal("a failing direct path must refuse its own traffic")
	}
	// The dashboard's per-provider panel reads the bare name, so it must show
	// the open breaker without knowing about egresses.
	st := h.Resilience.BreakerStatus(provider)
	if st.Name != provider {
		t.Errorf("breaker name = %q, want the bare provider %q", st.Name, provider)
	}
	if st.State != resilience.StateOpen {
		t.Errorf("state = %s, want OPEN", st.State)
	}
}

// One egress recovering must not be judged on another's evidence: a success on
// the direct path closes only the direct breaker.
func TestEgressRecoveryIsIndependent(t *testing.T) {
	h, provider := newResilienceTestHandler(t)
	h.Resilience = resilience.NewController(resilience.BreakerOptions{
		FailureThreshold: 2,
		ResetTimeout:     10 * time.Millisecond,
	}, resilience.SemaphoreOptions{MaxConcurrency: 4, Timeout: 2 * time.Second})
	direct := resilienceTestConfig()
	pool := poolConfig("pool-a")

	for range 2 {
		h.resilienceReport(provider, direct, http.StatusBadGateway)
		h.resilienceReport(provider, pool, http.StatusBadGateway)
	}
	if _, ok := h.resilienceAdmit(t.Context(), provider, direct); ok {
		t.Fatal("direct breaker should be open")
	}
	if _, ok := h.resilienceAdmit(t.Context(), provider, pool); ok {
		t.Fatal("pool breaker should be open")
	}
	// Both recover. A breaker in OPEN only closes on the outcome of a probe it
	// admitted, which is what Allow() hands out after the reset timeout — a
	// success reported without an admitted probe must not close it, because
	// that success belongs to a request that went out before the breaker
	// opened. The reset timeout is already short, so the probe is admitted
	// after a brief wait.
	time.Sleep(20 * time.Millisecond)

	for _, cfg := range []*providers.ProviderConfig{direct, pool} {
		_, ok := h.resilienceAdmit(t.Context(), provider, cfg)
		if !ok {
			t.Fatalf("egress %s did not admit a probe after the reset timeout", resilienceProxyKey(cfg))
		}
		h.resilienceReport(provider, cfg, http.StatusOK)
	}
	statuses, _ := h.Resilience.Statuses()
	// Filter to this test's provider: the registry is process-wide and other
	// tests in this package own their own breakers.
	var mine []resilience.BreakerStatus
	for _, st := range statuses {
		if st.Name == provider || st.Name == provider+"|pool-a" {
			mine = append(mine, st)
		}
	}
	if len(mine) != 2 {
		t.Fatalf("breakers = %d, want 2 (one per egress)", len(mine))
	}
	for _, st := range mine {
		if st.State != resilience.StateClosed {
			t.Errorf("breaker %s = %s, want CLOSED after its own success", st.Name, st.State)
		}
	}
}
