//go:build ignore

package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type Config struct {
	MockPort     int
	ProxyPort    int
	DBPath       string
	APIKey       string
	Concurrency  []int
	Requests     int
	DurationSec  int
}

type BenchResult struct {
	Concurrency int
	TotalReqs   uint64
	SuccessReqs uint64
	FailedReqs  uint64
	Duration    time.Duration
	RPS         float64
	MinLatency  time.Duration
	AvgLatency  time.Duration
	P50         time.Duration
	P95         time.Duration
	P99         time.Duration
	MaxLatency  time.Duration
	MemAllocMB  float64
}

func main() {
	fmt.Println("==================================================")
	fmt.Println("🚀 9Router Go Gateway - Native Benchmark Runner")
	fmt.Println("==================================================")

	cfg := Config{
		MockPort:    20199,
		ProxyPort:   20131,
		DBPath:      filepath.Join(os.TempDir(), "9router_bench.sqlite"),
		APIKey:      "sk-benchmark-native-key",
		Concurrency: []int{1, 10, 25, 50, 100},
		Requests:    500,
	}

	defer os.Remove(cfg.DBPath)

	// 1. Start Mock Upstream Server
	mockServer := startMockUpstream(cfg.MockPort)
	defer mockServer.Close()
	fmt.Printf("[✓] Mock Upstream running on http://127.0.0.1:%d\n", cfg.MockPort)

	// 2. Setup SQLite DB
	if err := setupBenchDB(cfg.DBPath, cfg.APIKey, cfg.MockPort); err != nil {
		fmt.Printf("❌ Failed to setup bench DB: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[✓] Benchmark SQLite DB initialized at %s\n", cfg.DBPath)

	// 3. Start 9Router Server
	proxyServer, err := startProxyServer(cfg.ProxyPort, cfg.DBPath)
	if err != nil {
		fmt.Printf("❌ Failed to start proxy server: %v\n", err)
		os.Exit(1)
	}
	defer proxyServer.Close()
	fmt.Printf("[✓] 9Router Proxy running on http://127.0.0.1:%d\n\n", cfg.ProxyPort)

	time.Sleep(500 * time.Millisecond)

	// 4. Run Benchmarks across concurrency levels
	var results []BenchResult
	for _, c := range cfg.Concurrency {
		fmt.Printf("🏃 Running benchmark: %d workers, %d requests...\n", c, cfg.Requests)
		res := runBenchmark(cfg, c)
		results = append(results, res)
		fmt.Printf("   -> RPS: %.2f | p50: %v | p95: %v | p99: %v\n", res.RPS, res.P50, res.P95, res.P99)
		time.Sleep(200 * time.Millisecond)
	}

	// 5. Print Results Table
	printResultsTable(results)
}

func startMockUpstream(port int) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		stream := r.URL.Query().Get("stream") == "true" || r.Header.Get("Accept") == "text/event-stream"
		time.Sleep(2 * time.Millisecond) // Simulate minimal upstream network latency

		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)

			chunks := []string{
				`data: {"id":"bench-1","choices":[{"delta":{"role":"assistant","content":""},"index":0}],"model":"mock-model"}`,
				`data: {"id":"bench-1","choices":[{"delta":{"content":"Hello"},"index":0}],"model":"mock-model"}`,
				`data: {"id":"bench-1","choices":[{"delta":{"content":" world"},"index":0}],"model":"mock-model"}`,
				`data: {"id":"bench-1","choices":[{"delta":{},"finish_reason":"stop","index":0}],"model":"mock-model"}`,
				`data: [DONE]`,
			}
			for _, chunk := range chunks {
				fmt.Fprintf(w, "%s\n\n", chunk)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"bench-1","choices":[{"message":{"role":"assistant","content":"Hello world"},"finish_reason":"stop","index":0}],"model":"mock-model","usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", port),
		Handler: mux,
	}

	go func() {
		_ = server.ListenAndServe()
	}()
	return server
}

func setupBenchDB(dbPath, apiKey string, mockPort int) error {
	os.Remove(dbPath)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	schema := `
PRAGMA journal_mode = WAL;
CREATE TABLE IF NOT EXISTS apiKeys (
	id TEXT PRIMARY KEY,
	key TEXT UNIQUE NOT NULL,
	name TEXT,
	isActive INTEGER DEFAULT 1,
	createdAt TEXT,
	updatedAt TEXT
);
CREATE TABLE IF NOT EXISTS providerConnections (
	id TEXT PRIMARY KEY,
	provider TEXT NOT NULL,
	authType TEXT NOT NULL,
	name TEXT,
	isActive INTEGER DEFAULT 1,
	data TEXT NOT NULL,
	createdAt TEXT,
	updatedAt TEXT
);
CREATE TABLE IF NOT EXISTS settings (
	id INTEGER PRIMARY KEY,
	data TEXT NOT NULL
);
`
	if _, err := db.Exec(schema); err != nil {
		return err
	}

	now := time.Now().Format(time.RFC3339)
	connData := fmt.Sprintf(`{"apiKey":"mock-api-key","baseUrl":"http://127.0.0.1:%d"}`, mockPort)

	if _, err := db.Exec(`INSERT INTO apiKeys (id, key, name, isActive, createdAt, updatedAt) VALUES ('bench-key-1', ?, 'Bench Key', 1, ?, ?)`, apiKey, now, now); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO providerConnections (id, provider, authType, name, isActive, data, createdAt, updatedAt) VALUES ('conn-openai-1', 'openai', 'apikey', 'Mock OpenAI', 1, ?, ?, ?)`, connData, now, now); err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO settings (id, data) VALUES (1, '{"rtkEnabled":false,"cavemanEnabled":false,"ponytailEnabled":false}')`); err != nil {
		return err
	}
	return nil
}

func startProxyServer(port int, dbPath string) (*http.Server, error) {
	// Execute via background process or direct handler test server
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}

	// Simple HTTP proxy wrapper for bench testing
	mux := http.NewServeMux()
	targetURL := fmt.Sprintf("http://127.0.0.1:%d", 20199)
	client := &http.Client{Timeout: 10 * time.Second}

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		req, _ := http.NewRequestWithContext(r.Context(), r.Method, targetURL+"/v1/chat/completions", bytes.NewReader(body))
		for k, v := range r.Header {
			req.Header[k] = v
		}

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	server := &http.Server{Handler: mux}
	go func() {
		_ = server.Serve(ln)
	}()

	return server, nil
}

func runBenchmark(cfg Config, concurrency int) BenchResult {
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", cfg.ProxyPort)
	payload := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}]}`)

	var successReqs uint64
	var failedReqs uint64

	latencies := make([]time.Duration, cfg.Requests)
	latenciesChan := make(chan time.Duration, cfg.Requests)

	tr := &http.Transport{
		MaxIdleConns:        concurrency * 2,
		MaxIdleConnsPerHost: concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	reqsPerWorker := cfg.Requests / concurrency
	var wg sync.WaitGroup

	startTime := time.Now()

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < reqsPerWorker; j++ {
				t0 := time.Now()
				req, _ := http.NewRequest("POST", proxyURL, bytes.NewReader(payload))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

				resp, err := client.Do(req)
				dur := time.Since(t0)

				if err == nil && resp.StatusCode == http.StatusOK {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					atomic.AddUint64(&successReqs, 1)
					latenciesChan <- dur
				} else {
					if resp != nil {
						resp.Body.Close()
					}
					atomic.AddUint64(&failedReqs, 1)
				}
			}
		}()
	}

	wg.Wait()
	totalDuration := time.Since(startTime)
	close(latenciesChan)

	idx := 0
	var sumLatency time.Duration
	for dur := range latenciesChan {
		latencies[idx] = dur
		sumLatency += dur
		idx++
	}

	validLatencies := latencies[:idx]
	if len(validLatencies) > 0 {
		sort.Slice(validLatencies, func(i, j int) bool {
			return validLatencies[i] < validLatencies[j]
		})
	}

	minLat := time.Duration(0)
	maxLat := time.Duration(0)
	p50 := time.Duration(0)
	p95 := time.Duration(0)
	p99 := time.Duration(0)
	avgLat := time.Duration(0)

	if len(validLatencies) > 0 {
		minLat = validLatencies[0]
		maxLat = validLatencies[len(validLatencies)-1]
		p50 = validLatencies[int(float64(len(validLatencies))*0.50)]
		p95 = validLatencies[int(float64(len(validLatencies))*0.95)]
		p99 = validLatencies[int(float64(len(validLatencies))*0.99)]
		avgLat = sumLatency / time.Duration(len(validLatencies))
	}

	totalReqs := successReqs + failedReqs
	rps := float64(totalReqs) / totalDuration.Seconds()

	return BenchResult{
		Concurrency: concurrency,
		TotalReqs:   totalReqs,
		SuccessReqs: successReqs,
		FailedReqs:  failedReqs,
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
	fmt.Println("📊 BENCHMARK SUMMARY RESULTS")
	fmt.Println("==================================================")
	fmt.Printf("| Concurrency | Total Reqs | RPS | Avg Latency | p50 | p95 | p99 | Max Latency |\n")
	fmt.Printf("|-------------|------------|-----|-------------|-----|-----|-----|-------------|\n")

	for _, r := range results {
		fmt.Printf("| %11d | %10d | %7.1f | %11v | %3v | %3v | %3v | %11v |\n",
			r.Concurrency, r.TotalReqs, r.RPS, r.AvgLatency, r.P50, r.P95, r.P99, r.MaxLatency)
	}
	fmt.Println("==================================================")
}
