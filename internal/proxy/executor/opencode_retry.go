package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/log"
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

// openCodeUsageLimitToken is the error type the gate answers with when the
// budget accounted to the upstream session is spent. Matched as a token for
// the same reason as the refusal above: the prose is not stable.
const openCodeUsageLimitToken = "FreeUsageLimitError"

// isOpencodeUsageLimit reports whether the gate answered 429 because the
// budget of the upstream session the request traveled on is spent — as opposed
// to a per-egress or per-key rate limit, which no session change cures.
func isOpencodeUsageLimit(ue *proxy.UpstreamError) bool {
	return ue != nil && ue.StatusCode == http.StatusTooManyRequests &&
		bytes.Contains(ue.Body, []byte(openCodeUsageLimitToken))
}

// refusedResponseToError moves a gate answer (403/429) from the response
// position into the error position. The stealth forward and ForwardOpenAI both
// return a non-200 as a plain response, while the refusal classifiers read the
// error position — without this lift a 403 FreeTierError or a 429
// FreeUsageLimitError would sail past every retry untouched. The body is read
// here (capped) because the token lives in it; the response is consumed.
func refusedResponseToError(resp *http.Response) *proxy.UpstreamError {
	if resp == nil || (resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests) {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, constants.UpstreamErrLimit))
	_ = resp.Body.Close()
	return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body}
}

// forwardOpenCode issues the request and returns the settled result: the
// response to stream from, or the error to report. It performs the single
// retry described above and nothing more.
//
// The repeat goes out over sendDirect, a DIRECT egress, not over the egress
// that just failed: the refusal is the gate declining this address, so a replay
// through the same proxy repeats the refusal.
//
// A non-200 can arrive either as an *proxy.UpstreamError in the error position
// (the request layer converts) or as a plain response (the stealth forward and
// ForwardOpenAI do not convert); refusedResponseToError lifts the second shape
// so the classifier sees both. Any other error is a transport failure; the
// account-failover path owns that, and it is returned untouched.
func forwardOpenCode(ctx context.Context, send, sendDirect func() (*http.Response, error)) (*http.Response, error) {
	resp, err := send()
	if err == nil {
		if ue := refusedResponseToError(resp); ue != nil {
			// The response is consumed; the refusal now lives in err.
			err = ue
		} else {
			return resp, nil
		}
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
	if replayUe := refusedResponseToError(directResp); replayUe != nil {
		// The replay was refused as well; the original refusal is the answer.
		return nil, ue
	}
	return directResp, nil
}

// forwardOpenCodeRotating extends the free-lane forward with one session
// rotation. The free tier accounts its quota per upstream session, and the
// official CLI recovers a spent session the same way it starts every
// conversation: with a freshly minted session id and a fresh budget. When the
// gate answers 429 FreeUsageLimitError on a router-minted stable session, the
// same recovery applies — rotate() installs a new session on the request and
// the replay goes out once on it. A client-supplied session travels as rotate
// == nil: it identifies the caller's own conversation upstream and is never
// swapped behind its back.
//
// Deliberately one rotation, like the refusal retry above: if the fresh
// session also answers 429, the limit is not the session's (an egress-wide
// budget or a genuine rate limit) and the caller's fallback machinery owns
// the advance.
func forwardOpenCodeRotating(ctx context.Context, send, sendDirect func() (*http.Response, error), rotate func()) (*http.Response, error) {
	resp, err := forwardOpenCode(ctx, send, sendDirect)
	if err == nil || rotate == nil {
		return resp, err
	}
	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) || !isOpencodeUsageLimit(ue) {
		return resp, err
	}
	log.Warn("opencode", "free session budget spent, rotating session and replaying once")
	rotate()
	return forwardOpenCode(ctx, send, sendDirect)
}
