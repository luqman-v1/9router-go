package tracing

import "testing"

func TestPercentile(t *testing.T) {
	if got := percentile(nil, 95); got != 0 {
		t.Errorf("empty: got %v, want 0", got)
	}
	durs := []int64{100, 200, 300, 400, 500}
	if got := percentile(durs, 50); got != 300 {
		t.Errorf("p50: got %v, want 300", got)
	}
	if got := percentile(durs, 95); got != 500 {
		t.Errorf("p95: got %v, want 500", got)
	}
}

func TestRecordAndLatencyByKey(t *testing.T) {
	Record(Span{Provider: "openai", Model: "gpt-4o", Status: "200", DurationMs: 100})
	Record(Span{Provider: "openai", Model: "gpt-4o", Status: "200", DurationMs: 200})
	Record(Span{Provider: "openai", Model: "gpt-4o", Status: "429", DurationMs: 50})
	Record(Span{Provider: "deepseek", Model: "v3", Status: "200", DurationMs: 900})

	stats := LatencyByKey()
	if len(stats) != 2 {
		t.Fatalf("expected 2 keys, got %d: %+v", len(stats), stats)
	}
	var oai, ds *LatencyStats
	for i := range stats {
		switch stats[i].Key {
		case "openai/gpt-4o":
			oai = &stats[i]
		case "deepseek/v3":
			ds = &stats[i]
		}
	}
	if oai == nil || ds == nil {
		t.Fatalf("missing keys: %+v", stats)
	}
	if oai.Count != 3 || oai.Errors != 1 {
		t.Errorf("openai count/errors: got %d/%d", oai.Count, oai.Errors)
	}
	if oai.P50Ms != 100 {
		t.Errorf("openai p50: got %v, want 100", oai.P50Ms)
	}
	if ds.Count != 1 || ds.P50Ms != 900 {
		t.Errorf("deepseek: got %+v", ds)
	}
}
