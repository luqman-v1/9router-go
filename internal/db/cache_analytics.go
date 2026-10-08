package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

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
	sqlCacheCreationTokensExpr = `CASE
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
func (r *Repo) GetPromptCacheMetrics(ctx context.Context) (*PromptCacheMetrics, error) {
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

	rows, err := r.db.QueryContext(ctx, `
SELECT
	COALESCE(provider, 'unknown'),
	COALESCE(model, 'unknown'),
	COALESCE(promptTokens, 0),
	COALESCE(`+sqlCachedTokensExpr+`, 0),
	COALESCE(`+sqlCacheCreationTokensExpr+`, 0)
FROM usageHistory`)
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

	for provider, bucket := range byProvider {
		metrics.ByProvider[provider] = providerStats(bucket)
	}
	for model, bucket := range byModel {
		metrics.ByModel[model] = modelStats(bucket)
	}

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

// GetPromptCacheTrend returns hourly cache metrics bucketed over the requested hours (1 to 720, default 24).
func (r *Repo) GetPromptCacheTrend(ctx context.Context, hours int) ([]CacheTrendPoint, error) {
	if hours < 1 {
		hours = 24
	}
	if hours > 720 {
		hours = 720
	}

	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)

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
WHERE timestamp >= ?
GROUP BY bucket
ORDER BY bucket ASC`

	rows, err := r.db.QueryContext(ctx, trendQuery, cutoff)
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
