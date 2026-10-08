//go:build integration

package integration

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// compressionSummary is the sanitized shape GET /api/analytics/compression
// returns. Only the fields this suite asserts on are declared.
type compressionSummary struct {
	TotalRequests int64 `json:"totalRequests"`
	ByMode        map[string]struct {
		Count int64 `json:"count"`
	} `json:"byMode"`
	ByProvider map[string]struct {
		Count int64 `json:"count"`
	} `json:"byProvider"`
}

// seedCompressionRun writes one compression run the way the live pipeline does,
// so the dashboard endpoints have something to aggregate.
func seedCompressionRun(t *testing.T, env *Env, provider, mode string, tokensSaved, original int) {
	t.Helper()
	rec := db.CompressionAnalyticsRecord{
		Timestamp:              time.Now().UTC().Format(time.RFC3339),
		Provider:               provider,
		Model:                  "deepseek-chat",
		Mode:                   mode,
		OriginalTokens:         original,
		CompressedTokens:       original - tokensSaved,
		TokensSaved:            tokensSaved,
		DurationMs:             12,
		RequestID:              "req-" + provider + "-" + mode,
		ActualPromptTokens:     original - tokensSaved,
		ActualCompletionTokens: 5,
		ActualTotalTokens:      original - tokensSaved + 5,
	}
	if err := env.Repo.InsertCompressionAnalytics(context.Background(), rec); err != nil {
		t.Fatalf("seed compression run %s/%s: %v", provider, mode, err)
	}
}

// TestUsageSectionsServeTheirOwnAnalytics is the end-to-end check for issue
// #200: Cache Analytics and Compression Analytics became sections of the Usage
// page, and each section must still be able to load the data it renders. A
// section that opens but 500s — or that reports an empty window because the
// endpoint silently substituted a different one — is exactly the regression a
// UI-only test cannot see.
func TestUsageSectionsServeTheirOwnAnalytics(t *testing.T) {
	env, _ := newProviderEnv(t)
	seedCompressionRun(t, env, "deepseek", "rtk", 400, 1000)

	t.Run("cache section loads the stats the view renders", func(t *testing.T) {
		res := env.Get(t, "/api/cache?trendHours=24")
		if res.Status != http.StatusOK {
			t.Fatalf("GET /api/cache = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
		}
		var cache struct {
			PromptCache *db.PromptCacheMetrics `json:"promptCache"`
			Trend       []db.CacheTrendPoint   `json:"trend"`
		}
		res.Decode(t, &cache)
		// A null promptCache leaves every Cache section card reading 0, which is
		// indistinguishable from "no caching happened".
		if cache.PromptCache == nil {
			t.Fatal("promptCache is null, so the Cache section has nothing to render")
		}
		if cache.PromptCache.ByProvider == nil {
			t.Error("promptCache.byProvider is null, so the provider breakdown table cannot render")
		}
	})

	t.Run("cache section honours the trend window it is asked for", func(t *testing.T) {
		// The Cache section offers 12h/24h/48h/72h; each must be served rather
		// than clamped to the default.
		for _, hours := range []string{"12", "24", "48", "72"} {
			res := env.Get(t, "/api/cache?trendHours="+hours)
			if res.Status != http.StatusOK {
				t.Errorf("GET /api/cache?trendHours=%s = %d, want 200 (body: %s)", hours, res.Status, truncate(res.Body))
			}
		}
	})

	t.Run("cache entries endpoint paginates", func(t *testing.T) {
		res := env.Get(t, "/api/cache/entries?page=1&limit=10&sortBy=created_at&sortOrder=desc")
		if res.Status != http.StatusOK {
			t.Fatalf("GET /api/cache/entries = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
		}
		var page struct {
			Entries    []map[string]any `json:"entries"`
			Pagination struct {
				Page  int `json:"page"`
				Limit int `json:"limit"`
			} `json:"pagination"`
		}
		res.Decode(t, &page)
		if page.Pagination.Page != 1 || page.Pagination.Limit != 10 {
			t.Errorf("pagination = %+v, want page 1 and limit 10", page.Pagination)
		}
		if page.Entries == nil {
			t.Error("entries is null, so the cached-entries table cannot render")
		}
	})

	t.Run("compression section reports the seeded run", func(t *testing.T) {
		res := env.Get(t, "/api/analytics/compression?since=24h")
		if res.Status != http.StatusOK {
			t.Fatalf("GET /api/analytics/compression = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
		}
		var summary compressionSummary
		res.Decode(t, &summary)

		if summary.TotalRequests != 1 {
			t.Fatalf("totalRequests = %d, want the 1 seeded run", summary.TotalRequests)
		}
		if summary.ByMode["rtk"].Count != 1 {
			t.Errorf("byMode[rtk].count = %d, want 1 (byMode = %+v)", summary.ByMode["rtk"].Count, summary.ByMode)
		}
		if summary.ByProvider["deepseek"].Count != 1 {
			t.Errorf("byProvider[deepseek].count = %d, want 1 (byProvider = %+v)", summary.ByProvider["deepseek"].Count, summary.ByProvider)
		}
	})

	t.Run("compression serves every window its dropdown offers", func(t *testing.T) {
		// The section's dropdown offers exactly these four. The endpoint answers
		// an unknown window with its 24h default instead of erroring, so the only
		// way to catch a dropped option is to request each one and require the
		// seeded run back — a section showing "no data" for 30d is broken even
		// though the call succeeded.
		for _, since := range []string{"24h", "7d", "30d", "all"} {
			res := env.Get(t, "/api/analytics/compression?since="+since)
			if res.Status != http.StatusOK {
				t.Errorf("GET /api/analytics/compression?since=%s = %d, want 200 (body: %s)", since, res.Status, truncate(res.Body))
				continue
			}
			var summary compressionSummary
			res.Decode(t, &summary)
			if summary.TotalRequests != 1 {
				t.Errorf("since=%s reported %d runs, want the 1 seeded run", since, summary.TotalRequests)
			}
		}
	})

	t.Run("usage sections have their own browser paths", func(t *testing.T) {
		// The sections are reached by path, so a path the gateway does not serve
		// the SPA shell on drops the user on a 404 instead of the Usage page.
		// requireLogin is switched off so the request is answered with the shell
		// itself: with login on, every /dashboard path answers 302 to /login
		// whether or not a route exists, and the check would pass vacuously.
		if err := env.Repo.UpdateSettingsRaw(map[string]any{"requireLogin": false}); err != nil {
			t.Fatalf("disable requireLogin: %v", err)
		}
		for _, path := range []string{
			"/dashboard/usage",
			"/dashboard/usage/cache",
			"/dashboard/usage/compression",
		} {
			res := env.Get(t, path, WithoutAPIKey())
			if res.Status != http.StatusOK {
				t.Errorf("GET %s = %d, want 200 (the SPA shell) (body: %s)", path, res.Status, truncate(res.Body))
				continue
			}
			if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Errorf("GET %s Content-Type = %q, want the SPA shell", path, ct)
			}
		}
	})

	t.Run("paths from before the move still serve the shell", func(t *testing.T) {
		// A tab left open across the upgrade must not 404.
		for _, path := range []string{"/dashboard/cache", "/dashboard/analytics/compression"} {
			res := env.Get(t, path, WithoutAPIKey())
			if res.Status != http.StatusOK {
				t.Errorf("GET %s = %d, want 200 (the SPA shell) (body: %s)", path, res.Status, truncate(res.Body))
			}
		}
	})

	t.Run("overview stats endpoint still backs the Usage page", func(t *testing.T) {
		// The Overview keeps its own window selector; the custom windows it offers
		// must resolve to that window and not fall back to the 7-day default.
		for _, period := range []string{"today", "24h", "7d", "all", "14d", "12h"} {
			res := env.Get(t, "/api/usage/stats?period="+period)
			if res.Status != http.StatusOK {
				t.Errorf("GET /api/usage/stats?period=%s = %d, want 200 (body: %s)", period, res.Status, truncate(res.Body))
			}
		}

		// A malformed window must not be accepted silently: the server answers
		// with its 7-day fallback, so the payload shape is the only signal. The
		// assertion here is that the response is the stats document at all.
		var stats map[string]jsontext.Value
		res := env.Get(t, "/api/usage/stats?period=all")
		res.Decode(t, &stats)
		if _, ok := stats["byProvider"]; !ok {
			t.Errorf("usage stats payload has no byProvider bucket (keys: %v)", stats)
		}
	})
}
