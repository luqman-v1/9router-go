package db

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"9router/proxy/internal/pricing"
)

// CompressionModeStats holds per-mode compression statistics.
type CompressionModeStats struct {
	Count         int64   `json:"count"`
	TokensSaved   int64   `json:"tokensSaved"`
	AvgSavingsPct float64 `json:"avgSavingsPct"`
	Skipped       int64   `json:"skipped,omitempty"`
}

// CompressionProviderStats holds per-provider compression statistics.
type CompressionProviderStats struct {
	Count       int64 `json:"count"`
	TokensSaved int64 `json:"tokensSaved"`
}

// CompressionModelStats holds per-model compression statistics.
type CompressionModelStats struct {
	Count         int64   `json:"count"`
	TokensSaved   int64   `json:"tokensSaved"`
	AvgSavingsPct float64 `json:"avgSavingsPct"`
}

// CompressionHourBucket represents an hourly time bucket for trend charts.
type CompressionHourBucket struct {
	Hour        string `json:"hour"`
	Count       int64  `json:"count"`
	TokensSaved int64  `json:"tokensSaved"`
}

// CompressionTopSaver represents a top compression run for inspection.
type CompressionTopSaver struct {
	RequestID        string  `json:"requestId"`
	Timestamp        string  `json:"timestamp"`
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Mode             string  `json:"mode"`
	OriginalTokens   int64   `json:"originalTokens"`
	CompressedTokens int64   `json:"compressedTokens"`
	TokensSaved      int64   `json:"tokensSaved"`
	SavingsPct       float64 `json:"savingsPct"`
	DurationMs       int64   `json:"durationMs"`
	EstimatedUsd     float64 `json:"estimatedUsd"`
}

// CompressionRealUsage aggregates upstream LLM token counts and dollar savings.
type CompressionRealUsage struct {
	RequestsWithReceipts int64            `json:"requestsWithReceipts"`
	PromptTokens         int64            `json:"promptTokens"`
	CompletionTokens     int64            `json:"completionTokens"`
	TotalTokens          int64            `json:"totalTokens"`
	CacheReadTokens      int64            `json:"cacheReadTokens"`
	CacheWriteTokens     int64            `json:"cacheWriteTokens"`
	EstimatedUsdSaved    float64          `json:"estimatedUsdSaved"`
	BySource             map[string]int64 `json:"bySource"`
}

// CompressionAnalyticsSummary matches the OmniRoute summary payload.
type CompressionAnalyticsSummary struct {
	TotalRequests       int64                               `json:"totalRequests"`
	TotalTokensSaved    int64                               `json:"totalTokensSaved"`
	AvgSavingsPct       float64                             `json:"avgSavingsPct"`
	AvgDurationMs       int64                               `json:"avgDurationMs"`
	ByMode              map[string]CompressionModeStats     `json:"byMode"`
	ByProvider          map[string]CompressionProviderStats `json:"byProvider"`
	ByModel             map[string]CompressionModelStats    `json:"byModel"`
	Last24h             []CompressionHourBucket             `json:"last24h"`
	TotalSkipped        int64                               `json:"totalSkipped"`
	BySkipReason        map[string]int64                    `json:"bySkipReason,omitempty"`
	ValidationFallbacks int64                               `json:"validationFallbacks"`
	RealUsage           CompressionRealUsage                `json:"realUsage"`
	RoiTokensPerMs      float64                             `json:"roiTokensPerMs"`
	TopSavers           []CompressionTopSaver               `json:"topSavers"`
}

// CompressionAnalyticsRecord represents a single run to record in compressionAnalytics table.
type CompressionAnalyticsRecord struct {
	Timestamp              string
	Provider               string
	Model                  string
	Mode                   string
	OriginalTokens         int
	CompressedTokens       int
	TokensSaved            int
	DurationMs             int
	RequestID              string
	ActualPromptTokens     int
	ActualCompletionTokens int
	ActualTotalTokens      int
	ActualCacheReadTokens  int
	ActualCacheWriteTokens int
	EstimatedUsdSaved      float64
	SkipReason             string
}

// InsertCompressionAnalytics inserts a telemetry record of a compression run.
func (r *Repo) InsertCompressionAnalytics(ctx context.Context, rec CompressionAnalyticsRecord) error {
	if rec.Timestamp == "" {
		rec.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if rec.Mode == "" {
		rec.Mode = "rtk"
	}

	if rec.EstimatedUsdSaved <= 0 && rec.TokensSaved > 0 {
		mp, _ := pricing.GetPricingForModel(rec.Provider, rec.Model)
		rate := mp.InputPer1M
		if rate <= 0 {
			rate = defaultAvgInputPricePerMillion
		}
		rec.EstimatedUsdSaved = math.Round((float64(rec.TokensSaved)/1_000_000.0)*rate*100) / 100
	}

	query := `
INSERT INTO compressionAnalytics (
	timestamp, provider, model, mode, originalTokens, compressedTokens, tokensSaved,
	durationMs, requestId, actualPromptTokens, actualCompletionTokens,
	actualTotalTokens, actualCacheReadTokens, actualCacheWriteTokens, estimatedUsdSaved, skipReason
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := r.db.ExecContext(ctx, query,
		rec.Timestamp,
		rec.Provider,
		rec.Model,
		rec.Mode,
		rec.OriginalTokens,
		rec.CompressedTokens,
		rec.TokensSaved,
		rec.DurationMs,
		rec.RequestID,
		rec.ActualPromptTokens,
		rec.ActualCompletionTokens,
		rec.ActualTotalTokens,
		rec.ActualCacheReadTokens,
		rec.ActualCacheWriteTokens,
		rec.EstimatedUsdSaved,
		rec.SkipReason,
	)
	if err != nil {
		return fmt.Errorf("Repo.InsertCompressionAnalytics: %w", err)
	}
	return nil
}

// GetCompressionAnalyticsSummary returns aggregated metrics from compressionAnalytics (or usageHistory backfill).
func (r *Repo) GetCompressionAnalyticsSummary(ctx context.Context, since string) (*CompressionAnalyticsSummary, error) {
	summary := &CompressionAnalyticsSummary{
		ByMode:       make(map[string]CompressionModeStats),
		ByProvider:   make(map[string]CompressionProviderStats),
		ByModel:      make(map[string]CompressionModelStats),
		Last24h:      make([]CompressionHourBucket, 0),
		BySkipReason: make(map[string]int64),
		RealUsage: CompressionRealUsage{
			BySource: make(map[string]int64),
		},
		TopSavers:    make([]CompressionTopSaver, 0),
	}

	cutoff := ""
	now := time.Now().UTC()
	switch since {
	case "7d":
		cutoff = now.Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	case "30d":
		cutoff = now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	case "all":
		cutoff = ""
	default: // "24h"
		cutoff = now.Add(-24 * time.Hour).Format(time.RFC3339)
	}

	// 1. Check if compressionAnalytics has records
	var caCount int64
	if cutoff != "" {
		_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM compressionAnalytics WHERE timestamp >= ?`, cutoff).Scan(&caCount)
	} else {
		_ = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM compressionAnalytics`).Scan(&caCount)
	}

	if caCount > 0 {
		return r.queryCompressionAnalyticsTable(ctx, summary, cutoff)
	}

	// Fallback/backfill: aggregate from usageHistory where saved_tokens > 0
	return r.queryUsageHistoryBackfill(ctx, summary, cutoff)
}

func (r *Repo) queryCompressionAnalyticsTable(ctx context.Context, summary *CompressionAnalyticsSummary, cutoff string) (*CompressionAnalyticsSummary, error) {
	whereClause := ""
	args := []any{}
	if cutoff != "" {
		whereClause = "WHERE timestamp >= ?"
		args = append(args, cutoff)
	}

	// Scalars
	scalarQuery := fmt.Sprintf(`
SELECT
	COUNT(*) as total,
	COALESCE(SUM(tokensSaved), 0) as totalSaved,
	COALESCE(AVG(CASE WHEN originalTokens > 0 THEN (tokensSaved * 100.0) / originalTokens ELSE 0 END), 0) as avgPct,
	COALESCE(AVG(durationMs), 0) as avgDur,
	COALESCE(SUM(CASE WHEN skipReason IS NOT NULL AND skipReason != '' THEN 1 ELSE 0 END), 0) as skipped
FROM compressionAnalytics %s`, whereClause)

	var (
		total      sql.NullInt64
		totalSaved sql.NullInt64
		avgPct     sql.NullFloat64
		avgDur     sql.NullFloat64
		skipped    sql.NullInt64
	)
	if err := r.db.QueryRowContext(ctx, scalarQuery, args...).Scan(&total, &totalSaved, &avgPct, &avgDur, &skipped); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("queryCompressionAnalyticsTable scalars: %w", err)
	}

	summary.TotalRequests = total.Int64
	summary.TotalTokensSaved = totalSaved.Int64
	summary.AvgSavingsPct = math.Round(avgPct.Float64)
	summary.AvgDurationMs = int64(math.Round(avgDur.Float64))
	summary.TotalSkipped = skipped.Int64

	// By Mode
	modeQuery := fmt.Sprintf(`
SELECT
	mode,
	COUNT(*) as cnt,
	COALESCE(SUM(tokensSaved), 0) as saved,
	COALESCE(AVG(CASE WHEN originalTokens > 0 THEN (tokensSaved * 100.0) / originalTokens ELSE 0 END), 0) as avgPct,
	COALESCE(SUM(CASE WHEN skipReason IS NOT NULL AND skipReason != '' THEN 1 ELSE 0 END), 0) as skp
FROM compressionAnalytics %s
GROUP BY mode`, whereClause)

	modeRows, err := r.db.QueryContext(ctx, modeQuery, args...)
	if err == nil {
		defer modeRows.Close()
		for modeRows.Next() {
			var m string
			var cnt, saved, skp sql.NullInt64
			var pct sql.NullFloat64
			if err := modeRows.Scan(&m, &cnt, &saved, &pct, &skp); err == nil {
				summary.ByMode[m] = CompressionModeStats{
					Count:         cnt.Int64,
					TokensSaved:   saved.Int64,
					AvgSavingsPct: math.Round(pct.Float64),
					Skipped:       skp.Int64,
				}
			}
		}
	}

	// By Provider
	provWhere := "WHERE provider IS NOT NULL AND provider != ''"
	provArgs := []any{}
	if cutoff != "" {
		provWhere += " AND timestamp >= ?"
		provArgs = append(provArgs, cutoff)
	}
	provQuery := fmt.Sprintf(`
SELECT
	COALESCE(provider, 'unknown') as prov,
	COUNT(*) as cnt,
	COALESCE(SUM(tokensSaved), 0) as saved
FROM compressionAnalytics
%s
GROUP BY prov`, provWhere)

	provRows, err := r.db.QueryContext(ctx, provQuery, provArgs...)
	if err == nil {
		defer provRows.Close()
		for provRows.Next() {
			var p string
			var cnt, saved sql.NullInt64
			if err := provRows.Scan(&p, &cnt, &saved); err == nil {
				summary.ByProvider[p] = CompressionProviderStats{
					Count:       cnt.Int64,
					TokensSaved: saved.Int64,
				}
			}
		}
	}

	// By Model
	modelWhere := "WHERE model IS NOT NULL AND model != ''"
	modelArgs := []any{}
	if cutoff != "" {
		modelWhere += " AND timestamp >= ?"
		modelArgs = append(modelArgs, cutoff)
	}
	modelQuery := fmt.Sprintf(`
SELECT
	COALESCE(model, 'unknown') as mdl,
	COUNT(*) as cnt,
	COALESCE(SUM(tokensSaved), 0) as saved,
	COALESCE(AVG(CASE WHEN originalTokens > 0 THEN (tokensSaved * 100.0) / originalTokens ELSE 0 END), 0) as avgPct
FROM compressionAnalytics
%s
GROUP BY mdl`, modelWhere)

	modelRows, err := r.db.QueryContext(ctx, modelQuery, modelArgs...)
	if err == nil {
		defer modelRows.Close()
		for modelRows.Next() {
			var m string
			var cnt, saved sql.NullInt64
			var avgPct sql.NullFloat64
			if err := modelRows.Scan(&m, &cnt, &saved, &avgPct); err == nil {
				summary.ByModel[m] = CompressionModelStats{
					Count:         cnt.Int64,
					TokensSaved:   saved.Int64,
					AvgSavingsPct: math.Round(avgPct.Float64),
				}
			}
		}
	}

	// Hourly trend
	trendCutoff := cutoff
	if trendCutoff == "" {
		trendCutoff = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	}
	trendQuery := `
SELECT
	strftime('%Y-%m-%dT%H:00:00Z', timestamp) as hr,
	COUNT(*) as cnt,
	COALESCE(SUM(tokensSaved), 0) as saved
FROM compressionAnalytics
WHERE timestamp >= ?
GROUP BY hr
ORDER BY hr ASC`

	trendRows, err := r.db.QueryContext(ctx, trendQuery, trendCutoff)
	if err == nil {
		defer trendRows.Close()
		for trendRows.Next() {
			var hr string
			var cnt, saved sql.NullInt64
			if err := trendRows.Scan(&hr, &cnt, &saved); err == nil {
				summary.Last24h = append(summary.Last24h, CompressionHourBucket{
					Hour:        hr,
					Count:       cnt.Int64,
					TokensSaved: saved.Int64,
				})
			}
		}
	}

	// Real Usage Receipts
	receiptWhere := "WHERE actualPromptTokens > 0 OR actualTotalTokens > 0"
	receiptArgs := []any{}
	if cutoff != "" {
		receiptWhere += " AND timestamp >= ?"
		receiptArgs = append(receiptArgs, cutoff)
	}
	receiptQuery := fmt.Sprintf(`
SELECT
	COUNT(*) as receipts,
	COALESCE(SUM(actualPromptTokens), 0) as prompt,
	COALESCE(SUM(actualCompletionTokens), 0) as completion,
	COALESCE(SUM(actualTotalTokens), 0) as total,
	COALESCE(SUM(actualCacheReadTokens), 0) as cacheRead,
	COALESCE(SUM(actualCacheWriteTokens), 0) as cacheWrite
FROM compressionAnalytics
%s`, receiptWhere)

	var (
		rReceipts, rPrompt, rComp, rTotal, rRead, rWrite sql.NullInt64
	)
	if err := r.db.QueryRowContext(ctx, receiptQuery, receiptArgs...).Scan(&rReceipts, &rPrompt, &rComp, &rTotal, &rRead, &rWrite); err == nil {
		summary.RealUsage.RequestsWithReceipts = rReceipts.Int64
		summary.RealUsage.PromptTokens = rPrompt.Int64
		summary.RealUsage.CompletionTokens = rComp.Int64
		summary.RealUsage.TotalTokens = rTotal.Int64
		summary.RealUsage.CacheReadTokens = rRead.Int64
		summary.RealUsage.CacheWriteTokens = rWrite.Int64
		var dynamicUsd float64
		pQuery := fmt.Sprintf(`
SELECT
	COALESCE(provider, 'unknown') as p,
	COALESCE(model, 'unknown') as m,
	COALESCE(SUM(tokensSaved), 0) as s
FROM compressionAnalytics
%s
GROUP BY p, m
HAVING s > 0`, whereClause)
		pRows, pErr := r.db.QueryContext(ctx, pQuery, args...)
		if pErr == nil {
			defer pRows.Close()
			for pRows.Next() {
				var p, m string
				var saved int64
				if err := pRows.Scan(&p, &m, &saved); err == nil && saved > 0 {
					mp, _ := pricing.GetPricingForModel(p, m)
					rate := mp.InputPer1M
					if rate <= 0 {
						rate = defaultAvgInputPricePerMillion
					}
					dynamicUsd += (float64(saved) / 1_000_000.0) * rate
				}
			}
		}
		if dynamicUsd > 0 {
			summary.RealUsage.EstimatedUsdSaved = math.Round(dynamicUsd*100) / 100
		} else {
			summary.RealUsage.EstimatedUsdSaved = math.Round((float64(summary.TotalTokensSaved)/1_000_000.0)*defaultAvgInputPricePerMillion*100) / 100
		}
	}

	// Net ROI efficiency score (tokens saved per ms of compression duration)
	if totalDur := summary.TotalRequests * summary.AvgDurationMs; totalDur > 0 {
		summary.RoiTokensPerMs = math.Round((float64(summary.TotalTokensSaved)/float64(totalDur))*10) / 10
	}

	// Top 10 Biggest Savers
	topQuery := fmt.Sprintf(`
SELECT
	COALESCE(requestId, '') as reqId,
	COALESCE(timestamp, '') as ts,
	COALESCE(provider, 'unknown') as prov,
	COALESCE(model, 'unknown') as mdl,
	COALESCE(mode, 'rtk') as md,
	originalTokens,
	compressedTokens,
	tokensSaved,
	CASE WHEN originalTokens > 0 THEN (tokensSaved * 100.0) / originalTokens ELSE 0 END as pct,
	durationMs,
	COALESCE(estimatedUsdSaved, 0) as usd
FROM compressionAnalytics
%s
ORDER BY tokensSaved DESC
LIMIT 10`, whereClause)

	topRows, err := r.db.QueryContext(ctx, topQuery, args...)
	if err == nil {
		defer topRows.Close()
		for topRows.Next() {
			var saver CompressionTopSaver
			var pct, usd sql.NullFloat64
			if err := topRows.Scan(
				&saver.RequestID,
				&saver.Timestamp,
				&saver.Provider,
				&saver.Model,
				&saver.Mode,
				&saver.OriginalTokens,
				&saver.CompressedTokens,
				&saver.TokensSaved,
				&pct,
				&saver.DurationMs,
				&usd,
			); err == nil {
				saver.SavingsPct = math.Round(pct.Float64)
				saver.EstimatedUsd = math.Round(usd.Float64*100) / 100
				summary.TopSavers = append(summary.TopSavers, saver)
			}
		}
	}

	return summary, nil
}

func (r *Repo) queryUsageHistoryBackfill(ctx context.Context, summary *CompressionAnalyticsSummary, cutoff string) (*CompressionAnalyticsSummary, error) {
	whereClause := `WHERE tokens IS NOT NULL AND json_valid(tokens) AND CAST(COALESCE(json_extract(tokens, '$.saved_tokens'), 0) AS INTEGER) > 0`
	args := []any{}
	if cutoff != "" {
		whereClause += " AND timestamp >= ?"
		args = append(args, cutoff)
	}

	scalarQuery := fmt.Sprintf(`
SELECT
	COUNT(*) as total,
	COALESCE(SUM(CAST(json_extract(tokens, '$.saved_tokens') AS INTEGER)), 0) as totalSaved,
	COALESCE(AVG(CAST(COALESCE(json_extract(tokens, '$.saved_percent'), 0) AS REAL)), 0) as avgPct
FROM usageHistory %s`, whereClause)

	var (
		total      sql.NullInt64
		totalSaved sql.NullInt64
		avgPct     sql.NullFloat64
	)
	if err := r.db.QueryRowContext(ctx, scalarQuery, args...).Scan(&total, &totalSaved, &avgPct); err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("queryUsageHistoryBackfill scalars: %w", err)
	}

	summary.TotalRequests = total.Int64
	summary.TotalTokensSaved = totalSaved.Int64
	summary.AvgSavingsPct = math.Round(avgPct.Float64)
	summary.AvgDurationMs = 25 // Default heuristic for local token compression

	if summary.TotalRequests > 0 {
		summary.ByMode["rtk"] = CompressionModeStats{
			Count:         summary.TotalRequests,
			TokensSaved:   summary.TotalTokensSaved,
			AvgSavingsPct: summary.AvgSavingsPct,
		}
	}

	// Providers
	provQuery := fmt.Sprintf(`
SELECT
	COALESCE(provider, 'unknown') as prov,
	COUNT(*) as cnt,
	COALESCE(SUM(CAST(json_extract(tokens, '$.saved_tokens') AS INTEGER)), 0) as saved
FROM usageHistory %s
GROUP BY prov`, whereClause)

	provRows, err := r.db.QueryContext(ctx, provQuery, args...)
	if err == nil {
		defer provRows.Close()
		for provRows.Next() {
			var p string
			var cnt, saved sql.NullInt64
			if err := provRows.Scan(&p, &cnt, &saved); err == nil {
				summary.ByProvider[p] = CompressionProviderStats{
					Count:       cnt.Int64,
					TokensSaved: saved.Int64,
				}
			}
		}
	}

	// Hourly trend
	trendCutoff := cutoff
	if trendCutoff == "" {
		trendCutoff = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	}
	trendQuery := `
SELECT
	strftime('%Y-%m-%dT%H:00:00Z', timestamp) as hr,
	COUNT(*) as cnt,
	COALESCE(SUM(CAST(json_extract(tokens, '$.saved_tokens') AS INTEGER)), 0) as saved
FROM usageHistory
WHERE tokens IS NOT NULL AND json_valid(tokens)
  AND CAST(COALESCE(json_extract(tokens, '$.saved_tokens'), 0) AS INTEGER) > 0
  AND timestamp >= ?
GROUP BY hr
ORDER BY hr ASC`

	trendRows, err := r.db.QueryContext(ctx, trendQuery, trendCutoff)
	if err == nil {
		defer trendRows.Close()
		for trendRows.Next() {
			var hr string
			var cnt, saved sql.NullInt64
			if err := trendRows.Scan(&hr, &cnt, &saved); err == nil {
				summary.Last24h = append(summary.Last24h, CompressionHourBucket{
					Hour:        hr,
					Count:       cnt.Int64,
					TokensSaved: saved.Int64,
				})
			}
		}
	}

	// Real Usage
	summary.RealUsage.RequestsWithReceipts = summary.TotalRequests
	summary.RealUsage.EstimatedUsdSaved = math.Round((float64(summary.TotalTokensSaved)/1_000_000.0)*3.0*100) / 100

	if totalDur := summary.TotalRequests * summary.AvgDurationMs; totalDur > 0 {
		summary.RoiTokensPerMs = math.Round((float64(summary.TotalTokensSaved)/float64(totalDur))*10) / 10
	}

	// Backfill Top Savers from usageHistory
	uhTopQuery := fmt.Sprintf(`
SELECT
	COALESCE(id, '') as reqId,
	COALESCE(timestamp, '') as ts,
	COALESCE(provider, 'unknown') as prov,
	COALESCE(model, 'unknown') as mdl,
	'rtk' as md,
	CAST(COALESCE(json_extract(tokens, '$.original_input_tokens'), promptTokens) AS INTEGER) as origTok,
	CAST(COALESCE(json_extract(tokens, '$.compressed_input_tokens'), promptTokens) AS INTEGER) as compTok,
	CAST(COALESCE(json_extract(tokens, '$.saved_tokens'), 0) AS INTEGER) as savedTok,
	CAST(COALESCE(json_extract(tokens, '$.saved_percent'), 0) AS REAL) as pct,
	25 as durMs
FROM usageHistory
%s
ORDER BY savedTok DESC
LIMIT 10`, whereClause)

	uhTopRows, err := r.db.QueryContext(ctx, uhTopQuery, args...)
	if err == nil {
		defer uhTopRows.Close()
		for uhTopRows.Next() {
			var saver CompressionTopSaver
			var pct sql.NullFloat64
			if err := uhTopRows.Scan(
				&saver.RequestID,
				&saver.Timestamp,
				&saver.Provider,
				&saver.Model,
				&saver.Mode,
				&saver.OriginalTokens,
				&saver.CompressedTokens,
				&saver.TokensSaved,
				&pct,
				&saver.DurationMs,
			); err == nil {
				saver.SavingsPct = math.Round(pct.Float64)
				mp, _ := pricing.GetPricingForModel(saver.Provider, saver.Model)
				rate := mp.InputPer1M
				if rate <= 0 {
					rate = defaultAvgInputPricePerMillion
				}
				saver.EstimatedUsd = math.Round((float64(saver.TokensSaved)/1_000_000.0)*rate*100) / 100
				summary.TopSavers = append(summary.TopSavers, saver)
			}
		}
	}

	return summary, nil
}

// BackfillCompressionAnalytics migrates historical compression telemetry from requestDetails
// and usageHistory into compressionAnalytics if not already performed.
func BackfillCompressionAnalytics(db *sql.DB) error {
	if db == nil {
		return nil
	}

	// 1. Check if migration already ran
	var done string
	err := db.QueryRow(`SELECT value FROM _meta WHERE key = 'migration_compression_backfill_done'`).Scan(&done)
	if err == nil && done == "true" {
		return nil
	}

	// 2. Query requestDetails with compression tokens
	rdQuery := `
SELECT
	id,
	timestamp,
	provider,
	COALESCE(model, '') as model,
	data,
	CAST(COALESCE(json_extract(data, '$.tokens.original_input_tokens'), 0) AS INTEGER) as origTokens,
	CAST(COALESCE(json_extract(data, '$.tokens.compressed_input_tokens'), 0) AS INTEGER) as compTokens,
	CAST(COALESCE(json_extract(data, '$.tokens.saved_tokens'), 0) AS INTEGER) as savedTokens,
	CAST(COALESCE(json_extract(data, '$.latency.total'), 0) AS INTEGER) as durMs,
	CAST(COALESCE(json_extract(data, '$.tokens.prompt_tokens'), 0) AS INTEGER) as promptTokens,
	CAST(COALESCE(json_extract(data, '$.tokens.completion_tokens'), 0) AS INTEGER) as compTokens2,
	CAST(COALESCE(json_extract(data, '$.tokens.cached_tokens'), 0) AS INTEGER) as cachedTokens,
	CAST(COALESCE(json_extract(data, '$.tokens.cache_creation_input_tokens'), 0) AS INTEGER) as cacheWriteTokens
FROM requestDetails
WHERE data IS NOT NULL AND json_valid(data)
  AND CAST(COALESCE(json_extract(data, '$.tokens.saved_tokens'), 0) AS INTEGER) > 0`

	rows, err := db.Query(rdQuery)
	if err == nil {
		defer rows.Close()

		insertStmt, prepErr := db.Prepare(`
INSERT INTO compressionAnalytics (
	timestamp, provider, model, mode, originalTokens, compressedTokens, tokensSaved,
	durationMs, requestId, actualPromptTokens, actualCompletionTokens,
	actualTotalTokens, actualCacheReadTokens, actualCacheWriteTokens, estimatedUsdSaved, skipReason
)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ''
WHERE NOT EXISTS (SELECT 1 FROM compressionAnalytics ca WHERE ca.requestId = ?)`)

		if prepErr == nil {
			defer insertStmt.Close()
			for rows.Next() {
				var (
					id, ts, prov, mdl, data                     string
					origTok, compTok, savedTok, durMs           int
					promptTok, compTok2, cachedTok, cacheWrTok int
				)
				if scanErr := rows.Scan(
					&id, &ts, &prov, &mdl, &data,
					&origTok, &compTok, &savedTok, &durMs,
					&promptTok, &compTok2, &cachedTok, &cacheWrTok,
				); scanErr == nil {
					mode := classifyHistoricalMode(data, savedTok)
					totalTok := promptTok + compTok2
					mp, _ := pricing.GetPricingForModel(prov, mdl)
					rate := mp.InputPer1M
					if rate <= 0 {
						rate = defaultAvgInputPricePerMillion
					}
					estUsd := math.Round((float64(savedTok)/1_000_000.0)*rate*100) / 100
					_, _ = insertStmt.Exec(
						ts, prov, mdl, mode, origTok, compTok, savedTok,
						durMs, id, promptTok, compTok2,
						totalTok, cachedTok, cacheWrTok, estUsd,
						id,
					)
				}
			}
		}
	}

	// 3. Fallback: also backfill from usageHistory for rows not covered by requestDetails
	uhQuery := `
SELECT
	COALESCE(timestamp, ''),
	COALESCE(provider, 'unknown'),
	COALESCE(model, 'unknown'),
	CAST(COALESCE(json_extract(tokens, '$.original_input_tokens'), promptTokens) AS INTEGER) as origTok,
	CAST(COALESCE(json_extract(tokens, '$.compressed_input_tokens'), promptTokens) AS INTEGER) as compTok,
	CAST(COALESCE(json_extract(tokens, '$.saved_tokens'), 0) AS INTEGER) as savedTok,
	promptTokens,
	completionTokens,
	CAST(COALESCE(json_extract(tokens, '$.cached_tokens'), 0) AS INTEGER) as cachedTok,
	CAST(COALESCE(json_extract(tokens, '$.cache_creation_input_tokens'), 0) AS INTEGER) as cacheWrTok
FROM usageHistory
WHERE tokens IS NOT NULL AND json_valid(tokens)
  AND CAST(COALESCE(json_extract(tokens, '$.saved_tokens'), 0) AS INTEGER) > 0`

	uhRows, err := db.Query(uhQuery)
	if err == nil {
		defer uhRows.Close()

		uhInsertStmt, prepErr := db.Prepare(`
INSERT INTO compressionAnalytics (
	timestamp, provider, model, mode, originalTokens, compressedTokens, tokensSaved,
	durationMs, requestId, actualPromptTokens, actualCompletionTokens,
	actualTotalTokens, actualCacheReadTokens, actualCacheWriteTokens, estimatedUsdSaved, skipReason
)
SELECT ?, ?, ?, 'stacked', ?, ?, ?, 25, '', ?, ?, ?, ?, ?, ?, ''
WHERE NOT EXISTS (
	SELECT 1 FROM compressionAnalytics ca
	WHERE ca.timestamp = ? AND ca.provider = ? AND ca.tokensSaved = ?
)`)

		if prepErr == nil {
			defer uhInsertStmt.Close()
			for uhRows.Next() {
				var (
					ts, prov, mdl                                string
					origTok, compTok, savedTok                   int
					promptTok, compTok2, cachedTok, cacheWrTok int
				)
				if scanErr := uhRows.Scan(
					&ts, &prov, &mdl,
					&origTok, &compTok, &savedTok,
					&promptTok, &compTok2, &cachedTok, &cacheWrTok,
				); scanErr == nil {
					totalTok := promptTok + compTok2
					mp, _ := pricing.GetPricingForModel(prov, mdl)
					rate := mp.InputPer1M
					if rate <= 0 {
						rate = defaultAvgInputPricePerMillion
					}
					estUsd := math.Round((float64(savedTok)/1_000_000.0)*rate*100) / 100
					_, _ = uhInsertStmt.Exec(
						ts, prov, mdl, origTok, compTok, savedTok,
						promptTok, compTok2, totalTok, cachedTok, cacheWrTok, estUsd,
						ts, prov, savedTok,
					)
				}
			}
		}
	}

	// 4. Mark migration done in _meta
	_, err = db.Exec(`INSERT OR REPLACE INTO _meta (key, value) VALUES ('migration_compression_backfill_done', 'true')`)
	if err != nil {
		return fmt.Errorf("BackfillCompressionAnalytics mark done: %w", err)
	}

	return nil
}

func classifyHistoricalMode(data string, savedTokens int) string {
	hasCaveman := strings.Contains(data, "terse caveman") || strings.Contains(data, "Caveman")
	hasADHD := strings.Contains(data, "ADHD") || strings.Contains(data, "action-first")
	hasPonytail := strings.Contains(data, "ponytail") || strings.Contains(data, "Ponytail")

	personaCount := 0
	if hasCaveman {
		personaCount++
	}
	if hasADHD {
		personaCount++
	}
	if hasPonytail {
		personaCount++
	}

	// A persona is present whenever any of the three prompts matched. "stacked"
	// is RTK compression applied *on top of* a persona prompt, so it needs both
	// savings and a persona; a lone persona prompt with no measurable savings is
	// classified by the persona alone.
	if personaCount > 0 {
		if savedTokens > 0 {
			return "stacked"
		}
		switch {
		case hasCaveman:
			return "caveman"
		case hasADHD:
			return "adhd"
		case hasPonytail:
			return "ponytail"
		}
	}
	if savedTokens > 0 {
		return "rtk"
	}
	return "none"
}
