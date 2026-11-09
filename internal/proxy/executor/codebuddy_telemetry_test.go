package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// codebuddyLifecycleCopy snapshots the lifecycle-dedup map under its lock, so
// tests can restore the pre-test state without racing the telemetry goroutines.
func codebuddyLifecycleCopy() map[string]bool {
	codebuddyLifecycleMu.Lock()
	defer codebuddyLifecycleMu.Unlock()
	cp := make(map[string]bool, len(codebuddyLifecycleSent))
	for k, v := range codebuddyLifecycleSent {
		cp[k] = v
	}
	return cp
}

// TestCodebuddyTelemetryOnlyForCN asserts the telemetry wrapper only fires for
// the codebuddy-cn provider — the intl lane (WorkBuddy) must pass straight
// through with no telemetry side effects.
func TestCodebuddyTelemetryOnlyForCN(t *testing.T) {
	var mu sync.Mutex
	var reports []string
	report := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reports = append(reports, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer report.Close()
	origReport := codebuddyReportURL
	origLifecycle := codebuddyLifecycleCopy()
	codebuddyLifecycleMu.Lock()
	codebuddyLifecycleSent = make(map[string]bool)
	codebuddyLifecycleMu.Unlock()
	defer func() {
		codebuddyReportURL = origReport
		codebuddyLifecycleMu.Lock()
		codebuddyLifecycleSent = origLifecycle
		codebuddyLifecycleMu.Unlock()
	}()
	codebuddyReportURL = report.URL + "/v2/report"

	// codebuddy-cn with a token.
	var gotHTTP bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHTTP = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	req := &Request{
		Provider: "codebuddy-cn",
		APIKey:   "sk-cn-test",
		Body:     []byte(`{"model":"cmc/x","messages":[{"role":"user","content":"hi"}]}`),
		Config:   &providers.ProviderConfig{BaseURL: upstream.URL},
		Client:   upstream.Client(),
		Ctx:      context.Background(),
	}
	rec := httptest.NewRecorder()
	if err := ForwardCodebuddyCNWithTelemetry(rec, req); err != nil {
		t.Fatalf("forward: %v", err)
	}
	if !gotHTTP {
		t.Fatal("upstream was never called")
	}
	// Telemetry is fire-and-forget: give the goroutine a short moment.
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	reportCount := len(reports)
	firstPath := ""
	if reportCount > 0 {
		firstPath = reports[0]
	}
	mu.Unlock()
	if reportCount == 0 {
		t.Fatalf("expected codebuddy-cn to emit telemetry, saw none")
	}
	if firstPath != "/v2/report" {
		t.Fatalf("first report path = %q, want /v2/report", firstPath)
	}

	// intl passes through with nothing.
	mu.Lock()
	before := len(reports)
	mu.Unlock()
	req2 := &Request{
		Provider: "codebuddy-intl",
		APIKey:   "sk-intl-test",
		Body:     []byte(`{"model":"x","messages":[]}`),
		Config:   &providers.ProviderConfig{BaseURL: upstream.URL},
		Client:   upstream.Client(),
		Ctx:      context.Background(),
	}
	rec2 := httptest.NewRecorder()
	if err := ForwardCodebuddyCNWithTelemetry(rec2, req2); err != nil {
		t.Fatalf("intl forward: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	after := len(reports)
	mu.Unlock()
	if after != before {
		t.Fatalf("codebuddy-intl emitted telemetry unexpectedly: %d → %d reports", before, after)
	}
}

// TestCodebuddyTelemetryFailsOpenWithoutToken asserts that a credential with no
// resolvable token never panics and never blocks the chat call.
func TestCodebuddyTelemetryFailsOpenWithoutToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	req := &Request{
		Provider: "codebuddy-cn", // empty APIKey
		Body:     []byte(`{"model":"x","messages":[]}`),
		Config:   &providers.ProviderConfig{BaseURL: upstream.URL},
		Client:   upstream.Client(),
		Ctx:      context.Background(),
	}
	rec := httptest.NewRecorder()
	start := time.Now()
	if err := ForwardCodebuddyCNWithTelemetry(rec, req); err != nil {
		t.Fatalf("forward without token: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("telemetry blocked the chat path: %v", time.Since(start))
	}
}

// TestCodebuddyHeadersCNVersion asserts the CN lane sends the current CLI build
// and the intl lane keeps cur.
func TestCodebuddyHeadersCNVersion(t *testing.T) {
	h := codebuddyHeaders(&Request{Provider: "codebuddy-cn"}, nil, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if !strings.Contains(h["User-Agent"], codebuddyCLIVersion) {
		t.Errorf("CN User-Agent = %q, want %s", h["User-Agent"], codebuddyCLIVersion)
	}
	hi := codebuddyHeaders(&Request{Provider: "codebuddy-intl"}, nil, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if strings.Contains(hi["User-Agent"], codebuddyCLIVersion) {
		t.Errorf("intl User-Agent = %q, must NOT carry CN CLI %s", hi["User-Agent"], codebuddyCLIVersion)
	}
}
