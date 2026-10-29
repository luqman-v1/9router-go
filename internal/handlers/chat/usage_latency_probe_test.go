package chat

// Latency probe for the synchronous usage-write tail that logUsage runs on
// the response goroutine today. Each scenario replicates the exact write
// sequence of a successful request (InsertUsageHistory + InsertRequestDetail
// + upsertDailyUsage) against a SQLite file built with the production pragmas
// (WAL, busy_timeout 5000, 4 pooled connections), and prints p50/p95/max.
//
// Run: go test ./internal/handlers/chat/ -run TestUsageWriteLatencyProbe -v
// The numbers are informational, not a pass/fail contract; the assertions
// only bound the obviously-broken cases so the probe cannot silently rot.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/dbtest"
)

type latProbe struct {
	durations []time.Duration
}

func (p *latProbe) record(d time.Duration) { p.durations = append(p.durations, d) }

func (p *latProbe) report(tb testing.TB, name string) {
	d := append([]time.Duration(nil), p.durations...)
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	if len(d) == 0 {
		return
	}
	pct := func(q float64) time.Duration { return d[int(float64(len(d)-1)*q)] }
	tb.Logf("%-34s n=%-4d p50=%8s  p95=%8s  max=%8s", name, len(d), pct(0.50), pct(0.95), d[len(d)-1])
}

// usageWriteSequence replicates one successful request's synchronous tail.
func usageWriteSequence(tb testing.TB, h *ChatHandler, detailKB int) {
	h.Repo.InsertUsageHistory("openai-compatible-chat-probe", "gpt-probe", "conn-1", "sk-****abcd", "/v1/chat/completions",
		12000, 3500, 0.021, "success", 15500, `{"provider":"x"}`, `{"prompt_tokens":12000,"completion_tokens":3500,"total_tokens":15500}`)
	if detailKB > 0 {
		h.Repo.InsertRequestDetail("probe-req", "openai-compatible-chat-probe", "gpt-probe", "conn-1", "success", probeDetailJSON(detailKB))
	}
	h.upsertDailyUsage("openai-compatible-chat-probe", "gpt-probe", "/v1/chat/completions", "conn-1", "sk-****abcd", 12000, 3500, 800, 0.021)
}

// probeDetailJSON builds a requestDetails payload of roughly detailKB, matching
// the ~20 KB row shape production writes (truncated messages + response text).
func probeDetailJSON(detailKB int) string {
	unit := fmt.Sprintf("%04096d", 1) // 4096 chars
	payload := struct {
		Messages []map[string]string `json:"messages"`
		Response string              `json:"response"`
	}{}
	for len(payload.Messages) < detailKB {
		payload.Messages = append(payload.Messages, map[string]string{
			"role": "user", "content": unit,
		})
	}
	payload.Response = unit
	b, _ := json.Marshal(payload)
	return string(b)
}

func newProbeHandler(tb testing.TB) *ChatHandler {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "probe.db")
	sqlDB, err := db.OpenDatabase(path)
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(func() { _ = sqlDB.Close() })
	if err := dbtest.CreateTables(sqlDB); err != nil {
		tb.Fatalf("tables: %v", err)
	}
	return &ChatHandler{Repo: db.NewRepo(sqlDB)}
}

func TestUsageWriteLatencyProbe(t *testing.T) {
	t.Run("idle_small_detail", func(t *testing.T) {
		h := newProbeHandler(t)
		var p latProbe
		for i := 0; i < 200; i++ {
			start := time.Now()
			usageWriteSequence(t, h, 2)
			p.record(time.Since(start))
		}
		p.report(t, "idle, 2KB detail")
	})

	t.Run("idle_20kb_detail", func(t *testing.T) {
		h := newProbeHandler(t)
		var p latProbe
		for i := 0; i < 200; i++ {
			start := time.Now()
			usageWriteSequence(t, h, 20)
			p.record(time.Since(start))
		}
		p.report(t, "idle, 20KB detail")
	})

	t.Run("daily_blob_grown", func(t *testing.T) {
		h := newProbeHandler(t)
		// Accumulate a realistic busy day first: 2400 requests across
		// 25 providers x 30 models, through the same RMW production uses.
		providers, models := 25, 30
		n := 0
		for pi := 0; pi < providers; pi++ {
			for mi := 0; mi < models; mi++ {
				for r := 0; r < 3; r++ {
					h.upsertDailyUsage(
						fmt.Sprintf("prov-%02d", pi), fmt.Sprintf("model-%02d", mi),
						"/v1/chat/completions", "conn-1", "sk-****abcd", 1000, 300, 50, 0.002)
					n++
				}
			}
		}
		blob, _ := h.Repo.GetUsageDaily(time.Now().UTC().Format("2006-01-02"))
		t.Logf("grown daily blob: %d requests accumulated, %.1f KB", n, float64(len(blob))/1024)

		var p latProbe
		for i := 0; i < 200; i++ {
			start := time.Now()
			usageWriteSequence(t, h, 2)
			p.record(time.Since(start))
		}
		p.report(t, "after a busy day (2KB detail)")
	})

	t.Run("response_path_cost_after_busy_day", func(t *testing.T) {
		h := newProbeHandler(t)
		providers, models := 25, 30
		for pi := 0; pi < providers; pi++ {
			for mi := 0; mi < models; mi++ {
				for r := 0; r < 3; r++ {
					h.upsertDailyUsage(
						fmt.Sprintf("prov-%02d", pi), fmt.Sprintf("model-%02d", mi),
						"/v1/chat/completions", "conn-1", "sk-****abcd", 1000, 300, 50, 0.002)
				}
			}
		}
		// What the response goroutine pays per request once the writes leave
		// the path: enqueue only. The landings are flushed before returning.
		SetUsageWritesSynchronousForTest(false)
		defer SetUsageWritesSynchronousForTest(true)
		var p latProbe
		for i := 0; i < 200; i++ {
			start := time.Now()
			enqueueUsageWrite(func() { usageWriteSequence(t, h, 2) })
			p.record(time.Since(start))
		}
		if !FlushUsageWrites(30 * time.Second) {
			t.Fatal("usage writes did not drain")
		}
		p.report(t, "response path, async (after busy day)")
	})

	t.Run("contended", func(t *testing.T) {
		h := newProbeHandler(t)
		// 100k-row ledger, like a gateway that has been running for weeks.
		if err := seedUsageHistory(h.Repo, 100_000); err != nil {
			t.Fatalf("seed: %v", err)
		}

		stop := make(chan struct{})
		var wg sync.WaitGroup

		// The dashboard's 5s poll: 24h fold over the ledger.
		wg.Add(1)
		go func() {
			defer wg.Done()
			cutoff := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = h.Repo.GetUsageHistorySince(cutoff)
				}
			}
		}()

		// Six rival writers: concurrent requests finishing elsewhere.
		for w := 0; w < 6; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						h.Repo.InsertUsageHistory("openai-compatible-chat-probe", "gpt-probe", "conn-1", "sk-****abcd", "/v1/chat/completions",
							12000, 3500, 0.021, "success", 15500, `{"provider":"x"}`, `{"prompt_tokens":12000}`)
					}
				}
			}()
		}

		var p latProbe
		for i := 0; i < 200; i++ {
			start := time.Now()
			usageWriteSequence(t, h, 20)
			p.record(time.Since(start))
		}
		close(stop)
		wg.Wait()
		p.report(t, "contended (dash poll + 6 writers)")
	})
}

// seedUsageHistory bulk-inserts n ledger rows in one transaction.
func seedUsageHistory(repo *db.Repo, n int) error {
	tx, err := repo.RawDB().Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO usageHistory
		(timestamp, provider, model, connectionId, apiKey, endpoint, promptTokens, completionTokens, cost, status, tokens, meta)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer func() { _ = stmt.Close() }()
	ts := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < n; i++ {
		if _, err := stmt.Exec(ts, "prov", fmt.Sprintf("model-%d", i%40), "conn-1", "sk-****abcd", "/v1/chat/completions",
			1000, 300, 0.002, "success", `{"prompt_tokens":1000}`, "{}"); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
