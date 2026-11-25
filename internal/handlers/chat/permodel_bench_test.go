package chat

import (
	"fmt"
	"testing"

	"9router/proxy/internal/db"
)

// The two costs the admission decision adds, measured for the perf gate:
// the resolver on a populated settings struct, the limiter allow itself,
// and the O(cap) eviction at the 5,000-bucket cap.
func BenchmarkResolvePerModel(b *testing.B) {
	settings := &db.PerModelRateLimitSettings{
		Enabled: new(true), Limit: 120, WindowMs: 60_000,
		Overrides: map[string]*db.PerModelRateLimitOverride{
			"openai-compatible-x": {Limit: 60},
		},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		resolvePerModelRateLimit(settings, "openai-compatible-x", "gpt-6")
	}
}

func BenchmarkAllowPerModel(b *testing.B) {
	l := NewPerModelRateLimiter()
	limit := PerModelLimit{Enabled: true, Limit: 10000, WindowMs: 60_000}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		l.allow("openai-compatible-x", fmt.Sprintf("m%d", i%50), "sess", limit)
	}
}

func BenchmarkEvictOldest(b *testing.B) {
	l := NewPerModelRateLimiter()
	limit := PerModelLimit{Enabled: true, Limit: 10000, WindowMs: 60_000}
	for i := 0; i < 5000; i++ {
		l.allow("p", fmt.Sprintf("m%d", i), "s", limit)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.allow("p", fmt.Sprintf("new%d", i), "s", limit)
	}
}
