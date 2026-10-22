package thinking

import (
	"strings"
	"testing"
)

// TestBodyMentionsThinkingDoesNotCopyTheBody pins the allocation contract: the
// scan runs on every request, so it must not copy the body it scans. The
// string conversion this replaced allocated the whole body per call.
func TestBodyMentionsThinkingDoesNotCopyTheBody(t *testing.T) {
	body := []byte(strings.Repeat(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],`+"\n", 2000))
	allocs := testing.AllocsPerRun(20, func() {
		if BodyMentionsThinking(body) {
			t.Fatal("unexpected hit")
		}
	})
	if allocs > 0 {
		t.Errorf("BodyMentionsThinking allocated %.1f objects per call, want 0", allocs)
	}
}

// BenchmarkBodyMentionsThinking measures the per-request scan on a body large
// enough for the cost to show. The scan is linear in the body size and runs
// on every request, so the number belongs in the changelog.
func BenchmarkBodyMentionsThinking(b *testing.B) {
	body := []byte(strings.Repeat(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],`+"\n", 8000))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if BodyMentionsThinking(body) {
			b.Fatal("unexpected hit")
		}
	}
}

// TestBodyMentionsThinkingIgnoresContent pins that the scan looks for a
// thinking KEY, not for the word anywhere in the body. A user message that
// talks about reasoning is not a thinking field, and treating it as one
// defeats the byte-identical fast path for ordinary traffic.
func TestBodyMentionsThinkingIgnoresContent(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"explain \"reasoning\" please"}]}`)
	if BodyMentionsThinking(body) {
		t.Fatalf("a thinking word in the content is not a thinking field: %s", body)
	}
	if BodyMentionsThinking([]byte(`{"model":"m","reasoning_effort":"high"}`)) == false {
		t.Fatal("a thinking key must be detected")
	}
	if BodyMentionsThinking([]byte(`{"model":"m","thinking":{"type":"enabled"}}`)) == false {
		t.Fatal("a thinking key must be detected")
	}
}

// TestBodyModelHasSuffixStaysFastOnALargeBody guards the model-suffix scan's
// cost. A request body is read without a size cap, so the scan has to stay
// cheap on a body that repeats the model key. The bound is a regression
// guard with a wide margin, not a timing assertion about machine speed.
func TestBodyModelHasSuffixStaysFastOnALargeBody(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := 0; i < 80000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"model":123}`)
	}
	b.WriteString(`]}`)
	body := []byte(b.String())
	if len(body) < 1<<20 {
		t.Fatalf("fixture is %d bytes, want at least 1 MiB", len(body))
	}

	if bodyModelHasSuffix(body) {
		t.Fatal("a body without a model suffix must not report one")
	}
	if allocs := testing.AllocsPerRun(10, func() { bodyModelHasSuffix(body) }); allocs > 0 {
		t.Errorf("the scan allocated %v times, want none: it runs on every request", allocs)
	}
}

// TestBodyModelHasSuffixFindsTheSuffix pins that the rewrite still finds a
// suffixed model value, whatever else the body carries.
func TestBodyModelHasSuffixFindsTheSuffix(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"suffixed model", `{"model":"m(high)"}`, true},
		{"suffixed model with spaces", `{"model" : "m(high)"}`, true},
		{"plain model", `{"model":"m"}`, false},
		{"suffixed modelId", `{"modelId":"m(high)"}`, true},
		{"suffixed model_id", `{"model_id":"m(high)"}`, true},
		{"suffix in another value", `{"note":"see (docs)"}`, false},
		{"model key in a value", `{"x":"model","model":"m"}`, false},
		{"unterminated value", `{"model":"m(high`, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := bodyModelHasSuffix([]byte(tt.body)); got != tt.want {
				t.Fatalf("bodyModelHasSuffix(%s) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
