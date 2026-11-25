package chat

import (
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func newTestLimiter(now *time.Time) *PerModelRateLimiter {
	l := NewPerModelRateLimiter()
	l.now = func() time.Time { return *now }
	return l
}

func TestPerModelLimitDefaultsAndClamp(t *testing.T) {
	// An absent block is OFF: this is net-new semantics, so a deployment that
	// never configured it must behave exactly as before.
	if limit := resolvePerModelRateLimit(nil, "openai-compatible-x", "m"); limit.Enabled {
		t.Fatalf("an unconfigured block must not enable the limiter, got %+v", limit)
	}

	// Opting in is explicit: enabled:true with no numbers applies the generous
	// default rather than an unbounded cap, and the same block with
	// enabled:false stays off.
	if off := resolvePerModelRateLimit(&db.PerModelRateLimitSettings{Enabled: new(false)}, "openai-compatible-x", "m"); off.Enabled {
		t.Fatalf("enabled:false must stay off, got %+v", off)
	}
	limit := resolvePerModelRateLimit(&db.PerModelRateLimitSettings{Enabled: new(true)}, "openai-compatible-x", "m")
	if !limit.Enabled || limit.Limit != perModelDefaultLimit || limit.WindowMs != perModelDefaultWindowMs {
		t.Fatalf("configured defaults = %+v, want enabled with the built-in default", limit)
	}

	// A typo cannot wedge chat: the limit clamps instead of rejecting.
	huge := &db.PerModelRateLimitSettings{Enabled: new(true), Limit: 99_999_999, WindowMs: 1}
	limit = resolvePerModelRateLimit(huge, "p", "m")
	if limit.Limit != perModelMaxLimit {
		t.Errorf("limit = %d, want clamped to %d", limit.Limit, perModelMaxLimit)
	}
	if limit.WindowMs != perModelDefaultWindowMs {
		t.Errorf("window = %d, want the default when the stored value is below the floor", limit.WindowMs)
	}
}

func TestPerModelLimitDisabled(t *testing.T) {
	off := &db.PerModelRateLimitSettings{Enabled: new(false)}
	if got := resolvePerModelRateLimit(off, "p", "m"); got.Enabled {
		t.Error("enabled:false must turn the limiter off")
	}

	// A missing or out-of-range limit falls back to the built-in default
	// instead of refusing every request; `enabled:false` is the off switch.
	zero := &db.PerModelRateLimitSettings{Enabled: new(true), Limit: 0}
	if got := resolvePerModelRateLimit(zero, "p", "m"); !got.Enabled || got.Limit != perModelDefaultLimit {
		t.Errorf("limit:0 = %+v, want the built-in default, not an empty cap", got)
	}
}

func TestPerModelLimitOverridePrecedence(t *testing.T) {
	settings := &db.PerModelRateLimitSettings{
		Enabled: new(true),
		Limit:   100,
		Overrides: map[string]*db.PerModelRateLimitOverride{
			"prov":         {Limit: 50},
			"prov/model-a": {Limit: 5},
		},
	}

	if got := resolvePerModelRateLimit(settings, "prov", "model-a"); got.Limit != 5 {
		t.Errorf("per-model override = %d, want 5", got.Limit)
	}
	if got := resolvePerModelRateLimit(settings, "prov", "model-b"); got.Limit != 50 {
		t.Errorf("per-provider override = %d, want 50", got.Limit)
	}
	if got := resolvePerModelRateLimit(settings, "other", "model-a"); got.Limit != 100 {
		t.Errorf("global = %d, want 100", got.Limit)
	}
}

func TestPerModelCapIsPerModelNotPerProvider(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newTestLimiter(&now)
	limit := PerModelLimit{Enabled: true, Limit: 2, WindowMs: 60_000}

	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("prov", "hot", "shared", limit); !ok {
			t.Fatalf("request %d on the hot model must be admitted", i+1)
		}
	}
	if ok, _ := l.allow("prov", "hot", "shared", limit); ok {
		t.Fatal("the third request on the hot model must be refused")
	}
	// The whole point: the cap is per MODEL, so another model on the same
	// provider is untouched.
	if ok, _ := l.allow("prov", "cold", "shared", limit); !ok {
		t.Fatal("a different model on the same provider must still be admitted")
	}
}

func TestPerModelCapRecoversAfterTheWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newTestLimiter(&now)
	limit := PerModelLimit{Enabled: true, Limit: 1, WindowMs: 60_000}

	if ok, _ := l.allow("p", "m", "shared", limit); !ok {
		t.Fatal("first request must be admitted")
	}
	ok, retry := l.allow("p", "m", "shared", limit)
	if ok {
		t.Fatal("second request inside the window must be refused")
	}
	if retry <= 0 || retry > time.Minute {
		t.Errorf("retryAfter = %v, want a positive wait no longer than the window", retry)
	}

	now = now.Add(61 * time.Second)
	if ok, _ := l.allow("p", "m", "shared", limit); !ok {
		t.Error("a request after the window must be admitted again")
	}
}

func TestPerModelScopeIsolation(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newTestLimiter(&now)
	limit := PerModelLimit{Enabled: true, Limit: 1, WindowMs: 60_000}

	if ok, _ := l.allow("p", "m", perModelScope("", "key-a"), limit); !ok {
		t.Fatal("first request must be admitted")
	}
	// One customer's hot model must not block another customer's.
	if ok, _ := l.allow("p", "m", perModelScope("", "key-b"), limit); !ok {
		t.Fatal("a different API key must have its own budget")
	}
	if ok, _ := l.allow("p", "m", perModelScope("sess-1", ""), limit); !ok {
		t.Fatal("a different session must have its own budget")
	}
	if ok, _ := l.allow("p", "m", perModelScope("", "key-a"), limit); ok {
		t.Fatal("the same customer's second request must be refused")
	}
}

func TestPerModelScopeNeverLeaksTheKey(t *testing.T) {
	secret := "sk-live-super-secret-value"
	scope := perModelScope("", secret)
	if scope == "" || scope == secret {
		t.Fatalf("scope = %q, must be a hash, not the key", scope)
	}
	if len(scope) > 16 {
		t.Errorf("scope = %q, want a short bounded hash", scope)
	}
	if perModelScope("", secret) != scope {
		t.Error("the same key must map to the same scope")
	}
	if perModelScope("", "sk-other") == scope {
		t.Error("different keys must not share a scope")
	}
}

func TestPerModelLimiterBoundsMemory(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := newTestLimiter(&now)
	limit := PerModelLimit{Enabled: true, Limit: 100, WindowMs: 60_000}

	for i := 0; i < perModelMaxBuckets+50; i++ {
		l.allow("prov", "m"+itoa(i), "shared", limit)
	}
	if got := len(l.buckets); got > perModelMaxBuckets {
		t.Errorf("limiter holds %d buckets, cap is %d", got, perModelMaxBuckets)
	}
}

func TestIsCustomCompatibleProvider(t *testing.T) {
	tests := map[string]bool{
		"openai-compatible-chat-x": true,
		"anthropic-compatible-y":   true,
		"openai":                   false,
		"deepseek":                 false,
		"custom-embedding-z":       false,
	}
	for provider, want := range tests {
		if got := isCustomCompatibleProvider(provider); got != want {
			t.Errorf("isCustomCompatibleProvider(%q) = %v, want %v", provider, got, want)
		}
	}
}
