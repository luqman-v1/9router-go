package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// chartBucket is one point of the usage chart. ByModel carries the per-model
// family token split the stacked area chart layers on top of the bucket total;
// the server folds every family past the top ones into "other" so the legend
// stays readable (parity with techysy/10router's getChartData).
type chartBucket struct {
	Label   string           `json:"label"`
	Tokens  int64            `json:"tokens"`
	Cost    float64          `json:"cost"`
	ByModel map[string]int64 `json:"byModel"`
}

// codenameFamilies maps opaque or proprietary codenames to the product family
// they belong to (e.g. Qoder's qfmodel → qwen, dfmodel → deepseek). Ported from
// 10router's CODENAME_FAMILIES so the chart legend shows one bar per product.
var codenameFamilies = []struct {
	name   string
	family string
}{
	{"qmodel", "qwen"},
	{"qfmodel", "qwen"},
	{"qwq", "qwen"},
	{"dmodel", "deepseek"},
	{"dfmodel", "deepseek"},
	{"kmodel", "kimi"},
	{"gmodel", "glm"},
	{"gfm", "glm"},
	{"mmodel", "minimax"},
	{"ultimate", "claude"},
	{"performance", "claude"},
}

// brandFamilies folds different product-line prefixes of one brand into a
// single family (stepaudio → step), applied after the first-segment strip.
var brandFamilies = map[string]string{"stepaudio": "step"}

// modelFamilyName reduces a model id to its chart family. The rules mirror
// 10router: strip a provider prefix, fold known codenames, drop a trailing
// version run from the first hyphen segment (gpt-5.6-sol → gpt), and classify
// custom-channel uuid prefixes as "other".
func modelFamilyName(model string) string {
	raw := strings.TrimSpace(model)
	if raw == "" {
		return "unknown"
	}
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		raw = raw[i+1:]
	}
	lower := strings.ToLower(raw)
	if hasHexPrefix(lower, 8) {
		return "other"
	}
	for _, cf := range codenameFamilies {
		if lower == cf.name || strings.HasPrefix(lower, cf.name) {
			return cf.family
		}
	}
	first := strings.SplitN(lower, "-", 2)[0]
	if first == "" {
		return "other"
	}
	stripped := trimTrailingVersion(first)
	if stripped == "" {
		stripped = first
	}
	if fam, ok := brandFamilies[stripped]; ok {
		return fam
	}
	return stripped
}

// hasHexPrefix reports whether s starts with at least n hex characters — the
// shape of a custom-channel uuid prefix like "85d2a64e-…".
func hasHexPrefix(s string, n int) bool {
	count := 0
	for _, r := range s {
		if count == n {
			return true
		}
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			return false
		}
		count++
	}
	return count == n
}

// trimTrailingVersion drops a trailing numeric run (with optional dot groups)
// from a family candidate: "gpt5" → "gpt", "qwen3.8" → "qwen". A purely
// numeric segment is left intact instead of collapsing to "".
func trimTrailingVersion(s string) string {
	i := len(s)
	for i > 0 {
		c := s[i-1]
		if (c >= '0' && c <= '9') || c == '.' {
			i--
			continue
		}
		break
	}
	if i == 0 {
		return s
	}
	return s[:i]
}

// finalizeChartBuckets keeps the top families by period total and folds the
// rest into "other" so the chart legend stays bounded (parity with 10router's
// finalizeModelBuckets, max 7 families).
func finalizeChartBuckets(buckets []chartBucket, maxFamilies int) {
	totals := map[string]int64{}
	for _, b := range buckets {
		for fam, t := range b.ByModel {
			totals[fam] += t
		}
	}
	type famTotal struct {
		fam   string
		total int64
	}
	ranked := make([]famTotal, 0, len(totals))
	for fam, t := range totals {
		ranked = append(ranked, famTotal{fam, t})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].total > ranked[j].total })
	top := map[string]bool{}
	for i, ft := range ranked {
		if i >= maxFamilies {
			break
		}
		top[ft.fam] = true
	}
	for i := range buckets {
		merged := map[string]int64{}
		for fam, t := range buckets[i].ByModel {
			key := fam
			if !top[fam] {
				key = "other"
			}
			merged[key] += t
		}
		buckets[i].ByModel = merged
	}
}

// dayKeyFor is the UTC calendar key of a timestamp. usageDaily rows are keyed
// by UTC day (see the chat usage writer), so the chart buckets by UTC too —
// mixing a local key here would split one day's rollup across two buckets.
func dayKeyFor(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// addFamily folds a row's tokens into a bucket's family map.
func addFamily(byModel map[string]int64, model string, tokens int64) {
	if tokens == 0 {
		return
	}
	byModel[modelFamilyName(model)] += tokens
}

// parseChartPeriod maps the ?period parameter onto a bucket kind and count.
// It mirrors the dashboard presets; anything unrecognized falls back to 7d.
func parseChartPeriod(raw string) (kind string, count int) {
	switch raw {
	case "today", "24h":
		return raw, 24
	case "all":
		return "all", 0
	case "30d":
		return "daily", 30
	case "60d":
		return "daily", 60
	}
	// The selector also allows custom "<n>d" / "<n>h" windows (see
	// resolveUsagePeriod); mirror that here so the chart follows the same
	// window the stats cards use. Hourly windows cap at 7 days of buckets.
	if n, ok := usagePeriodCount(raw, "d"); ok {
		return "daily", min(n, allUsageDays)
	}
	if n, ok := usagePeriodCount(raw, "h"); ok {
		return "hourly", min(n, 168)
	}
	return "daily", 7
}

// HandleUsageChart serves GET /api/usage/chart?period=: the time series the
// Overview chart draws. Today and 24h bucket by hour out of usageHistory;
// daily periods read the usageDaily rollup for totals and fill the per-family
// split from usageHistory rows in range.
func HandleUsageChart(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind, count := parseChartPeriod(r.URL.Query().Get("period"))
		now := time.Now()

		var buckets []chartBucket
		switch kind {
		case "today":
			start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			buckets = hourlyChartBuckets(repo, start, count)
		case "24h", "hourly":
			buckets = hourlyChartBuckets(repo, now.Add(-time.Duration(count)*time.Hour), count)
		default:
			buckets = dailyChartBuckets(repo, now, count)
		}

		finalizeChartBuckets(buckets, 7)
		handlerutil.WriteJSON(w, http.StatusOK, buckets)
	}
}

// hourlyChartBuckets builds count one-hour buckets from raw history rows.
func hourlyChartBuckets(repo *db.Repo, start time.Time, count int) []chartBucket {
	buckets := make([]chartBucket, count)
	for i := range buckets {
		buckets[i] = chartBucket{Label: start.Add(time.Duration(i) * time.Hour).Format("15:04"), ByModel: map[string]int64{}}
	}
	rows, err := repo.GetUsageHistoryChartSince(start.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return buckets
	}
	for _, row := range rows {
		ts, err := time.Parse(time.RFC3339Nano, row.Timestamp)
		if err != nil {
			continue
		}
		idx := int(ts.Sub(start) / time.Hour)
		if idx < 0 || idx >= count {
			continue
		}
		tokens := int64(row.PromptTokens) + int64(row.CompletionTokens)
		buckets[idx].Tokens += tokens
		buckets[idx].Cost += row.Cost
		addFamily(buckets[idx].ByModel, row.Model, tokens)
	}
	return buckets
}

// dailyChartBuckets builds one bucket per calendar day ending today. Totals
// come from the usageDaily rollup; the per-family split comes from a range
// scan of usageHistory over the same window (the rollup carries only totals).
// count == 0 means "all recorded days", derived from the earliest daily row.
func dailyChartBuckets(repo *db.Repo, now time.Time, count int) []chartBucket {
	dayRows, err := repo.GetUsageDailyRange(allUsageDays)
	if err != nil {
		return nil
	}
	if count == 0 {
		if len(dayRows) == 0 {
			return nil
		}
		earliest, err := time.Parse("2006-01-02", dayRows[len(dayRows)-1].DateKey)
		if err != nil {
			return nil
		}
		today, _ := time.Parse("2006-01-02", dayKeyFor(now))
		count = int(today.Sub(earliest)/(24*time.Hour)) + 1
		if count < 1 {
			count = 1
		}
	}

	index := make(map[string]int, count)
	buckets := make([]chartBucket, count)
	for i := 0; i < count; i++ {
		d := now.AddDate(0, 0, -(count - 1 - i))
		key := dayKeyFor(d)
		index[key] = i
		buckets[i] = chartBucket{Label: d.Format("Jan 2"), ByModel: map[string]int64{}}
	}

	for _, row := range dayRows {
		i, ok := index[row.DateKey]
		if !ok {
			continue
		}
		var dayData struct {
			PromptTokens     int64   `json:"promptTokens"`
			CompletionTokens int64   `json:"completionTokens"`
			Cost             float64 `json:"cost"`
		}
		if err := json.Unmarshal([]byte(row.Data), &dayData); err != nil {
			continue
		}
		buckets[i].Tokens = dayData.PromptTokens + dayData.CompletionTokens
		buckets[i].Cost = dayData.Cost
	}

	rangeStart := now.AddDate(0, 0, -(count - 1))
	rows, err := repo.GetUsageHistoryChartSince(rangeStart.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return buckets
	}
	for _, row := range rows {
		ts, err := time.Parse(time.RFC3339Nano, row.Timestamp)
		if err != nil {
			continue
		}
		i, ok := index[dayKeyFor(ts)]
		if !ok {
			continue
		}
		tokens := int64(row.PromptTokens) + int64(row.CompletionTokens)
		addFamily(buckets[i].ByModel, row.Model, tokens)
	}
	return buckets
}
