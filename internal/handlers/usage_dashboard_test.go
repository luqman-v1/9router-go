package handlers

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func TestLatencySpeedScoreBands(t *testing.T) {
	tests := []struct {
		name     string
		avgMs    int64
		expected int
	}{
		{name: "fast", avgMs: 1999, expected: 100},
		{name: "moderate", avgMs: 4999, expected: 80},
		{name: "slow", avgMs: 9999, expected: 60},
		{name: "very slow", avgMs: 19999, expected: 40},
		{name: "crawl", avgMs: 20000, expected: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := latencyScoreFromMs(tt.avgMs); got != tt.expected {
				t.Errorf("latencyScoreFromMs(%d) = %d, want %d", tt.avgMs, got, tt.expected)
			}
		})
	}
}

func TestComputeScore(t *testing.T) {
	full := 100
	missing := 20
	tests := []struct {
		name        string
		successRate float64
		latency     *int
		speed       *int
		expected    int
	}{
		{name: "all axes present", successRate: 100, latency: &full, speed: &full, expected: 100},
		{name: "missing speed takes neutral 50", successRate: 100, latency: &full, speed: nil, expected: 90},
		{name: "missing latency takes neutral 50", successRate: 100, latency: nil, speed: &missing, expected: 74},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeScore(tt.successRate, tt.latency, tt.speed); got != tt.expected {
				t.Errorf("computeScore(%v, %v, %v) = %d, want %d", tt.successRate, tt.latency, tt.speed, got, tt.expected)
			}
		})
	}
}

func TestDashboardDailyLifetimeAndStreaks(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	mk := func(requests, prompt, completion int64) string {
		b, _ := json.Marshal(map[string]any{
			"requests": requests, "promptTokens": prompt, "completionTokens": completion, "cost": 1.0,
		})
		return string(b)
	}
	rows := []db.UsageDailyRow{
		{DateKey: "2026-10-17", Data: mk(5, 100, 50)},
		{DateKey: "2026-10-16", Data: mk(9, 500, 300)},
		{DateKey: "2026-10-15", Data: mk(0, 0, 0)},
		{DateKey: "2026-10-14", Data: mk(2, 40, 10)},
	}
	daily, lifetime := dashboardDaily(rows, now)
	if lifetime.TotalRequests != 16 {
		t.Errorf("TotalRequests = %d, want 16", lifetime.TotalRequests)
	}
	if lifetime.TotalTokens != 1000 {
		t.Errorf("TotalTokens = %d, want 1000", lifetime.TotalTokens)
	}
	if lifetime.PeakTokens != 800 || lifetime.PeakDate != "2026-10-16" {
		t.Errorf("peak = (%d, %s), want (800, 2026-10-16)", lifetime.PeakTokens, lifetime.PeakDate)
	}
	// Today + yesterday are active, 10-15 breaks the run, so the current
	// streak stops at 2 and the longest is also 2.
	if lifetime.CurrentStreak != 2 || lifetime.LongestStreak != 2 {
		t.Errorf("streaks = (%d, %d), want (2, 2)", lifetime.CurrentStreak, lifetime.LongestStreak)
	}
	if len(daily) != 3 {
		t.Fatalf("len(daily) = %d, want 3 (empty days excluded)", len(daily))
	}
	if daily[0].Date != "2026-10-14" || daily[2].Date != "2026-10-17" {
		t.Errorf("daily must be oldest-first: %s..%s", daily[0].Date, daily[2].Date)
	}
}

func TestFoldPerfBufferedBurst(t *testing.T) {
	var agg perfAgg
	// 1000 tokens in 100ms total = 10k tok/s: a buffered flush, so the honest
	// duration is the end-to-end total, not the 90ms streaming window.
	foldPerf(&agg, detailPerf{Latency: struct {
		TTFT  int64 `json:"ttft"`
		Total int64 `json:"total"`
	}{TTFT: 10, Total: 100}, Tokens: struct {
		CompletionTokens int64 `json:"completion_tokens"`
		OutputTokens     int64 `json:"output_tokens"`
	}{CompletionTokens: 1000}})
	if agg.durMsSum != 100 {
		t.Errorf("durMsSum = %d, want the 100ms end-to-end total", agg.durMsSum)
	}
}

func setupDashboardTestDB(t *testing.T) *db.Repo {
	t.Helper()
	conn, err := db.OpenDatabase(t.TempDir() + "/dash.sqlite")
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	repo := db.NewRepo(conn)
	if err := repo.UpsertUsageDaily(time.Now().UTC().Format("2006-01-02"), `{"requests":60,"promptTokens":60000,"completionTokens":12000,"cost":3.0}`); err != nil {
		t.Fatalf("seed usageDaily: %v", err)
	}
	for range 60 {
		if err := repo.InsertUsageHistory("deepseek", "deepseek-chat", "conn-1", "sk-1", "/v1/chat/completions",
			1000, 200, 0.05, "success", 1200, `{"provider":"deepseek"}`, `{"prompt_tokens":1000,"completion_tokens":200,"cached_tokens":300}`); err != nil {
			t.Fatalf("seed usageHistory: %v", err)
		}
	}
	detail, _ := json.Marshal(map[string]any{
		"latency": map[string]int64{"ttft": 400, "total": 3000},
		"tokens":  map[string]int64{"completion_tokens": 200},
	})
	for i := range 60 {
		if err := repo.InsertRequestDetail(fmt.Sprintf("req-%02d", i), "deepseek", "deepseek-chat", "conn-1", "success", string(detail)); err != nil {
			t.Fatalf("seed requestDetails: %v", err)
		}
	}
	return repo
}

func TestHandleUsageDashboard(t *testing.T) {
	repo := setupDashboardTestDB(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/usage/dashboard", nil)
	HandleUsageDashboard(repo)(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var payload dashboardResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Lifetime == nil || payload.Lifetime.TotalRequests != 60 {
		t.Fatalf("lifetime = %+v, want 60 total requests", payload.Lifetime)
	}
	if payload.Lifetime.TopModel == nil || payload.Lifetime.TopModel.Model != "deepseek-chat" {
		t.Errorf("topModel = %+v, want deepseek-chat", payload.Lifetime.TopModel)
	}
	// Every seeded row is 1000 prompt tokens of which 300 are cached, so the
	// window-derived rate is 300/1000 = 30%.
	if payload.Lifetime.CacheHitRate == nil || *payload.Lifetime.CacheHitRate != 30 {
		t.Errorf("cacheHitRate = %v, want 30 from the trailing-7-day rows", payload.Lifetime.CacheHitRate)
	}
	if len(payload.Nodes) != 1 {
		t.Fatalf("len(nodes) = %d, want the one provider above 50 requests", len(payload.Nodes))
	}
	node := payload.Nodes[0]
	if node.Provider != "deepseek" || node.Requests != 60 {
		t.Errorf("node = %+v", node)
	}
	if node.Score == nil || !node.HasPerfData {
		t.Fatalf("node must carry a health score from the requestDetails ring: %+v", node)
	}
	// success 100% (60/60), latency 3000ms → 80, speed 200tok/2.6s ≈ 77 tok/s → 80.
	if *node.Score != 92 {
		t.Errorf("score = %d, want 92", *node.Score)
	}
	if len(payload.Models) != 1 {
		t.Fatalf("len(models) = %d, want the one model above 10 requests", len(payload.Models))
	}
}

func TestHandleUsageDashboardEmptyDB(t *testing.T) {
	conn, err := db.OpenDatabase(t.TempDir() + "/empty.sqlite")
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	rec := httptest.NewRecorder()
	HandleUsageDashboard(db.NewRepo(conn))(rec, httptest.NewRequest("GET", "/api/usage/dashboard", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var payload dashboardResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Lifetime == nil || payload.Lifetime.TotalRequests != 0 || payload.Lifetime.CacheHitRate != nil {
		t.Errorf("empty ledger must report zero lifetime totals and no cache rate: %+v", payload.Lifetime)
	}
}
