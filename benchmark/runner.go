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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/proxy"
)

type Config struct {
	Target      string
	APIKey      string
	Concurrency []int
	Requests    int
	Stream      bool
	Timeout     time.Duration
	// Payload is the request body sent verbatim when set. It overrides the
	// built-in payload, so a matrix can drive one runner through several
	// request shapes without recompiling it.
	Payload []byte
	// Model is the model field of the built-in payload.
	Model string
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
	// TTFT is the first semantic event, measured only on successful stream
	// responses. TTFTSamples says how many were seen, so a level that produced
	// no semantic event cannot report a TTFT of zero as if it were fast.
	TTFTAvg     time.Duration
	TTFTP50     time.Duration
	TTFTP95     time.Duration
	TTFTMax     time.Duration
	TTFTSamples uint64
}

type sample struct {
	HeaderLatency time.Duration
	FirstBodyByte time.Duration
	// TTFT is time-to-first-token as the client sees it: the arrival of the
	// first semantic event (text, tool call or reasoning delta), not the first
	// byte on the wire. Zero when the stream carried no such event. The
	// distinction matters — the first byte of a valid SSE stream is usually
	// metadata, a keep-alive or an empty `data:` frame, all of which a client
	// cannot use.
	TTFT   time.Duration
	Total  time.Duration
	Status int
	Body   []byte
}

// eventKind classifies one SSE payload by what a client can act on.
type eventKind int

const (
	evNone eventKind = iota // metadata, keep-alive, role-only, terminal, usage trailer
	evText
	evTool
	evReasoning
	evError // typed error event: the stream is over without an answer
)

// eventProbe reads the fields that carry content across the three wire shapes
// the gateway serves: Chat Completions, Claude Messages and Responses.
type eventProbe struct {
	Type  string          `json:"type"`
	Error json.RawMessage `json:"error"`
	// OpenAI chat
	Choices []struct {
		Text  string `json:"text"` // legacy completions
		Delta struct {
			Content          json.RawMessage `json:"content"`
			ToolCalls        json.RawMessage `json:"tool_calls"`
			FunctionCall     json.RawMessage `json:"function_call"`
			ReasoningContent string          `json:"reasoning_content"`
			Reasoning        string          `json:"reasoning"`
		} `json:"delta"`
	} `json:"choices"`
	// Claude Messages carries an object; Responses carries a plain string under
	// the same key, so it is left raw and decoded per event type.
	Delta        json.RawMessage `json:"delta"`
	ContentBlock struct {
		Type string `json:"type"`
	} `json:"content_block"`
}

// claudeDelta is the object form of `delta`, used by content_block_delta.
type claudeDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Thinking    string `json:"thinking"`
	PartialJSON string `json:"partial_json"`
}

// classifyEvent reports what kind of event a payload is. Non-JSON payloads and
// shapes this prober does not model read as evNone: unknown metadata is not an
// answer, and guessing would timestamp a TTFT the client never received.
func classifyEvent(payload []byte) eventKind {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return evNone
	}
	var ev eventProbe
	if err := json.Unmarshal(trimmed, &ev); err != nil {
		return evNone
	}
	switch ev.Type {
	case "error", "response.failed":
		return evError
	}
	if rawJSONHasValue(ev.Error) {
		return evError
	}

	if len(ev.Choices) > 0 {
		choice := ev.Choices[0]
		if strings.TrimSpace(choice.Text) != "" {
			return evText
		}
		d := choice.Delta
		if rawJSONHasValue(d.ToolCalls) || rawJSONHasValue(d.FunctionCall) {
			return evTool
		}
		if strings.TrimSpace(d.ReasoningContent) != "" || strings.TrimSpace(d.Reasoning) != "" {
			return evReasoning
		}
		if textOfContent(d.Content) != "" {
			return evText
		}
		return evNone
	}

	switch ev.Type {
	case "content_block_delta":
		var d claudeDelta
		if err := json.Unmarshal(ev.Delta, &d); err != nil {
			return evNone
		}
		switch d.Type {
		case "text_delta":
			if d.Text != "" {
				return evText
			}
		case "thinking_delta":
			if d.Thinking != "" {
				return evReasoning
			}
		case "input_json_delta":
			return evTool
		}
	case "content_block_start":
		// A tool_use block carries the tool name and id, so the client can act
		// on it before any argument arrives.
		switch ev.ContentBlock.Type {
		case "tool_use":
			return evTool
		case "thinking":
			return evReasoning
		}
	case "response.output_text.delta":
		return evText
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return evTool
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return evReasoning
	}
	return evNone
}

// rawJSONHasValue reports whether a raw JSON value carries content rather than
// null, an empty string, or an empty array/object.
func rawJSONHasValue(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) ||
		bytes.Equal(trimmed, []byte("[]")) || bytes.Equal(trimmed, []byte("{}")) {
		return false
	}
	return true
}

// textOfContent returns text carried by an OpenAI chat delta content, which is
// a string in the classic shape and an array of parts in the multimodal one.
func textOfContent(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return ""
	}
	for _, part := range parts {
		if strings.TrimSpace(part.Text) != "" {
			return strings.TrimSpace(part.Text)
		}
	}
	return ""
}

// firstByteClock records when the first body byte arrived. It sits in the read
// path so the timestamp is taken while the body is still streaming, not after
// the whole body was read.
type firstByteClock struct {
	started time.Time
	at      time.Duration
	seen    bool
}

func (c *firstByteClock) Write(p []byte) (int, error) {
	if !c.seen && len(p) > 0 {
		c.seen = true
		c.at = time.Since(c.started)
	}
	return len(p), nil
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

	const maxBody = 10 << 20
	var buf bytes.Buffer
	clock := &firstByteClock{started: started}
	limited := io.LimitReader(resp.Body, maxBody+1)
	sink := io.MultiWriter(&buf, clock)

	var readErr error
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		// ScanStream emits each complete event as it arrives, so the semantic
		// event is timestamped when the client could have seen it rather than
		// after io.ReadAll drained the response.
		ttftSet := false
		readErr = proxy.ScanStream(io.TeeReader(limited, sink), func(payload []byte) {
			if ttftSet {
				return
			}
			switch classifyEvent(payload) {
			case evText, evTool, evReasoning:
				got.TTFT = time.Since(started)
				ttftSet = true
			}
		})
	} else {
		_, readErr = io.Copy(sink, limited)
	}
	got.Total = time.Since(started)
	if readErr == nil || readErr == io.EOF {
		readErr = nil
	}
	got.FirstBodyByte = clock.at
	if !clock.seen {
		got.FirstBodyByte = got.Total
	}
	got.Body = buf.Bytes()
	if len(got.Body) > maxBody {
		return got, fmt.Errorf("benchmark response exceeds %d bytes", maxBody)
	}
	return got, readErr
}

// loadPayloadFile reads a JSON request body from disk and checks it is an
// object. A file holding an array or a scalar is rejected: the benchmark sends
// it as an HTTP request body, and a body the gateway cannot parse is a
// measurement of nothing.
func loadPayloadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object: %w", path, err)
	}
	return data, nil
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
	var payloadPath string
	var modelFlag string
	flag.StringVar(&cfg.Target, "target", os.Getenv("BENCH_TARGET"), "gateway chat/completions URL under test")
	flag.StringVar(&cfg.APIKey, "key", os.Getenv("BENCH_KEY"), "API key for the sandbox gateway")
	flag.StringVar(&concurrencyCSV, "concurrency", "1,10,25,50,100", "comma-separated concurrency levels")
	flag.IntVar(&cfg.Requests, "requests", 500, "requests per concurrency level")
	flag.BoolVar(&cfg.Stream, "stream", true, "send stream:true and require a terminal event")
	flag.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "per-request timeout")
	flag.StringVar(&payloadPath, "payload", os.Getenv("BENCH_PAYLOAD"), "path to a JSON request body file; overrides the built-in payload")
	flag.StringVar(&modelFlag, "model", os.Getenv("BENCH_MODEL"), "model field of the built-in payload (default gpt-4o)")
	flag.Parse()

	if cfg.Target == "" || cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "target and key are required: this runner measures an operator-started gateway, it does not start one")
		os.Exit(2)
	}
	cfg.Model = modelFlag
	if cfg.Model == "" {
		cfg.Model = "gpt-4o"
	}
	if payloadPath != "" {
		body, err := loadPayloadFile(payloadPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid payload file: %v\n", err)
			os.Exit(2)
		}
		cfg.Payload = body
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
	payload := cfg.Payload
	if len(payload) == 0 {
		payload, _ = json.Marshal(map[string]any{
			"model":    cfg.Model,
			"stream":   cfg.Stream,
			"messages": []map[string]string{{"role": "user", "content": "Hi"}},
		})
	}

	var successReqs, failedReqs, semanticErr uint64
	latencies := make([]time.Duration, cfg.Requests)
	ttfts := make([]time.Duration, cfg.Requests)

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
					ttfts[idx] = got.TTFT
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

	// TTFT is reported separately from total latency and only over samples
	// that produced a semantic event. A stream that answered with metadata
	// and a terminal event carries no TTFT, and folding a zero into the
	// average would advertise a fast first token that never existed.
	var ttftValid []time.Duration
	var ttftSum time.Duration
	for _, d := range ttfts {
		if d > 0 {
			ttftValid = append(ttftValid, d)
			ttftSum += d
		}
	}
	sort.Slice(ttftValid, func(i, j int) bool { return ttftValid[i] < ttftValid[j] })

	ttftAvg, ttftP50, ttftP95, ttftMax := time.Duration(0), time.Duration(0), time.Duration(0), time.Duration(0)
	if len(ttftValid) > 0 {
		ttftAvg = ttftSum / time.Duration(len(ttftValid))
		ttftP50 = ttftValid[int(float64(len(ttftValid))*0.50)]
		ttftP95 = ttftValid[int(float64(len(ttftValid))*0.95)]
		ttftMax = ttftValid[len(ttftValid)-1]
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
		TTFTAvg:     ttftAvg,
		TTFTP50:     ttftP50,
		TTFTP95:     ttftP95,
		TTFTMax:     ttftMax,
		TTFTSamples: uint64(len(ttftValid)),
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

	// TTFT gets its own table on purpose: it is a different measurement from
	// total latency, and appending it to the table above would invite a
	// side-by-side "speedup" between two numbers that do not mean the same
	// thing. `samples` is printed so a level whose streams carried no semantic
	// event shows its empty TTFT rather than hiding it behind a zero.
	fmt.Println("\nTTFT — first semantic event (text, tool or reasoning delta), not first byte")
	fmt.Printf("| Concurrency | Samples | Avg TTFT | p50 | p95 | Max TTFT |\n")
	fmt.Printf("|-------------|---------|----------|-----|-----|----------|\n")
	for _, r := range results {
		if r.TTFTSamples == 0 {
			fmt.Printf("| %11d | %7d | %8s | %3s | %3s | %8s |\n", r.Concurrency, 0, "n/a", "n/a", "n/a", "n/a")
			continue
		}
		fmt.Printf("| %11d | %7d | %8v | %3v | %3v | %8v |\n",
			r.Concurrency, r.TTFTSamples, r.TTFTAvg, r.TTFTP50, r.TTFTP95, r.TTFTMax)
	}
	fmt.Println("==================================================")

	if failedOverall {
		fmt.Fprintln(os.Stderr, "at least one level recorded zero successes — this is a failed run, not a speedup")
		os.Exit(1)
	}
}
