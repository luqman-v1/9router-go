package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

func TestModelFamilyName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "strips provider prefix", input: "openai/gpt-5", expected: "gpt"},
		{name: "drops hyphenated version", input: "gpt-5.6-sol", expected: "gpt"},
		{name: "drops attached version", input: "qwen3.8-flash", expected: "qwen"},
		{name: "folds qoder qfmodel codename", input: "qfmodel", expected: "qwen"},
		{name: "folds qoder dfmodel codename", input: "dfmodel", expected: "deepseek"},
		{name: "folds brand stepaudio", input: "stepaudio-tts", expected: "step"},
		{name: "uuid custom channel is other", input: "85d2a64e-abc:custom", expected: "other"},
		{name: "empty model is unknown", input: "", expected: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelFamilyName(tt.input); got != tt.expected {
				t.Errorf("modelFamilyName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestParseChartPeriod(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantKind  string
		wantCount int
	}{
		{name: "today is hourly", input: "today", wantKind: "today", wantCount: 24},
		{name: "24h is hourly", input: "24h", wantKind: "24h", wantCount: 24},
		{name: "30d is daily", input: "30d", wantKind: "daily", wantCount: 30},
		{name: "60d is daily", input: "60d", wantKind: "daily", wantCount: 60},
		{name: "all is unbounded", input: "all", wantKind: "all", wantCount: 0},
		{name: "unknown falls back to 7d", input: "bogus", wantKind: "daily", wantCount: 7},
		{name: "custom hourly window", input: "12h", wantKind: "hourly", wantCount: 12},
		{name: "custom daily window", input: "14d", wantKind: "daily", wantCount: 14},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, count := parseChartPeriod(tt.input)
			if kind != tt.wantKind || count != tt.wantCount {
				t.Errorf("parseChartPeriod(%q) = (%q, %d), want (%q, %d)",
					tt.input, kind, count, tt.wantKind, tt.wantCount)
			}
		})
	}
}

func TestFinalizeChartBuckets(t *testing.T) {
	buckets := []chartBucket{
		{Label: "a", ByModel: map[string]int64{"gpt": 100, "extra-a": 1}},
		{Label: "b", ByModel: map[string]int64{"gpt": 50, "extra-b": 2}},
	}
	finalizeChartBuckets(buckets, 1)
	if buckets[0].ByModel["gpt"] != 100 {
		t.Errorf("top family must survive: %+v", buckets[0].ByModel)
	}
	if buckets[0].ByModel["other"] != 1 {
		t.Errorf("tail family must fold into other: %+v", buckets[0].ByModel)
	}
	if buckets[1].ByModel["other"] != 2 {
		t.Errorf("tail family must fold into other: %+v", buckets[1].ByModel)
	}
}

func setupChartTestDB(t *testing.T) *db.Repo {
	t.Helper()
	conn, err := db.OpenDatabase(t.TempDir() + "/chart.sqlite")
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := db.EnsureCoreSchema(conn); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	repo := db.NewRepo(conn)
	t.Cleanup(func() { conn.Close() })
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if err := repo.UpsertUsageDaily(yesterday, `{"promptTokens":1000,"completionTokens":500,"cost":1.5}`); err != nil {
		t.Fatalf("seed usageDaily: %v", err)
	}
	if err := repo.InsertUsageHistory("openai", "gpt-5", "conn-1", "sk-1", "/v1/chat/completions",
		100, 50, 1.5, "ok", 150, "{}", "{}"); err != nil {
		t.Fatalf("seed usageHistory: %v", err)
	}
	return repo
}

func TestHandleUsageChart(t *testing.T) {
	repo := setupChartTestDB(t)

	t.Run("daily window returns buckets with totals and family split", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/usage/chart?period=7d", nil)
		HandleUsageChart(repo).ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var buckets []chartBucket
		if err := json.Unmarshal(rec.Body.Bytes(), &buckets); err != nil {
			t.Fatalf("decode buckets: %v", err)
		}
		if len(buckets) != 7 {
			t.Fatalf("len(buckets) = %d, want 7", len(buckets))
		}
		var total int64
		for _, b := range buckets {
			total += b.Tokens
		}
		if total < 1500 {
			t.Errorf("chart total = %d, want at least the seeded 1500", total)
		}
	})

	t.Run("hourly window returns 24 labeled buckets", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/usage/chart?period=today", nil)
		HandleUsageChart(repo).ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var buckets []chartBucket
		if err := json.Unmarshal(rec.Body.Bytes(), &buckets); err != nil {
			t.Fatalf("decode buckets: %v", err)
		}
		if len(buckets) != 24 {
			t.Fatalf("len(buckets) = %d, want 24", len(buckets))
		}
		for _, b := range buckets {
			if !strings.Contains(b.Label, ":") {
				t.Errorf("hourly label %q must be HH:MM", b.Label)
				break
			}
		}
	})

	t.Run("unknown period falls back to 7d", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/usage/chart?period=bogus", nil)
		HandleUsageChart(repo).ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var buckets []chartBucket
		if err := json.Unmarshal(rec.Body.Bytes(), &buckets); err != nil {
			t.Fatalf("decode buckets: %v", err)
		}
		if len(buckets) != 7 {
			t.Fatalf("len(buckets) = %d, want 7", len(buckets))
		}
	})
}

func TestDailyChartBucketsAll(t *testing.T) {
	repo := setupChartTestDB(t)
	now := time.Now().UTC()
	buckets := dailyChartBuckets(repo, now, 0)
	if len(buckets) < 2 {
		t.Fatalf("all-period must cover both seeded days, got %d buckets", len(buckets))
	}
}
