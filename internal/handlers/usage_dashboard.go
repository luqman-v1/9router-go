package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// The usage dashboard serves the Details section (issue #280): lifetime cards,
// the activity heatmap, and the node-health table. The heatmap is a fixed
// trailing-year window, node health a fixed trailing 7 days, and the cards are
// lifetime totals except the cache-hit rate, which reuses the same trailing
// 7-day history the Top Model card reads — ported from techysy/10router's
// getUsageDashboard.

// dashboardDailyPoint is one heatmap cell.
type dashboardDailyPoint struct {
	Date     string  `json:"date"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// dashboardTopModel is the "Most Used Model" card payload.
type dashboardTopModel struct {
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
	Tokens   int64  `json:"tokens"`
}

// dashboardLifetime carries the lifetime cards' numbers.
type dashboardLifetime struct {
	TotalRequests int64              `json:"totalRequests"`
	TotalTokens   int64              `json:"totalTokens"`
	PeakTokens    int64              `json:"peakTokens"`
	PeakDate      string             `json:"peakDate"`
	CurrentStreak int                `json:"currentStreak"`
	LongestStreak int                `json:"longestStreak"`
	TopModel      *dashboardTopModel `json:"topModel"`
	CacheHitRate  *float64           `json:"cacheHitRate"`
}

// dashboardNode is one node-health row (a provider, or a model under it).
// Score is null when the row has no latency sample at all: missing data is
// not a neutral score, it is no score, and such rows stay out of the ranking.
type dashboardNode struct {
	Name             string   `json:"name,omitempty"`
	Model            string   `json:"model,omitempty"`
	Provider         string   `json:"provider"`
	Requests         int      `json:"requests"`
	Errors           int      `json:"errors"`
	SuccessRate      float64  `json:"successRate"`
	AvgLatencyMs     *int64   `json:"avgLatencyMs"`
	AvgTtftMs        *int64   `json:"avgTtftMs"`
	AvgSpeed         *float64 `json:"avgSpeed"`
	HasPerfData      bool     `json:"hasPerfData"`
	Score            *int     `json:"score"`
	PromptTokens     int64    `json:"promptTokens"`
	CompletionTokens int64    `json:"completionTokens"`
	Cost             float64  `json:"cost"`
	LastUsed         string   `json:"lastUsed"`
}

type dashboardResponse struct {
	Daily    []dashboardDailyPoint `json:"daily"`
	Nodes    []dashboardNode       `json:"nodes"`
	Models   []dashboardNode       `json:"models"`
	Lifetime *dashboardLifetime    `json:"lifetime"`
}

func round1(n float64) float64 { return math.Round(n*10) / 10 }

// latencyScoreFromMs and speedScoreFromTps are the two performance axes of the
// health score, banded exactly like upstream.
func latencyScoreFromMs(avgMs int64) int {
	switch {
	case avgMs < 2000:
		return 100
	case avgMs < 5000:
		return 80
	case avgMs < 10000:
		return 60
	case avgMs < 20000:
		return 40
	default:
		return 20
	}
}

func speedScoreFromTps(tps float64) int {
	switch {
	case tps >= 80:
		return 100
	case tps >= 50:
		return 80
	case tps >= 25:
		return 60
	case tps >= 10:
		return 40
	default:
		return 20
	}
}

// computeScore weighs success 60%, latency 20%, speed 20%. A missing axis
// takes a neutral 50 rather than being rewarded with the success rate.
func computeScore(successRate float64, latencyScore, speedScore *int) int {
	ls := 50
	if latencyScore != nil {
		ls = *latencyScore
	}
	ss := 50
	if speedScore != nil {
		ss = *speedScore
	}
	return int(math.Round(successRate*0.6 + float64(ls)*0.2 + float64(ss)*0.2))
}

// dashboardDaily parses the usageDaily rollup into the trailing-year heatmap
// window and the lifetime aggregates over every recorded day.
func dashboardDaily(rows []db.UsageDailyRow, now time.Time) ([]dashboardDailyPoint, *dashboardLifetime) {
	type dayData struct {
		Requests         int64   `json:"requests"`
		PromptTokens     int64   `json:"promptTokens"`
		CompletionTokens int64   `json:"completionTokens"`
		Cost             float64 `json:"cost"`
	}
	lifetime := &dashboardLifetime{}
	streak := 0
	var prevKey string
	active := map[string]bool{}
	byKey := map[string]*dayData{}
	for _, row := range rows { // rows arrive newest-first
		var d dayData
		if err := json.Unmarshal([]byte(row.Data), &d); err != nil {
			continue
		}
		parsed := d
		byKey[row.DateKey] = &parsed
		tokens := d.PromptTokens + d.CompletionTokens
		lifetime.TotalRequests += d.Requests
		lifetime.TotalTokens += tokens
		if tokens > lifetime.PeakTokens {
			lifetime.PeakTokens = tokens
			lifetime.PeakDate = row.DateKey
		}
		if d.Requests > 0 {
			active[row.DateKey] = true
			if prevKey != "" && dayDiff(prevKey, row.DateKey) == 1 {
				streak++
			} else {
				streak = 1
			}
			if streak > lifetime.LongestStreak {
				lifetime.LongestStreak = streak
			}
		} else {
			streak = 0
		}
		prevKey = row.DateKey
	}
	// The current streak counts back from today (or yesterday while today is
	// still empty — the day is not over yet).
	cursor := now.UTC().Format("2006-01-02")
	if !active[cursor] {
		if t, err := time.Parse("2006-01-02", cursor); err == nil {
			cursor = t.AddDate(0, 0, -1).Format("2006-01-02")
		}
	}
	for active[cursor] {
		lifetime.CurrentStreak++
		t, err := time.Parse("2006-01-02", cursor)
		if err != nil {
			break
		}
		cursor = t.AddDate(0, 0, -1).Format("2006-01-02")
	}

	// Heatmap window: the trailing 365 days, oldest first; empty days stay out
	// of the payload, the component fills the grid.
	var points []dashboardDailyPoint
	today := now.UTC()
	for i := 364; i >= 0; i-- {
		key := today.AddDate(0, 0, -i).Format("2006-01-02")
		d, ok := byKey[key]
		if !ok || d.Requests == 0 {
			continue
		}
		points = append(points, dashboardDailyPoint{
			Date:     key,
			Requests: d.Requests,
			Tokens:   d.PromptTokens + d.CompletionTokens,
			Cost:     d.Cost,
		})
	}
	return points, lifetime
}

// dayDiff is the whole-day distance between two YYYY-MM-DD keys.
func dayDiff(later, earlier string) int {
	a, err1 := time.Parse("2006-01-02", later)
	b, err2 := time.Parse("2006-01-02", earlier)
	if err1 != nil || err2 != nil {
		return -1
	}
	return int(a.Sub(b) / (24 * time.Hour))
}


// perfAgg accumulates latency observations per provider (and per model).
type perfAgg struct {
	sum, count          int64
	ttftSum, ttftCount  int64
	tokensSum, durMsSum int64
}

// detailPerf is the requestDetails observation the perf feed reads. The
// numbers arrive from the repository, which reads them out of the stored
// payload: the payload itself carries the request messages and the response
// body, and shipping it for every retained request made this read scale with
// the size of the stored traffic.
type detailPerf struct {
	Latency struct {
		TTFT  int64 `json:"ttft"`
		Total int64 `json:"total"`
	} `json:"latency"`
	Tokens struct {
		CompletionTokens int64 `json:"completion_tokens"`
		OutputTokens     int64 `json:"output_tokens"`
	} `json:"tokens"`
}

// detailPerfFromRow turns a stored observation into the shape the fold reads.
func detailPerfFromRow(row db.RequestDetailPerfRow) detailPerf {
	var p detailPerf
	p.Latency.TTFT = row.TTFTMs
	p.Latency.Total = row.TotalMs
	p.Tokens.CompletionTokens = row.CompletionTokens
	p.Tokens.OutputTokens = row.OutputTokens
	return p
}

// foldPerf adds one latency observation to an accumulator, deriving the
// streaming duration. A burst faster than 300 tok/s is a buffered flush (the
// whole answer arrived at once), so the end-to-end total is the honest
// duration there — dampening that keeps fast-but-honest streams from
// triggering false alerts.
func foldPerf(agg *perfAgg, p detailPerf) {
	if p.Latency.Total <= 0 {
		return
	}
	agg.sum += p.Latency.Total
	agg.count++
	if p.Latency.TTFT > 0 {
		agg.ttftSum += p.Latency.TTFT
		agg.ttftCount++
	}
	out := p.Tokens.CompletionTokens
	if out == 0 {
		out = p.Tokens.OutputTokens
	}
	if out <= 0 {
		return
	}
	var duration int64
	switch {
	case p.Latency.TTFT > 0 && p.Latency.Total > p.Latency.TTFT && p.Latency.Total-p.Latency.TTFT >= 50:
		duration = p.Latency.Total - p.Latency.TTFT
	case p.Latency.Total >= 50:
		duration = p.Latency.Total
	}
	if duration > 0 && float64(out)/float64(duration)*1000 > 300 && p.Latency.Total >= 50 {
		duration = p.Latency.Total
	}
	if duration > 0 {
		agg.tokensSum += out
		agg.durMsSum += duration
	}
}

func (a *perfAgg) avgLatency() *int64 {
	if a.count == 0 {
		return nil
	}
	v := a.sum / a.count
	return &v
}

func (a *perfAgg) avgTtft() *int64 {
	if a.ttftCount == 0 {
		return nil
	}
	v := a.ttftSum / a.ttftCount
	return &v
}

func (a *perfAgg) avgSpeed() *float64 {
	if a.durMsSum == 0 || a.tokensSum == 0 {
		return nil
	}
	v := round1(float64(a.tokensSum) / (float64(a.durMsSum) / 1000))
	return &v
}

// dashboardPerf indexes the requestDetails ring by provider and by
// provider|model. The ring is the gateway's only per-request latency source
// (usageHistory meta carries none), so health covers the retained window.
func dashboardPerf(rows []db.RequestDetailPerfRow) (node, model map[string]*perfAgg) {
	node, model = map[string]*perfAgg{}, map[string]*perfAgg{}
	fold := func(m map[string]*perfAgg, k string, p detailPerf) {
		agg := m[k]
		if agg == nil {
			agg = &perfAgg{}
			m[k] = agg
		}
		foldPerf(agg, p)
	}
	for _, row := range rows {
		p := detailPerfFromRow(row)
		fold(node, row.Provider, p)
		fold(model, row.Provider+"|"+row.Model, p)
	}
	return node, model
}

// buildDashboardNodes turns group rows into scored node rows, keeping only
// entries with at least minRequests of traffic. Rows without a health score
// (no latency sample) sort last and the frontend drops them from the list.
func buildDashboardNodes(rows []db.UsageGroupRow, perf map[string]*perfAgg, minRequests int, asModel bool) []dashboardNode {
	var out []dashboardNode
	for _, row := range rows {
		if row.Requests < minRequests {
			continue
		}
		key := row.Provider
		if asModel {
			key = row.Provider + "|" + row.Model
		}
		successRate := 0.0
		if row.Requests > 0 {
			successRate = round1(float64(row.Requests-row.Errors) / float64(row.Requests) * 100)
		}
		n := dashboardNode{
			Provider:         row.Provider,
			Requests:         row.Requests,
			Errors:           row.Errors,
			SuccessRate:      successRate,
			PromptTokens:     int64(row.PromptTokens),
			CompletionTokens: int64(row.CompletionTokens),
			Cost:             row.Cost,
			LastUsed:         row.LastUsed,
		}
		if asModel {
			n.Model = row.Model
		} else {
			n.Name = row.Provider
		}
		if agg := perf[key]; agg != nil {
			n.AvgLatencyMs = agg.avgLatency()
			n.AvgTtftMs = agg.avgTtft()
			n.AvgSpeed = agg.avgSpeed()
		}
		if n.AvgLatencyMs != nil {
			n.HasPerfData = true
			ls := latencyScoreFromMs(*n.AvgLatencyMs)
			var ss *int
			if n.AvgSpeed != nil {
				v := speedScoreFromTps(*n.AvgSpeed)
				ss = &v
			}
			score := computeScore(successRate, &ls, ss)
			n.Score = &score
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := -1, -1
		if out[i].Score != nil {
			si = *out[i].Score
		}
		if out[j].Score != nil {
			sj = *out[j].Score
		}
		return si > sj
	})
	return out
}

// HandleUsageDashboard serves GET /api/usage/dashboard: the Details section's
// lifetime cards, activity heatmap, and node-health table.
func HandleUsageDashboard(repo *db.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		allDays, err := repo.GetUsageDailyRange(allUsageDays)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		daily, lifetime := dashboardDaily(allDays, now)

		weekAgo := now.AddDate(0, 0, -6).UTC().Format(time.RFC3339)
		// The Top Model card and the cache-hit rate are aggregates over the
		// trailing week. They run in SQL: the row-level fold this replaced
		// shipped every row of the window — and the JSON payload each row
		// carries — into the process to sum two numbers there.
		if provider, model, tokens, ok, err := repo.GetUsageTopModelSince(weekAgo); err == nil && ok {
			lifetime.TopModel = &dashboardTopModel{Model: model, Provider: provider, Tokens: tokens}
		}
		if hit, total, err := repo.GetUsageCacheHitTotalsSince(weekAgo); err == nil && total > 0 {
			rate := round1(float64(hit) / float64(total) * 100)
			lifetime.CacheHitRate = &rate
		}

		perfRows, err := repo.GetRequestDetailPerf()
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		nodePerf, modelPerf := dashboardPerf(perfRows)

		nodeRows, err := repo.GetUsageGroupByProviderSince(weekAgo)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		modelRows, err := repo.GetUsageGroupByModelSince(weekAgo)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		handlerutil.WriteJSON(w, http.StatusOK, dashboardResponse{
			Daily:    daily,
			Nodes:    buildDashboardNodes(nodeRows, nodePerf, 50, false),
			Models:   buildDashboardNodes(modelRows, modelPerf, 10, true),
			Lifetime: lifetime,
		})
	}
}
