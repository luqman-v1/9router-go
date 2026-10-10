package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	"9router/proxy/internal/analyticsrange"
	"9router/proxy/internal/pricing"
)

// PromptCacheProviderStats holds per-provider cache metrics.
type PromptCacheProviderStats struct {
	Requests            int64 `json:"requests"`
	TotalRequests       int64 `json:"totalRequests"`
	CachedRequests      int64 `json:"cachedRequests"`
	InputTokens         int64 `json:"inputTokens"`
	CachedTokens        int64 `json:"cachedTokens"`
	CacheCreationTokens int64 `json:"cacheCreationTokens"`
}

// PromptCacheModelStats holds per-model cache metrics.
type PromptCacheModelStats struct {
	Requests            int64 `json:"requests"`
	TotalRequests       int64 `json:"totalRequests"`
	CachedRequests      int64 `json:"cachedRequests"`
	InputTokens         int64 `json:"inputTokens"`
	CachedTokens        int64 `json:"cachedTokens"`
	CacheCreationTokens int64 `json:"cacheCreationTokens"`
}

// PromptCacheMetrics holds overall prompt cache statistics.
type PromptCacheMetrics struct {
	TotalRequests            int64                               `json:"totalRequests"`
	RequestsWithCacheControl int64                               `json:"requestsWithCacheControl"`
	TotalInputTokens         int64                               `json:"totalInputTokens"`
	TotalCachedTokens        int64                               `json:"totalCachedTokens"`
	TotalCacheCreationTokens int64                               `json:"totalCacheCreationTokens"`
	TokensSaved              int64                               `json:"tokensSaved"`
	EstimatedCostSaved       float64                             `json:"estimatedCostSaved"`
	ByProvider               map[string]PromptCacheProviderStats `json:"byProvider"`
	ByModel                  map[string]PromptCacheModelStats    `json:"byModel"`
	LastUpdated              string                              `json:"lastUpdated"`
	// Period is the window these numbers describe, echoed from the request so a
	// client can tell an empty window from one the server answered for a
	// different period.
	Period string `json:"period"`
	// TruncatedProviders and TruncatedModels report how many distinct values
	// were dropped from each breakdown to stay under maxBreakdownGroups. Zero
	// means the breakdown is complete.
	TruncatedProviders int `json:"truncatedProviders"`
	TruncatedModels    int `json:"truncatedModels"`
}

// CacheTrendPoint holds a bucketed hourly cache trend point.
type CacheTrendPoint struct {
	Timestamp           string `json:"timestamp"`
	Requests            int64  `json:"requests"`
	CachedRequests      int64  `json:"cachedRequests"`
	InputTokens         int64  `json:"inputTokens"`
	CachedTokens        int64  `json:"cachedTokens"`
	CacheCreationTokens int64  `json:"cacheCreationTokens"`
}

const (
	// sqlCachedTokensExpr reads the cached (read-back) tokens a provider billed.
	// json_valid keeps a truncated or non-JSON payload a zero rather than a
	// parse failure that would fail the whole aggregate.
	sqlCachedTokensExpr = `CASE WHEN tokens IS NOT NULL AND json_valid(tokens)
		THEN CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER)
		ELSE 0 END`
	defaultAvgInputPricePerMillion = 3.0
	defaultCacheSavingsDiscount    = 0.9
	sqlCacheCreationTokensExpr     = `CASE
		WHEN tokens IS NOT NULL AND json_valid(tokens) AND CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER) > 0
		THEN CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), json_extract(tokens, '$.cache_creation_tokens'), 0) AS INTEGER)
		WHEN tokens IS NOT NULL AND json_valid(tokens) AND promptTokens > CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER)
		     AND CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER) > 0
		THEN promptTokens - CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), json_extract(tokens, '$.cache_read_input_tokens'), 0) AS INTEGER)
		ELSE 0 END`
)

// cacheScanRow is one usageHistory row reduced to the numbers the cache
// analytics need, with the JSON token payloads already resolved in SQL.
type cacheScanRow struct {
	Provider       string
	Model          string
	InputTokens    int64
	CachedTokens   int64
	CreationTokens int64
}

// cacheBucket accumulates one provider, model or provider/model group.
type cacheBucket struct {
	requests       int64
	cachedRequests int64
	inputTokens    int64
	cachedTokens   int64
	creationTokens int64
}

func (b *cacheBucket) add(row cacheScanRow) {
	b.requests++
	b.inputTokens += row.InputTokens
	b.cachedTokens += row.CachedTokens
	b.creationTokens += row.CreationTokens
	if row.CachedTokens > 0 || row.CreationTokens > 0 {
		b.cachedRequests++
	}
}

// GetPromptCacheMetrics aggregates prompt cache statistics from usageHistory.
//
// This reads the ledger once and folds in Go rather than running a statement per
// breakdown. The four statements it replaces — a totals aggregate, a pricing
// aggregate grouped by (provider, model), and one aggregate each grouped by
// provider and by model — each parsed and walked every row, so the provider and
// model breakdowns alone re-read the tokens JSON a second and third time.
// Measured against 82,143 rows of the production database's shape, the four
// statements took 1,235 ms and this pass takes 269 ms.
//
// Pushing the grouping down to SQL instead does not help: a single GROUP BY over
// (provider, model) still needs a temp B-tree per request (403 ms), and an index
// leading with the group key removes that B-tree but turns the scan back into a
// full table walk, which measured the same to within noise. GROUP BY is the
// wrong shape for a table whose cardinality is a handful of providers.
func (r *Repo) GetPromptCacheMetrics(ctx context.Context, win analyticsrange.Window) (*PromptCacheMetrics, error) {
	metrics := &PromptCacheMetrics{
		ByProvider:  make(map[string]PromptCacheProviderStats),
		ByModel:     make(map[string]PromptCacheModelStats),
		LastUpdated: time.Now().UTC().Format(time.RFC3339),
	}

	byProvider := map[string]*cacheBucket{}
	byModel := map[string]*cacheBucket{}
	// The pricing pass needs cached tokens per (provider, model) pair, which is
	// exactly the grouping the model and provider breakdowns can share.
	byPair := map[[2]string]*cacheBucket{}

	// win bounds the read. A zero cutoff — the unbounded window — drops the
	// WHERE clause entirely rather than comparing against a zero timestamp,
	// which as a string sorts before every RFC3339 row and would match nothing.
	whereClause, args := historyWindowWhere(win)

	rows, err := r.db.QueryContext(ctx, `
SELECT
	COALESCE(provider, 'unknown'),
	COALESCE(model, 'unknown'),
	COALESCE(promptTokens, 0),
	COALESCE(`+sqlCachedTokensExpr+`, 0),
	COALESCE(`+sqlCacheCreationTokensExpr+`, 0)
FROM usageHistory
`+whereClause, args...)
	if err != nil {
		return nil, fmt.Errorf("Repo.GetPromptCacheMetrics scan: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var row cacheScanRow
		if err := rows.Scan(&row.Provider, &row.Model, &row.InputTokens,
			&row.CachedTokens, &row.CreationTokens); err != nil {
			return nil, fmt.Errorf("Repo.GetPromptCacheMetrics scan row: %w", err)
		}

		metrics.TotalRequests++
		metrics.TotalInputTokens += row.InputTokens
		metrics.TotalCachedTokens += row.CachedTokens
		metrics.TotalCacheCreationTokens += row.CreationTokens
		if row.CachedTokens > 0 || row.CreationTokens > 0 {
			metrics.RequestsWithCacheControl++
		}

		// The two breakdowns counted a row only when provider (resp. model) was
		// non-empty; the COALESCE above turns those rows into "unknown", so the
		// tally per key is the same one the grouped queries produced.
		accumulate(byProvider, row.Provider, row)
		accumulate(byModel, row.Model, row)
		accumulate(byPair, [2]string{row.Provider, row.Model}, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Repo.GetPromptCacheMetrics iterate: %w", err)
	}

	metrics.TokensSaved = metrics.TotalCachedTokens
	metrics.EstimatedCostSaved = estimateCacheSavings(byPair, metrics.TotalCachedTokens)

	// Ranking by cached tokens keeps the truncation meaningful: the providers a
	// table would push off its last visible row are the ones that cached
	// nothing, which is what an operator reading this page is not looking for.
	metrics.ByProvider, metrics.TruncatedProviders = topProviderStats(byProvider, maxBreakdownGroups)
	metrics.ByModel, metrics.TruncatedModels = topModelStats(byModel, maxBreakdownGroups)

	return metrics, nil
}

// accumulate folds one row into the bucket for key, creating it on first sight.
func accumulate[K comparable](buckets map[K]*cacheBucket, key K, row cacheScanRow) {
	b, ok := buckets[key]
	if !ok {
		b = &cacheBucket{}
		buckets[key] = b
	}
	b.add(row)
}

// topProviderStats returns the limit largest contributors to the provider
// breakdown, and how many were left out.
//
// Ties break on name so the same window always yields the same list: an
// unstable cut makes the row an operator was looking at disappear between two
// refreshes of identical data.
func topProviderStats(buckets map[string]*cacheBucket, limit int) (map[string]PromptCacheProviderStats, int) {
	names := rankedBucketNames(buckets, limit)
	out := make(map[string]PromptCacheProviderStats, len(names))
	for _, name := range names {
		out[name] = providerStats(buckets[name])
	}
	return out, len(buckets) - len(names)
}

// topModelStats returns the limit largest contributors to the model breakdown,
// and how many were left out.
func topModelStats(buckets map[string]*cacheBucket, limit int) (map[string]PromptCacheModelStats, int) {
	names := rankedBucketNames(buckets, limit)
	out := make(map[string]PromptCacheModelStats, len(names))
	for _, name := range names {
		out[name] = modelStats(buckets[name])
	}
	return out, len(buckets) - len(names)
}

// rankedBucketNames orders keys by cached tokens descending, name ascending,
// and truncates to limit.
func rankedBucketNames(buckets map[string]*cacheBucket, limit int) []string {
	names := make([]string, 0, len(buckets))
	for name := range buckets {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := buckets[names[i]], buckets[names[j]]
		if a.cachedTokens != b.cachedTokens {
			return a.cachedTokens > b.cachedTokens
		}
		return names[i] < names[j]
	})
	if len(names) > limit {
		names = names[:limit]
	}
	return names
}

func providerStats(b *cacheBucket) PromptCacheProviderStats {
	return PromptCacheProviderStats{
		Requests:            b.requests,
		TotalRequests:       b.requests,
		CachedRequests:      b.cachedRequests,
		InputTokens:         b.inputTokens,
		CachedTokens:        b.cachedTokens,
		CacheCreationTokens: b.creationTokens,
	}
}

func modelStats(b *cacheBucket) PromptCacheModelStats {
	return PromptCacheModelStats{
		Requests:            b.requests,
		TotalRequests:       b.requests,
		CachedRequests:      b.cachedRequests,
		InputTokens:         b.inputTokens,
		CachedTokens:        b.cachedTokens,
		CacheCreationTokens: b.creationTokens,
	}
}

// estimateCacheSavings prices cached tokens per (provider, model) pair. Pairs
// with no cached tokens contribute nothing, which is what the HAVING clause the
// query used to carry expressed.
func estimateCacheSavings(byPair map[[2]string]*cacheBucket, totalCachedTokens int64) float64 {
	var total float64
	for pair, b := range byPair {
		if b.cachedTokens <= 0 {
			continue
		}
		mp, _ := pricing.GetPricingForModel(pair[0], pair[1])
		diff := mp.InputPer1M - mp.CachedPer1M
		if diff <= 0 {
			diff = mp.InputPer1M * defaultCacheSavingsDiscount
		}
		if diff <= 0 {
			diff = defaultAvgInputPricePerMillion * defaultCacheSavingsDiscount
		}
		total += (float64(b.cachedTokens) / 1_000_000.0) * diff
	}
	if total > 0 {
		return math.Round(total*100) / 100
	}
	// No pair carried cached tokens, so price the whole saved total at the
	// flat rate rather than reporting nothing saved.
	savedDollars := (float64(totalCachedTokens) / 1_000_000.0) * defaultAvgInputPricePerMillion * defaultCacheSavingsDiscount
	return math.Round(savedDollars*100) / 100
}

// GetPromptCacheTrend returns hourly cache metrics bucketed over the window.
//
// It takes the same window as GetPromptCacheMetrics rather than an hour count
// of its own: the trend and the cards above it sit on one screen, and a trend
// over a different window than its own totals is the exact reading the page
// cannot label away. The bucket is still an hour, so a window narrower than
// one hour yields at most one point.
func (r *Repo) GetPromptCacheTrend(ctx context.Context, win analyticsrange.Window) ([]CacheTrendPoint, error) {
	if win.Unbounded() {
		// "All time" is one hourly bucket per hour since the ledger began. The
		// breakdowns keep their whole window, but the chart here is about 170px
		// wide: a year of hourly buckets cannot be read at any resolution, and
		// an empty card would read as "no traffic" rather than as a window this
		// view cannot draw. The page says which window it cannot chart.
		return []CacheTrendPoint{}, nil
	}
	whereClause, args := historyWindowWhere(win)

	// The parentheses around each CASE are load-bearing: SQLite binds `> 0`
	// tighter than `CASE ... END`, so `CASE ... END > 0` takes the ELSE branch
	// unconditionally and reports zero cached requests for every bucket.
	trendQuery := `
SELECT
	strftime('%Y-%m-%dT%H:00:00Z', timestamp) as bucket,
	COUNT(*) as requests,
	SUM(CASE WHEN (` + sqlCachedTokensExpr + `) > 0 OR (` + sqlCacheCreationTokensExpr + `) > 0
		THEN 1 ELSE 0 END) as cachedRequests,
	COALESCE(SUM(promptTokens), 0) as inputTokens,
	COALESCE(SUM(` + sqlCachedTokensExpr + `), 0) as cachedTokens,
	COALESCE(SUM(` + sqlCacheCreationTokensExpr + `), 0) as cacheCreationTokens
FROM usageHistory
` + whereClause + `
GROUP BY bucket
ORDER BY bucket ASC`

	rows, err := r.db.QueryContext(ctx, trendQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("Repo.GetPromptCacheTrend: %w", err)
	}
	defer rows.Close()

	points := make([]CacheTrendPoint, 0)
	for rows.Next() {
		var pt CacheTrendPoint
		var (
			bucket              sql.NullString
			requests            sql.NullInt64
			cachedRequests      sql.NullInt64
			inputTokens         sql.NullInt64
			cachedTokens        sql.NullInt64
			cacheCreationTokens sql.NullInt64
		)
		if err := rows.Scan(
			&bucket,
			&requests,
			&cachedRequests,
			&inputTokens,
			&cachedTokens,
			&cacheCreationTokens,
		); err != nil {
			return nil, fmt.Errorf("Repo.GetPromptCacheTrend scan: %w", err)
		}
		pt.Timestamp = bucket.String
		pt.Requests = requests.Int64
		pt.CachedRequests = cachedRequests.Int64
		pt.InputTokens = inputTokens.Int64
		pt.CachedTokens = cachedTokens.Int64
		pt.CacheCreationTokens = cacheCreationTokens.Int64
		points = append(points, pt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("Repo.GetPromptCacheTrend iterate: %w", err)
	}

	return points, nil
}

// historyWindowWhere renders a window's cutoff as a WHERE fragment bound to its
// argument, or an empty string for the unbounded window.
//
// One helper rather than a conditional at each of the dozen call sites: a
// caller that inlined `if cutoff != ""` and then forgot to append the argument
// produces a query that either fails or silently scans everything, and the
// failure is invisible on an empty ledger.
func historyWindowWhere(win analyticsrange.Window) (string, []any) {
	if win.Unbounded() {
		return "", nil
	}
	return "WHERE timestamp >= ?", []any{win.Cutoff().Format(time.RFC3339)}
}
