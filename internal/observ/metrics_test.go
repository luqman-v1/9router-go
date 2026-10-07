package observ

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// plaintextKey is the shape of a real gateway credential. It appears here only
// to prove it never reaches a label.
const plaintextKey = "sk-ant-api03-REALLOOKINGSECRET0000000000000000"

func gather(t *testing.T, m *Metrics) []*dto.MetricFamily {
	t.Helper()
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	return families
}

func familyValue(t *testing.T, m *Metrics, name string) float64 {
	t.Helper()
	for _, f := range gather(t, m) {
		if f.GetName() != name {
			continue
		}
		var total float64
		for _, metric := range f.GetMetric() {
			switch {
			case metric.Counter != nil:
				total += metric.Counter.GetValue()
			case metric.Histogram != nil:
				total += float64(metric.Histogram.GetSampleCount())
			}
		}
		return total
	}
	return 0
}

func familyLabels(t *testing.T, m *Metrics, name string) map[string]string {
	t.Helper()
	for _, f := range gather(t, m) {
		if f.GetName() != name {
			continue
		}
		out := map[string]string{}
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				out[l.GetName()] = l.GetValue()
			}
		}
		return out
	}
	return nil
}

func counterOf(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	c, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", labels, err)
	}
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

func TestRecordRequestLabels(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		endpoint string
		status   int
		want     map[string]string
	}{
		{
			name: "full set", provider: "claude", model: "claude-opus-4",
			endpoint: "/v1/messages", status: 200,
			want: map[string]string{
				"provider": "claude", "model": "claude-opus-4",
				"endpoint": "/v1/messages", "status": "200",
			},
		},
		{
			name: "empty values become unknown", provider: "", model: "", endpoint: "", status: 0,
			want: map[string]string{
				"provider": "unknown", "model": "unknown",
				"endpoint": "unknown", "status": "unknown",
			},
		},
		{
			name: "error status", provider: "openai", model: "gpt-5",
			endpoint: "/v1/chat/completions", status: 502,
			want: map[string]string{
				"provider": "openai", "model": "gpt-5",
				"endpoint": "/v1/chat/completions", "status": "502",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			m.RecordRequest(tc.provider, tc.model, tc.endpoint, tc.status)

			if got := familyValue(t, m, "router_requests_total"); got != 1 {
				t.Errorf("requests_total = %v, want 1", got)
			}
			for k, want := range tc.want {
				if got := familyLabels(t, m, "router_requests_total")[k]; got != want {
					t.Errorf("label %s = %q, want %q", k, got, want)
				}
			}
		})
	}
}

func TestRecordUsage(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		model         string
		prompt        int
		completion    int
		costMicros    float64
		ttftMillis    int64
		wantTokens    float64
		wantCost      float64
		wantTTFTSample float64
	}{
		{
			name: "non streaming turn records no ttft", provider: "claude", model: "opus",
			prompt: 120, completion: 40, costMicros: 1500, ttftMillis: 0,
			wantTokens: 160, wantCost: 1500, wantTTFTSample: 0,
		},
		{
			name: "streaming turn records ttft", provider: "claude", model: "opus",
			prompt: 120, completion: 40, costMicros: 1500, ttftMillis: 850,
			wantTokens: 160, wantCost: 1500, wantTTFTSample: 1,
		},
		{
			name: "zero tokens are dropped", provider: "openai", model: "gpt-5",
			prompt: 0, completion: 0, costMicros: 0, ttftMillis: 0,
			wantTokens: 0, wantCost: 0, wantTTFTSample: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			m.RecordUsage(tc.provider, tc.model, "/v1/messages", 200,
				2*time.Second, tc.prompt, tc.completion, tc.costMicros, tc.ttftMillis)

			if got := familyValue(t, m, "router_tokens_total"); got != tc.wantTokens {
				t.Errorf("tokens_total = %v, want %v", got, tc.wantTokens)
			}
			if got := familyValue(t, m, "router_cost_micros_total"); got != tc.wantCost {
				t.Errorf("cost_micros_total = %v, want %v", got, tc.wantCost)
			}
			if got := familyValue(t, m, "router_time_to_first_token_seconds"); got != tc.wantTTFTSample {
				t.Errorf("ttft sample count = %v, want %v", got, tc.wantTTFTSample)
			}
			if got := familyValue(t, m, "router_request_duration_seconds"); got != 1 {
				t.Errorf("duration sample count = %v, want 1", got)
			}
		})
	}
}

func TestTokenKindsAreSeparateSeries(t *testing.T) {
	m := New()
	m.AddTokens("claude", "opus", TokenKindPrompt, 10)
	m.AddTokens("claude", "opus", TokenKindCompletion, 5)

	if got := counterOf(t, m.TokensTotal, "claude", "opus", TokenKindPrompt); got != 10 {
		t.Errorf("prompt = %v, want 10", got)
	}
	if got := counterOf(t, m.TokensTotal, "claude", "opus", TokenKindCompletion); got != 5 {
		t.Errorf("completion = %v, want 5", got)
	}
}

func TestRegistryIsolation(t *testing.T) {
	a, b := New(), New()
	a.RecordRequest("claude", "opus", "/v1/messages", 200)

	if got := familyValue(t, b, "router_requests_total"); got != 0 {
		t.Errorf("second instance saw %v requests, want 0 — registries are shared", got)
	}
	if got := familyValue(t, a, "router_requests_total"); got != 1 {
		t.Errorf("first instance saw %v requests, want 1", got)
	}
}

func TestNoPlaintextKeyInAnyLabel(t *testing.T) {
	m := New()
	// Every entry point that could plausibly be handed a credential.
	m.RecordRequest("claude", "opus", "/v1/messages", 200)
	m.RecordRequest(plaintextKey, plaintextKey, plaintextKey, 200)
	m.AddTokens(plaintextKey, "opus", TokenKindPrompt, 1)
	m.AddCostMicros(plaintextKey, "opus", 1)
	m.RecordTTFT(plaintextKey, "opus", 10)
	m.IncFallback(plaintextKey, "opus", plaintextKey)
	m.IncUpstreamError(plaintextKey, "opus", 500)
	m.IncRateLimitReject("api_key", plaintextKey)

	rec := httptest.NewRecorder()
	HandlerFor(m).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(string(body), plaintextKey) {
		t.Fatal("plaintext API key appeared in the exposition output")
	}

	// The rate-limit counter keeps per-key visibility through a derived id.
	if labels := familyLabels(t, m, "router_rate_limit_rejects_total"); labels["key"] == plaintextKey {
		t.Fatal("rate limit reject stored the raw key as a label")
	} else if labels["key"] != KeyRef(plaintextKey) {
		t.Errorf("rate limit key label = %q, want KeyRef", labels["key"])
	}
}

func TestKeyRefIsStableAndBounded(t *testing.T) {
	a, b := KeyRef("key-1"), KeyRef("key-1")
	if a != b {
		t.Errorf("KeyRef is not stable: %q vs %q", a, b)
	}
	if len(a) > 16 {
		t.Errorf("KeyRef length = %d, want <= 16", len(a))
	}
	if a == KeyRef("key-2") {
		t.Error("distinct keys produced the same KeyRef")
	}
	if got := KeyRef(""); got != unknownLabel {
		t.Errorf("KeyRef(\"\") = %q, want %q", got, unknownLabel)
	}
}

func TestLabelBounds(t *testing.T) {
	long := strings.Repeat("m", 500)
	if got := Label(long); len(got) != maxLabelLen {
		t.Errorf("Label length = %d, want %d", len(got), maxLabelLen)
	}
	if got := Label("   "); got != unknownLabel {
		t.Errorf("Label(blank) = %q, want %q", got, unknownLabel)
	}
}

func TestDefaultIsStableAndShared(t *testing.T) {
	if Default() != Default() {
		t.Error("Default() returned two different instances")
	}
	// The package-level helpers must reach the same instance the handler serves.
	Default().RecordRequest("claude", "unique-model-marker", "/v1/messages", 200)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "unique-model-marker") {
		t.Fatal("Handler() did not serve the default instance's registry")
	}
}