package audit

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitStat polls pred until it is true or the deadline passes.
func waitStat(t *testing.T, timeout, poll time.Duration, pred func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(poll)
	}
	return pred()
}

func TestResolveOTLPConfig(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	if _, ok, err := ResolveOTLPConfig("1.0", "ins_1", getenv); err != nil || ok {
		t.Fatalf("no endpoint anywhere: ok=%v err=%v, want ok=false", ok, err)
	}

	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://collector:4318/"
	env["OTEL_EXPORTER_OTLP_HEADERS"] = "x-api-key=abc%3D,tenant=acme"
	env["OTEL_RESOURCE_ATTRIBUTES"] = "deployment.environment=prod,service.name=fromenv"
	cfg, ok, err := ResolveOTLPConfig("1.0", "ins_1", getenv)
	if err != nil || !ok {
		t.Fatalf("configured endpoint: ok=%v err=%v", ok, err)
	}
	if got := cfg.URL; got != "http://collector:4318/v1/logs" {
		t.Errorf("URL = %q, want http://collector:4318/v1/logs", got)
	}
	if got := cfg.Headers["x-api-key"]; got != "abc=" {
		t.Errorf("header x-api-key = %q, want abc= (url-decoded)", got)
	}
	if got := cfg.Resource["service.name"]; got != "fromenv" {
		t.Errorf("service.name = %q, want fromenv", got)
	}
	if got := cfg.Resource["deployment.environment"]; got != "prod" {
		t.Errorf("deployment.environment = %q, want prod", got)
	}
	if got := cfg.Resource["service.instance.id"]; got != "ins_1" {
		t.Errorf("service.instance.id = %q, want ins_1", got)
	}
	if got := cfg.Resource["service.version"]; got != "1.0" {
		t.Errorf("service.version = %q, want 1.0", got)
	}

	// The signal-specific endpoint is used as-is, per the OTel spec.
	env["OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"] = "https://logs.example.com/custom/path"
	cfg, _, err = ResolveOTLPConfig("", "", getenv)
	if err != nil {
		t.Fatalf("signal-specific endpoint: %v", err)
	}
	if got := cfg.URL; got != "https://logs.example.com/custom/path" {
		t.Errorf("URL = %q, want the signal-specific endpoint as-is", got)
	}

	// A gRPC-only protocol cannot work and must be reported, not silently used.
	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "grpc"
	if _, ok, err = ResolveOTLPConfig("", "", getenv); err == nil || ok {
		t.Fatalf("grpc protocol: ok=%v err=%v, want an error", ok, err)
	}

	// Default service name when nothing supplies one.
	delete(env, "OTEL_EXPORTER_OTLP_PROTOCOL")
	env["OTEL_SERVICE_NAME"] = "kr-prod"
	cfg, ok, err = ResolveOTLPConfig("", "", getenv)
	if err != nil || !ok {
		t.Fatalf("service name env: ok=%v err=%v", ok, err)
	}
	if got := cfg.Resource["service.name"]; got != "kr-prod" {
		t.Errorf("service.name = %q, want kr-prod", got)
	}
}

func TestOTLPExporterSendsSpecShapedJSON(t *testing.T) {
	var (
		mu       sync.Mutex
		bodies   []map[string]any
		calls    atomic.Int32
		seenAuth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First call fails transiently to exercise the retry.
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/v1/logs" {
			t.Errorf("path = %q, want /v1/logs", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		if ce := r.Header.Get("Content-Encoding"); ce != "gzip" {
			t.Errorf("content-encoding = %q, want gzip", ce)
		}
		mu.Lock()
		seenAuth = r.Header.Get("Authorization")
		mu.Unlock()
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("gzip body: %v", err)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(zr).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	exp := NewOTLPExporter(OTLPConfig{
		URL: srv.URL + "/v1/logs", Gzip: true, MaxRetries: 2, FlushInterval: 10 * time.Millisecond,
		Headers:  map[string]string{"Authorization": "Bearer t"},
		Resource: map[string]string{"service.name": "kr"},
	})
	var slept []time.Duration
	var sleepMu sync.Mutex
	exp.sleep = func(_ context.Context, d time.Duration) bool {
		sleepMu.Lock()
		slept = append(slept, d)
		sleepMu.Unlock()
		return true
	}
	exp.Enqueue([]Entry{{
		ID: "au_1", TenantID: "default", Actor: "session:ab", ActorType: ActorDashboard,
		Action: "keys.delete", Target: "key_9", ResourceType: "keys", Outcome: OutcomeSuccess,
		Severity: SeverityInfo, ClientIP: "10.0.0.1", HTTPMethod: "DELETE", HTTPRoute: "/api/keys/{id}",
		StatusCode: 200, TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7",
		Detail: `{"fields":"name"}`, ChainID: "ins_1", ChainSeq: 7, Hash: "h",
		CreatedAt: time.Unix(1700000000, 123000).UTC(),
	}})
	if !waitStat(t, 2*time.Second, 5*time.Millisecond, func() bool { return exp.Stats().Exported == 1 }) {
		t.Fatalf("exported = %d, want 1", exp.Stats().Exported)
	}
	exp.Stop(time.Second)

	sleepMu.Lock()
	if len(slept) != 1 || slept[0] != time.Second {
		t.Errorf("backoff = %v, want [1s] (Retry-After honoured)", slept)
	}
	sleepMu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	if seenAuth != "Bearer t" {
		t.Errorf("authorization = %q, want Bearer t", seenAuth)
	}
	if len(bodies) != 1 {
		t.Fatalf("bodies = %d, want 1", len(bodies))
	}
	rlAny, _ := bodies[0]["resourceLogs"].([]any)
	if len(rlAny) != 1 {
		t.Fatalf("resourceLogs = %d, want 1", len(rlAny))
	}
	rl := rlAny[0].(map[string]any)
	resAttrs := rl["resource"].(map[string]any)["attributes"].([]any)
	if got := resAttrs[0].(map[string]any)["key"]; got != "service.name" {
		t.Errorf("resource attr[0].key = %v, want service.name", got)
	}
	slAny := rl["scopeLogs"].([]any)
	sl := slAny[0].(map[string]any)
	if got := sl["scope"].(map[string]any)["name"]; got != scopeName {
		t.Errorf("scope.name = %v, want %s", got, scopeName)
	}
	lrAny := sl["logRecords"].([]any)
	lr := lrAny[0].(map[string]any)
	if got := lr["timeUnixNano"]; got != "1700000000000123000" {
		t.Errorf("timeUnixNano = %v, want 1700000000000123000 (decimal string)", got)
	}
	if got := lr["severityNumber"]; got != float64(9) {
		t.Errorf("severityNumber = %v, want 9 (INFO)", got)
	}
	if got := lr["eventName"]; got != EventNamePrefix+"keys.delete" {
		t.Errorf("eventName = %v, want %s", got, EventNamePrefix+"keys.delete")
	}
	if got := lr["traceId"]; got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("traceId = %v", got)
	}
	if got := lr["spanId"]; got != "00f067aa0ba902b7" {
		t.Errorf("spanId = %v", got)
	}
	if got := lr["body"].(map[string]any)["stringValue"]; got != "dashboard session:ab keys.delete key_9 -> success" {
		t.Errorf("body = %v", got)
	}
	attrs := map[string]map[string]any{}
	for _, a := range lr["attributes"].([]any) {
		kv := a.(map[string]any)
		attrs[kv["key"].(string)] = kv["value"].(map[string]any)
	}
	checks := map[string]string{
		"user.id":                       "session:ab",
		"client.address":                "10.0.0.1",
		"http.route":                    "/api/keys/{id}",
		"http.response.status_code":     "200",
		"keirouter.audit.chain.seq":     "7",
		"keirouter.audit.detail.fields": "name",
	}
	for k, want := range checks {
		v, ok := attrs[k]
		if !ok {
			t.Errorf("attribute %s missing", k)
			continue
		}
		got, _ := v["stringValue"].(string)
		if got == "" {
			got, _ = v["intValue"].(string)
		}
		if got != want {
			t.Errorf("attribute %s = %v, want %s", k, got, want)
		}
	}
}

func TestOTLPExporterDropsOnPermanentError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	exp := NewOTLPExporter(OTLPConfig{URL: srv.URL, MaxRetries: 5, FlushInterval: 5 * time.Millisecond})
	exp.Enqueue([]Entry{{ID: "a", Action: "x", CreatedAt: time.Now()}})
	if !waitStat(t, 2*time.Second, 5*time.Millisecond, func() bool { return exp.Stats().Failed == 1 }) {
		t.Fatalf("failed = %d, want 1", exp.Stats().Failed)
	}
	exp.Stop(time.Second)
	if got := calls.Load(); got != 1 {
		t.Errorf("collector calls = %d, want 1 (4xx is not retried)", got)
	}
	if got := exp.Stats().LastError; got == "" {
		t.Error("last_error empty after a permanent failure")
	}
}

func TestRecorderExportsOnlyCommittedRows(t *testing.T) {
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		var data otlpLogsData
		if json.Unmarshal(body, &data) == nil {
			got.Add(int32(len(data.ResourceLogs[0].ScopeLogs[0].LogRecords)))
		}
	}))
	defer srv.Close()

	ms := &fakeStore{}
	exp := NewOTLPExporter(OTLPConfig{URL: srv.URL, FlushInterval: 5 * time.Millisecond})
	rec := New(ms, exp, Config{FlushSize: 1})
	defer rec.Stop(2 * time.Second)

	ms.fail = true
	rec.Record(Event{Action: "lost"})
	rec.Flush(context.Background())
	ms.fail = false
	rec.Record(Event{Action: "kept"})
	rec.Flush(context.Background())
	rec.Stop(2 * time.Second)

	if got := got.Load(); got != 1 {
		t.Errorf("exported = %d, want 1 (only the committed row)", got)
	}
}
