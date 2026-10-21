package thinking

import (
	"strings"
	"testing"
	"time"
)

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

	start := time.Now()
	got := bodyModelHasSuffix(body)
	elapsed := time.Since(start)

	if got {
		t.Fatal("a body without a model suffix must not report one")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the scan took %s, want well under 2s (a linear scan)", elapsed)
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
