package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"9router/proxy/internal/proxy"
)

// The OpenCode free-tier gate occasionally refuses a request whose fingerprint
// is correct, and the same request succeeds when repeated. That makes one
// retry worth paying for: it recovers the blip without the latency cost of a
// longer cycle.
//
// Deliberately one retry, not a loop. While the gate is refusing on identity
// (see #367, where every client variant was refused) no number of attempts
// succeeds, so each extra attempt only delays the router's advance to a
// fallback connection and the caller's actionable status. A single repeat
// costs at most openCodeRetryDelay and still catches the transient case.
const (
	// openCodeRetryDelay is the wait before the single retry. Short enough that
	// someone waiting on a completion does not notice it, long enough that the
	// repeat is not issued into the same instant as the refusal.
	openCodeRetryDelay = 200 * time.Millisecond

	// openCodeRefusalToken is the error type the gate puts in the body. Matched
	// as a token rather than the prose, because the message has already been
	// reworded once ("...Download it at https://opencode.ai and keep building.").
	openCodeRefusalToken = "FreeTierError"
)

// isOpenCodeRefusal reports whether a failure is the gate refusing this
// request, as opposed to a genuine account, quota or auth error. Only this
// refusal is retried: a 403 meaning "insufficient credit" fails identically
// forever, and repeating it would burn quota and delay the error the caller
// needs to see.
func isOpenCodeRefusal(ue *proxy.UpstreamError) bool {
	if ue == nil || ue.StatusCode != http.StatusForbidden {
		return false
	}
	return bytes.Contains(ue.Body, []byte(openCodeRefusalToken))
}

// forwardOpenCode issues the request and returns the settled result: the
// response to stream from, or the error to report. It performs the single
// retry described above and nothing more.
//
// The repeat goes out over sendDirect, a DIRECT egress, not over the egress
// that just failed: the refusal is the gate declining this address, so a replay
// through the same proxy repeats the refusal.
//
// A non-200 arrives as an *proxy.UpstreamError in the error position — the
// request layer converts the status before returning, so the response is nil
// there — which is why the classifier reads the error rather than the response.
// Any other error is a transport failure; the account-failover path owns that,
// and it is returned untouched.
func forwardOpenCode(ctx context.Context, send, sendDirect func() (*http.Response, error)) (*http.Response, error) {
	resp, err := send()
	if err == nil {
		return resp, nil
	}

	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) || !isOpenCodeRefusal(ue) {
		return nil, err
	}
	// The caller forbade routing around its proxy; report the refusal instead.
	if proxy.StrictProxy(ctx) {
		return nil, err
	}

	select {
	case <-ctx.Done():
		// The caller went away; report the refusal rather than sleeping for a
		// retry nobody is waiting for.
		return nil, ue
	case <-time.After(openCodeRetryDelay):
	}

	directResp, directErr := sendDirect()
	if directErr != nil {
		// The replay failed too. The gate's refusal is the actionable answer, so
		// report it rather than the replay's follow-on error.
		return nil, ue
	}
	return directResp, nil
}
