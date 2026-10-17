package db

import (
	"fmt"
	"time"
)

// usageHistoryColumns is the projection every usageHistory reader shares.
const usageHistoryColumns = `SELECT timestamp, COALESCE(provider, ''), COALESCE(model, ''), COALESCE(connectionId, ''),
	       COALESCE(apiKey, ''), COALESCE(endpoint, ''), COALESCE(promptTokens, 0),
	       COALESCE(completionTokens, 0), COALESCE(cost, 0.0), COALESCE(status, 'ok'), COALESCE(tokens, '{}')
		FROM usageHistory`

// The three usageHistory reads are named constants because
// usage_query_plan_test.go runs EXPLAIN QUERY PLAN against them. The plan is the
// contract: ordering by rowid forces a full scan that grows with the table and
// runs on the dashboard's 5-second poll, while `timestamp` is covered by
// idx_uh_ts. Keeping the SQL here means that test pins what these functions
// execute rather than a copy that could drift (#138).
const (
	usageHistorySinceQuery = usageHistoryColumns + `
		WHERE timestamp >= ?
		ORDER BY timestamp DESC
	`
	usageHistoryBetweenQuery = usageHistoryColumns + `
		WHERE timestamp > ? AND timestamp <= ?
		ORDER BY timestamp ASC
	`
	usageHistoryRecentQuery = usageHistoryColumns + `
		ORDER BY timestamp DESC
		LIMIT ?
	`
)

// GetUsageDaily returns the daily usage JSON data for a given date key.
func (r *Repo) GetUsageDaily(dateKey string) (string, error) {
	var data string
	err := r.db.QueryRow(`SELECT data FROM usageDaily WHERE dateKey = ?`, dateKey).Scan(&data)
	if err != nil {
		return "", fmt.Errorf("get daily usage %s: %w", dateKey, err)
	}
	return data, nil
}

// InsertUsageHistory logs a single request's token usage to the usageHistory table.
func (r *Repo) InsertUsageHistory(provider, model, connectionID, apiKey, endpoint string, promptTokens, completionTokens int, cost float64, status string, totalTokens int, meta string, tokensJSON string) error {
	timestamp := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(
		`INSERT INTO usageHistory (timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timestamp, provider, model, connectionID, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokensJSON, meta,
	)
	if err != nil {
		return fmt.Errorf("insert usage history: %w", err)
	}
	return nil
}

// UpsertUsageDaily inserts or replaces a daily usage aggregation record.
// The data parameter should be a JSON string matching the 9router-go daily aggregation format.
// NOTE: INSERT OR REPLACE is an atomic full-row replace of the pre-merged JSON
// blob. Merging happens in-process (see handlers/chat/usage.go dailyUsageMu), so
// concurrent writers from MULTIPLE processes can still clobber each other. This
// is documented as single-writer unless the aggregation moves SQL-side.
func (r *Repo) UpsertUsageDaily(dateKey string, data string) error {
	_, err := r.db.Exec(
		`INSERT OR REPLACE INTO usageDaily (dateKey, data) VALUES (?, ?)`,
		dateKey, data,
	)
	if err != nil {
		return fmt.Errorf("upsert daily usage %s: %w", dateKey, err)
	}
	return nil
}

// InsertRequestDetail logs a request detail record for the Recent Requests dashboard tab.
func (r *Repo) InsertRequestDetail(id, provider, model, connectionID, status string, data string) error {
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	_, err := r.db.Exec(
		`INSERT OR IGNORE INTO requestDetails (id, timestamp, provider, model, connectionId, status, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, timestamp, provider, model, connectionID, status, data,
	)
	if err != nil {
		return fmt.Errorf("insert request detail %s: %w", id, err)
	}
	return nil
}

// UsageHistoryRow represents a record from the usageHistory table.
type UsageHistoryRow struct {
	Timestamp        string
	Provider         string
	Model            string
	ConnectionID     string
	APIKey           string
	Endpoint         string
	PromptTokens     int
	CompletionTokens int
	Cost             float64
	Status           string
	Tokens           string
}

// GetUsageDailyRecent returns the most recent daily usage records up to limit.

func (r *Repo) GetUsageDailyRecent(limit int) ([]string, error) {
	rows, err := r.db.Query(`SELECT data FROM usageDaily ORDER BY dateKey DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent usageDaily: %w", err)
	}
	defer rows.Close()

	var res []string
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan usageDaily recent: %w", err)
		}
		res = append(res, data)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageDaily recent: %w", err)
	}
	return res, nil
}

// UsageDailyRow is one usageDaily row: the local date key plus its aggregate
// payload. The chart endpoint needs the key (the existing
// GetUsageDailyRecent drops it), so this keeps both.
type UsageDailyRow struct {
	DateKey string
	Data    string
}

// GetUsageDailyRange returns the most recent daily usage rows up to limit,
// newest first, each with its dateKey.
func (r *Repo) GetUsageDailyRange(limit int) ([]UsageDailyRow, error) {
	rows, err := r.db.Query(`SELECT dateKey, data FROM usageDaily ORDER BY dateKey DESC LIMIT ?`, limit) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("query usageDaily range: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var res []UsageDailyRow
	for rows.Next() {
		var row UsageDailyRow
		if err := rows.Scan(&row.DateKey, &row.Data); err != nil {
			return nil, fmt.Errorf("scan usageDaily range: %w", err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageDaily range: %w", err)
	}
	return res, nil
}

// UsageGroupRow is one provider (or provider+model) aggregate over a time
// window, used by the usage dashboard's node-health table.
type UsageGroupRow struct {
	Provider         string
	Model            string
	Requests         int
	Errors           int
	PromptTokens     int
	CompletionTokens int
	Cost             float64
	LastUsed         string
}

const usageGroupStats = `
	COUNT(*) AS requests,
	SUM(CASE WHEN status NOT IN ('ok', 'success') THEN 1 ELSE 0 END) AS errors,
	COALESCE(SUM(promptTokens), 0) AS promptTokens,
	COALESCE(SUM(completionTokens), 0) AS completionTokens,
	COALESCE(SUM(cost), 0) AS cost,
	MAX(timestamp) AS lastUsed
`

func scanUsageGroupRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}, op string) ([]UsageGroupRow, error) {
	var res []UsageGroupRow
	for rows.Next() {
		var row UsageGroupRow
		if err := rows.Scan(
			&row.Provider, &row.Model, &row.Requests, &row.Errors,
			&row.PromptTokens, &row.CompletionTokens, &row.Cost, &row.LastUsed,
		); err != nil {
			return nil, fmt.Errorf("scan usage group %s: %w", op, err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage group %s: %w", op, err)
	}
	return res, nil
}

// GetUsageGroupByProviderSince aggregates usageHistory per provider since the
// cutoff. The model column is a constant empty string: provider rows have no
// single model, and the shared scanner expects both key columns.
func (r *Repo) GetUsageGroupByProviderSince(since string) ([]UsageGroupRow, error) {
	rows, err := r.db.Query( //nolint:noctx,rowserrcheck
		`SELECT COALESCE(provider, ''), '', `+usageGroupStats+`
		   FROM usageHistory WHERE timestamp >= ? GROUP BY COALESCE(provider, '')`, since)
	if err != nil {
		return nil, fmt.Errorf("query usage group by provider: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanUsageGroupRows(rows, "provider")
}

// GetUsageGroupByModelSince aggregates usageHistory per provider+model since
// the cutoff.
func (r *Repo) GetUsageGroupByModelSince(since string) ([]UsageGroupRow, error) {
	rows, err := r.db.Query( //nolint:noctx,rowserrcheck
		`SELECT COALESCE(provider, ''), COALESCE(model, ''), `+usageGroupStats+`
		   FROM usageHistory WHERE timestamp >= ? GROUP BY COALESCE(provider, ''), COALESCE(model, '')`, since)
	if err != nil {
		return nil, fmt.Errorf("query usage group by model: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanUsageGroupRows(rows, "model")
}

// CacheUsageRow carries the raw fields needed to compute the cache hit rate:
// the row's input count and its token JSON (cached/creation live inside it).
type CacheUsageRow struct {
	PromptTokens int
	Tokens       string
}

// GetCacheUsageRows returns every history row whose token payload mentions a
// cache field. The lifetime cache-hit rate is unbounded by design (it judges
// the whole ledger), and the LIKE prefilter keeps the JSON decode off rows
// that can never contribute.
func (r *Repo) GetCacheUsageRows() ([]CacheUsageRow, error) {
	rows, err := r.db.Query(`SELECT promptTokens, tokens FROM usageHistory WHERE tokens LIKE '%cache%'`) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("query cache usage rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var res []CacheUsageRow
	for rows.Next() {
		var row CacheUsageRow
		if err := rows.Scan(&row.PromptTokens, &row.Tokens); err != nil {
			return nil, fmt.Errorf("scan cache usage row: %w", err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cache usage rows: %w", err)
	}
	return res, nil
}

// RequestDetailPerfRow is one latency observation from the requestDetails ring.
type RequestDetailPerfRow struct {
	Provider string
	Model    string
	Data     string
}

// GetRequestDetailPerf returns the stored payload of every request detail.
// The ring is retention-bounded, so this stays cheap; it is the gateway's only
// source of per-request latency for the node-health table.
func (r *Repo) GetRequestDetailPerf() ([]RequestDetailPerfRow, error) {
	rows, err := r.db.Query(`SELECT COALESCE(provider, ''), COALESCE(model, ''), data FROM requestDetails`) //nolint:noctx
	if err != nil {
		return nil, fmt.Errorf("query request detail perf: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var res []RequestDetailPerfRow
	for rows.Next() {
		var row RequestDetailPerfRow
		if err := rows.Scan(&row.Provider, &row.Model, &row.Data); err != nil {
			return nil, fmt.Errorf("scan request detail perf: %w", err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request detail perf: %w", err)
	}
	return res, nil
}

// GetUsageHistorySince returns usage history records since the cutoff timestamp.
func (r *Repo) GetUsageHistorySince(cutoff string) ([]UsageHistoryRow, error) {
	rows, err := r.db.Query(usageHistorySinceQuery, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query usageHistory since %s: %w", cutoff, err)
	}
	defer rows.Close()

	var res []UsageHistoryRow
	for rows.Next() {
		var row UsageHistoryRow
		if err := rows.Scan(
			&row.Timestamp, &row.Provider, &row.Model, &row.ConnectionID,
			&row.APIKey, &row.Endpoint, &row.PromptTokens, &row.CompletionTokens,
			&row.Cost, &row.Status, &row.Tokens,
		); err != nil {
			return nil, fmt.Errorf("scan usageHistory since %s: %w", cutoff, err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageHistory since %s: %w", cutoff, err)
	}
	return res, nil
}

// GetUsageHistoryWatermark returns the timestamp of the newest usageHistory row.
// It rides idx_uh_ts as a covering-index seek, so it stays O(log n) no matter
// how large the table grows — that is what makes a cached aggregate safe to
// refresh from a delta instead of re-reading the whole window.
func (r *Repo) GetUsageHistoryWatermark() (string, error) {
	var ts string
	if err := r.db.QueryRow(`SELECT COALESCE(MAX(timestamp), '') FROM usageHistory`).Scan(&ts); err != nil {
		return "", fmt.Errorf("read usageHistory watermark: %w", err)
	}
	return ts, nil
}

// GetUsageHistoryBetween returns rows in (since, until] — the delta a cached
// window aggregate has yet to fold in. The bounds are exclusive on the low end
// so a row already folded in at the watermark is never counted twice.
func (r *Repo) GetUsageHistoryBetween(since, until string) ([]UsageHistoryRow, error) {
	rows, err := r.db.Query(usageHistoryBetweenQuery, since, until)
	if err != nil {
		return nil, fmt.Errorf("query usageHistory between %s and %s: %w", since, until, err)
	}
	defer rows.Close()

	var res []UsageHistoryRow
	for rows.Next() {
		var row UsageHistoryRow
		if err := rows.Scan(&row.Timestamp, &row.Provider, &row.Model, &row.ConnectionID,
			&row.APIKey, &row.Endpoint, &row.PromptTokens, &row.CompletionTokens,
			&row.Cost, &row.Status, &row.Tokens); err != nil {
			return nil, fmt.Errorf("scan usageHistory between %s and %s: %w", since, until, err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usageHistory between %s and %s: %w", since, until, err)
	}
	return res, nil
}

// GetRecentUsageHistory returns the latest N usage history records.
func (r *Repo) GetRecentUsageHistory(limit int) ([]UsageHistoryRow, error) {
	rows, err := r.db.Query(usageHistoryRecentQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent usageHistory: %w", err)
	}
	defer rows.Close()

	var res []UsageHistoryRow
	for rows.Next() {
		var row UsageHistoryRow
		if err := rows.Scan(
			&row.Timestamp, &row.Provider, &row.Model, &row.ConnectionID,
			&row.APIKey, &row.Endpoint, &row.PromptTokens, &row.CompletionTokens,
			&row.Cost, &row.Status, &row.Tokens,
		); err != nil {
			return nil, fmt.Errorf("scan recent usageHistory: %w", err)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent usageHistory: %w", err)
	}
	return res, nil
}

// RequestDetailListRow is one row of the Details table: every value the table
// renders, and nothing else.
//
// The stored payload also carries the request messages and the response body —
// up to MaxLoggedMessages × MaxMessageContentLen plus MaxResponseContentLen
// characters, which measured ~20 KB per row. A page of 20 is 400 KB that the
// table never reads: it shows timestamps, provider, model, latency and token
// counts, and only the View action wants the text. So the list query selects
// those values out of the JSON and the payload stays behind
// GetRequestDetailByID until a row is actually opened.
type RequestDetailListRow struct {
	ID           string
	Timestamp    string
	Provider     string
	Model        string
	ConnectionID string
	Status       string
	LatencyTTFT  int64
	LatencyTotal int64
	TokensJSON   string
	Account      string
	Cost         float64
	Combo        string
	Protocol     string
}

// requestDetailListSelect pulls the renderable values straight out of the
// stored JSON. The key paths mirror what internal/handlers/chat writes into
// `latency` and `tokens`; json_extract returns NULL for a path a given row does
// not carry, which COALESCE folds to the zero the table already renders for
// "not reported".
const requestDetailListSelect = `
	SELECT id, timestamp, COALESCE(provider, ''), COALESCE(model, ''), COALESCE(connectionId, ''),
	       COALESCE(status, ''),
	       COALESCE(json_extract(data, '$.latency.ttft'), 0),
	       COALESCE(json_extract(data, '$.latency.total'), 0),
	       COALESCE(json_extract(data, '$.tokens'), '{}'),
	       COALESCE(json_extract(data, '$.account'), ''),
	       COALESCE(json_extract(data, '$.cost'), 0.0),
	       COALESCE(json_extract(data, '$.combo'), ''),
	       COALESCE(json_extract(data, '$.protocol'), ''),
	       json_valid(data)
	FROM requestDetails
	ORDER BY timestamp DESC
	LIMIT ? OFFSET ?`

// GetRequestDetailsPaged returns one page of collapsed detail rows and the
// total number of recorded requests.
//
// The count is not allowed to fail quietly. Reporting 0 next to a page that
// does carry rows is the same "zero that means broken" class this reader's
// siblings were fixed for, and it reaches the caller looking like a coherent
// empty page.
func (r *Repo) GetRequestDetailsPaged(limit, offset int) ([]RequestDetailListRow, int, error) {
	var total int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM requestDetails`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count requestDetails: %w", err)
	}

	rows, err := r.db.Query(requestDetailListSelect, limit, offset)
	if err != nil {
		return nil, total, fmt.Errorf("query requestDetails paged: %w", err)
	}
	defer rows.Close()

	res := make([]RequestDetailListRow, 0, limit)
	for rows.Next() {
		var row RequestDetailListRow
		var dataValid int
		if err := rows.Scan(&row.ID, &row.Timestamp, &row.Provider, &row.Model,
			&row.ConnectionID, &row.Status, &row.LatencyTTFT, &row.LatencyTotal, &row.TokensJSON,
			&row.Account, &row.Cost, &row.Combo, &row.Protocol,
			&dataValid); err != nil {
			return nil, total, fmt.Errorf("scan requestDetails: %w", err)
		}
		// A stored `data` that is NULL or not JSON is a broken read, not an
		// empty row. The raw-column query this replaced surfaced that as a scan
		// error; json_extract alone would fold it to the COALESCE defaults above
		// and render the row as a request with zero tokens and no payload, which
		// reads as real data. json_valid separates the two cases in the same pass.
		if dataValid != 1 {
			return nil, total, fmt.Errorf("requestDetails %s has an unreadable data column", row.ID)
		}
		res = append(res, row)
	}
	if err := rows.Err(); err != nil {
		return nil, total, fmt.Errorf("iterate requestDetails paged: %w", err)
	}
	return res, total, nil
}

// GetRequestDetailByID returns the full stored payload for one request, which
// is what the inspector shows. It is a primary-key lookup, so opening a row
// costs the same whether the table holds ten rows or ten million.
func (r *Repo) GetRequestDetailByID(id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("get request detail: empty id")
	}
	var data string
	if err := r.db.QueryRow(`SELECT data FROM requestDetails WHERE id = ?`, id).Scan(&data); err != nil {
		return "", fmt.Errorf("get request detail %s: %w", id, err)
	}
	return data, nil
}
