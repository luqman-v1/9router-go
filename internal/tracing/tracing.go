// Package tracing provides lightweight in-process request tracing with
// latency percentiles per provider+model, exported over an HTTP endpoint.
// It is intentionally dependency-free (stdlib only) — swap for the full
// OpenTelemetry SDK when an OTLP collector is needed.
package tracing

import (
	"encoding/json"
	"math"
	"sort"
	"sync"
	"time"
)

// Span is one completed upstream request.
type Span struct {
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs"`
	TTFTMs     int64  `json:"ttftMs,omitempty"`
	At         int64  `json:"at"` // unix ms
}

var (
	mu     sync.RWMutex
	spans  []Span
	limit  = 2000 // ring: keep the last N spans in memory
	startT = time.Now()
)

// Record appends a completed span, trimming the ring to limit.
func Record(s Span) {
	if s.At == 0 {
		s.At = time.Now().UnixMilli()
	}
	mu.Lock()
	spans = append(spans, s)
	if len(spans) > limit {
		spans = spans[len(spans)-limit:]
	}
	mu.Unlock()
}

// Recent returns the last n spans (n <= 0 returns all kept).
func Recent(n int) []Span {
	mu.RLock()
	defer mu.RUnlock()
	if n <= 0 || n >= len(spans) {
		out := make([]Span, len(spans))
		copy(out, spans)
		return out
	}
	out := make([]Span, n)
	copy(out, spans[len(spans)-n:])
	return out
}

// LatencyStats is the p50/p95/p99 latency summary for one provider+model key.
type LatencyStats struct {
	Key        string  `json:"key"`
	Count      int     `json:"count"`
	P50Ms      float64 `json:"p50Ms"`
	P95Ms      float64 `json:"p95Ms"`
	P99Ms      float64 `json:"p99Ms"`
	Errors     int     `json:"errors"`
	LastAt     int64   `json:"lastAt"`
	UptimeSecs int64   `json:"uptimeSecs"`
}

// LatencyByKey aggregates spans into per provider+model latency stats.
func LatencyByKey() []LatencyStats {
	mu.RLock()
	defer mu.RUnlock()
	buckets := make(map[string][]int64)
	errs := make(map[string]int)
	last := make(map[string]int64)
	for _, s := range spans {
		key := s.Provider + "/" + s.Model
		buckets[key] = append(buckets[key], s.DurationMs)
		if s.Status != "200" {
			errs[key]++
		}
		if s.At > last[key] {
			last[key] = s.At
		}
	}
	out := make([]LatencyStats, 0, len(buckets))
	for key, durs := range buckets {
		out = append(out, LatencyStats{
			Key:        key,
			Count:      len(durs),
			P50Ms:      percentile(durs, 50),
			P95Ms:      percentile(durs, 95),
			P99Ms:      percentile(durs, 99),
			Errors:     errs[key],
			LastAt:     last[key],
			UptimeSecs: int64(time.Since(startT).Seconds()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// percentile returns the p-th percentile of durations (0..100).
func percentile(durs []int64, p float64) float64 {
	if len(durs) == 0 {
		return 0
	}
	sorted := make([]int64, len(durs))
	copy(sorted, durs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return float64(sorted[idx])
}

// JSON renders the recent-traces payload for the debug endpoint.
func JSON(n int) ([]byte, error) {
	return json.Marshal(struct {
		Spans  []Span         `json:"spans"`
		Latency []LatencyStats `json:"latency"`
	}{
		Spans:   Recent(n),
		Latency: LatencyByKey(),
	})
}
