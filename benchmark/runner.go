//go:build ignore

// Benchmark runner. It measures a gateway the operator already started
// (G05/G00 sandbox, synthetic state). It does not boot a gateway, start or
// kill processes, or touch SQLite — a fake in-process proxy made earlier
// results meaningless, so the target is now explicit.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Target      string
	APIKey      string
	Concurrency []int
	Requests    int
	Stream      bool
	Timeout     time.Duration
}

type BenchResult struct {
	Concurrency int
	TotalReqs   uint64
	SuccessReqs uint64
	FailedReqs  uint64
	SemanticErr uint64
	Duration    time.Duration
	RPS         float64
	MinLatency  time.Duration
	AvgLatency  time.Duration
	P50         time.Duration
	P95         time.Duration
	P99         time.Duration
	MaxLatency  time.Duration
}

type sample struct {
	HeaderLatency time.Duration
	FirstBodyByte time.Duration
	Total         time.Duration
	Status        int
	Body          []byte
}

func measureOnce(ctx context.Context, client *http.Client, target string, body []byte, key string) (sample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return sample{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return sample{}, err
	}
	defer resp.Body.Close()
	got := sample{HeaderLatency: time.Since(started), Status: resp.StatusCode}
	first := make([]byte, 1)
	n, readErr := io.ReadFull(resp.Body, first)
	got.FirstBodyByte = time.Since(started)
	if readErr != nil && readErr != io.EOF {
		return got, readErr
	}
	const maxBody = 10 << 20
	rest, err := io.ReadAll(io.LimitReader(resp.Body, maxBody-int64(n)+1))
	got.Total = time.Since(started)
	got.Body = append(first[:n], rest...)
	if len(got.Body) > maxBody {
		return got, fmt.Errorf("benchmark response exceeds %d bytes", maxBody)
	}
	return got, err
}

// completeBody reports whether a response carries a terminal event or a
// well-formed JSON document. A body cut short by maxBody, a dead socket, or a
// proxy that stopped mid-stream is not a success — counting it as one is how a
// truncated run reports a fake speedup.
func completeBody(stream bool, body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if !stream {
		var v any
		return json.Unmarshal(body, &v) == nil
	}
	return bytes.Contains(body, []byte("data: [DONE]")) ||
		bytes.Contains(body, []byte(`"type":"message_stop"`)) ||
		bytes.Contains(body, []byte(`"type":"response.completed"`))
}

func main() {
	var cfg Config
	var concurrencyCSV string
	flag.StringVar(&cfg.Target, "target", os.Getenv("BENCH_TARGET"), "gateway chat/completions URL under test")
	flag.StringVar(&cfg.APIKey, "key", os.Getenv("BENCH_KEY"), "API key for the sandbox gateway")
	flag.StringVar(&concurrencyCSV, "concurrency", "1,10,25,50,100", "comma-separated concurrency levels")
	flag.IntVar(&cfg.Requests, "requests", 500, "requests per concurrency level")
	flag.BoolVar(&cfg.Stream, "stream", true, "send stream:true and require a terminal event")
	flag.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "per-request timeout")
	flag.Parse()

	if cfg.Target == "" || cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "target and key are required: this runner measures an operator-started gateway, it does not start one")
		os.Exit(2)
	}
	for _, raw := range bytes.Split([]byte(concurrencyCSV), []byte(",")) {
		var c int
		if _, err := fmt.Sscanf(string(bytes.TrimSpace(raw)), "%d", &c); err != nil || c < 1 {
			fmt.Fprintf(os.Stderr, "invalid concurrency level %q\n", raw)
			os.Exit(2)
		}
		cfg.Concurrency = append(cfg.Concurrency, c)
	}

	fmt.Println("==================================================")
	fmt.Println("9Router benchmark — measuring the real gateway")
	fmt.Printf("target=%s stream=%v requests=%d\n", cfg.Target, cfg.Stream, cfg.Requests)
	fmt.Println("==================================================")

	var results []BenchResult
	for _, c := range cfg.Concurrency {
		fmt.Printf("running: %d workers, %d requests...\n", c, cfg.Requests)
		res := runBenchmark(cfg, c)
		results = append(results, res)
		fmt.Printf("   -> RPS: %.2f | p50: %v | p95: %v | p99: %v | success: %d | failed: %d | semantic: %d\n",
			res.RPS, res.P50, res.P95, res.P99, res.SuccessReqs, res.FailedReqs, res.SemanticErr)
	}

	printResultsTable(results)
}

func runBenchmark(cfg Config, concurrency int) BenchResult {
	payload, _ := json.Marshal(map[string]any{
		"model":    "gpt-4o",
		"stream":   cfg.Stream,
		"messages": []map[string]string{{"role": "user", "content": "Hi"}},
	})

	var successReqs, failedReqs, semanticErr uint64
	latencies := make([]time.Duration, cfg.Requests)

	tr := &http.Transport{
		MaxIdleConns:        concurrency * 2,
		MaxIdleConnsPerHost: concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: cfg.Timeout}

	// Work queue by index, not requests/concurrency: integer division silently
	// drops the remainder, so a run of 503 requests over 10 workers would
	// dispatch 500 and still look complete.
	var next int64 = -1
	var wg sync.WaitGroup
	var mu sync.Mutex
	ctx := context.Background()

	startTime := time.Now()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := int(atomic.AddInt64(&next, 1))
				if idx >= cfg.Requests {
					return
				}
				got, err := measureOnce(ctx, client, cfg.Target, payload, cfg.APIKey)
				switch {
				case err != nil || got.Status != http.StatusOK:
					atomic.AddUint64(&failedReqs, 1)
				case !completeBody(cfg.Stream, got.Body):
					atomic.AddUint64(&semanticErr, 1)
				default:
					atomic.AddUint64(&successReqs, 1)
					mu.Lock()
					latencies[idx] = got.Total
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	totalDuration := time.Since(startTime)

	var valid []time.Duration
	var sum time.Duration
	for _, d := range latencies {
		if d > 0 {
			valid = append(valid, d)
			sum += d
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i] < valid[j] })

	minLat, maxLat, p50, p95, p99, avgLat := time.Duration(0), time.Duration(0), time.Duration(0), time.Duration(0), time.Duration(0), time.Duration(0)
	if len(valid) > 0 {
		minLat = valid[0]
		maxLat = valid[len(valid)-1]
		p50 = valid[int(float64(len(valid))*0.50)]
		p95 = valid[int(float64(len(valid))*0.95)]
		p99 = valid[int(float64(len(valid))*0.99)]
		avgLat = sum / time.Duration(len(valid))
	}

	totalReqs := successReqs + failedReqs + semanticErr
	rps := float64(successReqs) / totalDuration.Seconds()

	return BenchResult{
		Concurrency: concurrency,
		TotalReqs:   totalReqs,
		SuccessReqs: successReqs,
		FailedReqs:  failedReqs,
		SemanticErr: semanticErr,
		Duration:    totalDuration,
		RPS:         rps,
		MinLatency:  minLat,
		AvgLatency:  avgLat,
		P50:         p50,
		P95:         p95,
		P99:         p99,
		MaxLatency:  maxLat,
	}
}

func printResultsTable(results []BenchResult) {
	fmt.Println("\n==================================================")
	fmt.Println("BENCHMARK SUMMARY RESULTS")
	fmt.Println("==================================================")
	fmt.Printf("| Concurrency | Total Reqs | Success | Failed | Semantic | RPS | Avg Latency | p50 | p95 | p99 | Max Latency |\n")
	fmt.Printf("|-------------|------------|---------|--------|----------|-----|-------------|-----|-----|-----|-------------|\n")

	failedOverall := false
	for _, r := range results {
		if r.SuccessReqs == 0 {
			failedOverall = true
		}
		fmt.Printf("| %11d | %10d | %7d | %6d | %8d | %7.1f | %11v | %3v | %3v | %3v | %11v |\n",
			r.Concurrency, r.TotalReqs, r.SuccessReqs, r.FailedReqs, r.SemanticErr, r.RPS, r.AvgLatency, r.P50, r.P95, r.P99, r.MaxLatency)
	}
	fmt.Println("==================================================")
	if failedOverall {
		fmt.Fprintln(os.Stderr, "at least one level recorded zero successes — this is a failed run, not a speedup")
		os.Exit(1)
	}
}
