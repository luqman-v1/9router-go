package chat

import (
	"testing"
	"time"
)

// retryAfterBody encodes an absolute retry instant in the shape an upstream
// sends it, and is the single place the chat tests build that body.
//
// The instant is written with sub-second precision on purpose. RFC3339 has
// second precision, so formatting an instant to it truncates: an upstream
// asking for a wait of one second can have that wait cut down to nothing
// when the request lands late in a second, and the router then reads the
// instant as already elapsed and concludes the upstream asked for no wait at
// all. The wait a test asks for has to be the wait the router reads back,
// otherwise the test measures the clock rather than the behaviour.
func retryAfterBody(base time.Time, wait time.Duration) string {
	return `{"error":{"message":"rate limited","retryAfter":"` +
		base.Add(wait).UTC().Format(time.RFC3339Nano) + `"}}`
}

// TestRetryAfterBodyKeepsTheWaitItAsksFor pins the property every test that
// hands the router a retry instant depends on: the wait it encodes is the
// wait the router reads back.
//
// The base is pinned late in a second on purpose. That is where a
// second-precision encoding loses the most — almost the whole requested wait
// — so the assertion fails there and nowhere else, instead of depending on
// where in a second the test happens to start.
func TestRetryAfterBodyKeepsTheWaitItAsksFor(t *testing.T) {
	const want = time.Second
	base := time.Now().Truncate(time.Second).Add(999 * time.Millisecond)

	encoded := retryAfterBody(base, want)
	instant, err := time.Parse(time.RFC3339, extractRetryAfter([]byte(encoded)))
	if err != nil {
		t.Fatalf("the router cannot read the instant %q: %v", encoded, err)
	}
	if got := instant.Sub(base); got < want-time.Second/20 {
		t.Errorf("the encoded wait lost its sub-second part: got %v, want %v", got, want)
	}

	// And the router's own reader finds a usable wait in it.
	ue := &upstreamError{StatusCode: 429, Body: []byte(encoded)}
	got, ok := retryAfterWait(ue)
	if !ok {
		t.Fatalf("the router found no usable wait in %q", encoded)
	}
	if got <= 0 {
		t.Errorf("the router read the wait as already elapsed: %v", got)
	}
}
