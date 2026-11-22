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

// openCodeAbusePhrases are the wordings upstream uses when the refusal is a
// network-level flag on this egress rather than a request-shape verdict.
//
// The list is deliberately narrow. The gate answers ONE 403 FreeTierError body
// for at least five unrelated causes — a refused egress, an abuse-flagged
// egress, tool declarations that are not the CLI built-ins, a User-Agent that
// is not the CLI shape, and a missing session header — and ours cannot tell
// them apart beyond this token. Only the abuse flag is made worse by a retry:
// no header, session or body change clears it, so replaying the request only
// extends the penalty. The other four fail identically on a replay anyway, so
// they stay on the single-retry path where a transient case still recovers.
//
// Do NOT widen this to the handler's bare "abuse" match
// (internal/handlers/chat/fallback.go classifyOpencodeFreeTierRefusal). That
// prefix would make any body mentioning "anti-abuse" terminal and discard a
// legitimate recovery. The two layers answer different questions: this one
// asks "will a replay help?", the handler asks "how do I report it?".
var openCodeAbusePhrases = []string{
	"detected abusive traffic",
	"abuse traffic",
	"abuse of traffic",
}

// openCodeNetworkAbusePair is the conjunctive wording the gate uses when the
// refusal is a network-level flag rather than a request-shape verdict. Both
// halves must appear: each occurs alone in benign prose (a model-unavailable
// notice, a docs sentence), so matching either one would make ordinary errors
// terminal and suppress a legitimate replay.
var openCodeNetworkAbusePair = [2]string{
	"free models aren't available",
	"from your current network",
}

// isOpenCodeTerminalRefusal reports whether a gate refusal is the egress-level
// abuse flag, which no client-side change can clear.
func isOpenCodeTerminalRefusal(ue *proxy.UpstreamError) bool {
	if !isOpenCodeRefusal(ue) {
		return false
	}
	for _, phrase := range openCodeAbusePhrases {
		if containsFoldASCII(ue.Body, phrase) {
			return true
		}
	}
	return containsFoldASCII(ue.Body, openCodeNetworkAbusePair[0]) &&
		containsFoldASCII(ue.Body, openCodeNetworkAbusePair[1])
}

// containsFoldASCII reports whether b contains s, comparing ASCII
// case-insensitively.
//
// It exists instead of bytes.ToLower because the refusal body is capped at
// constants.UpstreamErrLimit (1 MiB): lowercasing that much on every refusal
// allocates a megabyte on the 1 vCPU box this gateway is sized for (AGENTS.md
// §4.A/§4.G). This scan allocates nothing.
func containsFoldASCII(b []byte, s string) bool {
	if s == "" {
		return true
	}
	if len(s) > len(b) {
		return false
	}
	for i := 0; i+len(s) <= len(b); i++ {
		if matchesFoldASCII(b[i:], s) {
			return true
		}
	}
	return false
}

// matchesFoldASCII compares the first len(s) bytes of b against s.
func matchesFoldASCII(b []byte, s string) bool {
	for j := range len(s) {
		if lowerASCII(b[j]) != lowerASCII(s[j]) {
			return false
		}
	}
	return true
}

// lowerASCII folds one ASCII byte to lower case; every other byte is returned
// unchanged, so multi-byte UTF-8 is compared byte-for-byte.
func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// openCodeUsageLimitToken is the error type the gate answers with when the
// budget accounted to the upstream session is spent. Matched as a token for the
// same reason as the refusal above: the prose is not stable.
const openCodeUsageLimitToken = "FreeUsageLimitError"

// isOpencodeUsageLimit reports whether the gate answered 429 because the
// budget of the upstream session the request traveled on is spent — as opposed
// to a per-egress or per-key rate limit, which no session change cures.
func isOpencodeUsageLimit(ue *proxy.UpstreamError) bool {
	return ue != nil && ue.StatusCode == http.StatusTooManyRequests &&
		bytes.Contains(ue.Body, []byte(openCodeUsageLimitToken))
}

// openCodeOverloadToken is the wording of the free lane's load shedding. Only
// this 429 is transient: the quota 429 (FreeUsageLimitError) is an account
// verdict and repeating it only delays the error the caller needs to see.
const openCodeOverloadToken = "overloaded"

// isOpenCodeOverload reports whether the gate shed this request under load —
// the transient 429 the official CLI retries (decolua/9router@989b1eecb).
func isOpenCodeOverload(ue *proxy.UpstreamError) bool {
	return ue != nil && ue.StatusCode == http.StatusTooManyRequests &&
		containsFoldASCII(ue.Body, openCodeOverloadToken)
}

// isOpenCodeSpentSession reports whether the gate answered 403 or a non-overload
// 429: the session the request traveled on is spent or flagged, so the caller
// must drop it rather than keep dialing with it (#5559).
func isOpenCodeSpentSession(ue *proxy.UpstreamError) bool {
	if ue == nil {
		return false
	}
	return ue.StatusCode == http.StatusForbidden ||
		(ue.StatusCode == http.StatusTooManyRequests && !isOpenCodeOverload(ue))
}

// refusedResponseToError moves a gate answer (403/429) from the response
// position into the error position. The stealth forward and ForwardOpenAI both
// return a non-200 as a plain response, while the refusal classifiers read the
// error position — without this lift a 403 FreeTierError or a 429
// FreeUsageLimitError would sail past every retry untouched. The body is read
// here (capped) because the token lives in it; the response is consumed.
//
// The response headers travel with the error because Retry-After lives only
// there: a spent free-tier allowance announces its reset in that header, and
// dropping it left the caller waiting the hardcoded minute instead of the real
// window. The plain-HTTP path (proxy.doRequestOnce) already keeps this header,
// so carrying it here stops the two transports from disagreeing.
func refusedResponseToError(resp *http.Response) *proxy.UpstreamError {
	if resp == nil || (resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests) {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, constants.UpstreamErrLimit))
	_ = resp.Body.Close()
	return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}
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
	// The egress-level abuse flag is terminal: no header, session or body change
	// clears it, so a replay only extends the penalty. Answer with the refusal
	// itself, before the strict-proxy check and before the retry sleep — this is
	// a property of the refusal, not of the caller's egress policy.
	if isOpenCodeTerminalRefusal(ue) {
		return nil, ue
	}
	// The caller forbade routing around its proxy; report the refusal instead.
	if proxy.StrictProxy(ctx) {
		return nil, err
	}
	// A replay that was itself refused says this egress is out, not that the
	// request shape was stale. Replaying again within the cooldown cannot
	// succeed; skip it (and its sleep) rather than amplify the penalty. Placed
	// after the strict-proxy check because that path never replays at all.
	if openCodeRetryCoolingDown(time.Now()) {
		return nil, ue
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
		noteRefusedReplay(directErr)
		return nil, ue
	}
	if replayUe := refusedResponseToError(directResp); replayUe != nil {
		// The replay was refused as well; the original refusal is the answer.
		noteRefusedReplay(replayUe)
		return nil, ue
	}
	return directResp, nil
}

// noteRefusedReplay arms the cooldown when a replay came back refused.
//
// It reads both positions a refusal can arrive in: sendDirect is
// proxy.ForwardOpenAI on the plain lanes, which converts a non-200 into an
// error, while the stealth forward returns it as a plain response. Recording
// only the response position would leave the common lane without a cooldown at
// all.
func noteRefusedReplay(err error) {
	var ue *proxy.UpstreamError
	if errors.As(err, &ue) && isOpenCodeRefusal(ue) {
		noteOpenCodeRetryRefused(time.Now())
	}
}

// Free-lane load shedding: total attempts (first try + retries) and backoff,
// mirroring the official CLI (decolua/9router@989b1eecb). Live bursts last
// ~20 s — a request that kept failing for 8 s succeeded at 19 s — so the
// schedule below covers 800+1600+2400+3200+4000*3 ≈ 20 s.
const openCodeOverloadAttempts = 8

// The delays are vars so a test can shrink the ~20s budget; production never
// writes them.
var (
	openCodeOverloadBaseDelay = 800 * time.Millisecond
	openCodeOverloadMaxDelay  = 4 * time.Second
)

// forwardOpenCodeRotating extends the free-lane forward with the two recoveries
// the free tier needs, in this order:
//
//   - Transient load shedding. The lane answers 429 "The inference service is
//     overloaded. Please try again later." once it is busy, and the official CLI
//     retries that rather than surfacing it. Only that wording is retried, so a
//     quota 429 (FreeUsageLimitError) is reported without delay.
//   - A spent session. The free tier accounts its quota per upstream session,
//     and the official CLI recovers a spent session the same way it starts
//     every conversation: with a freshly minted session id and a fresh budget.
//     When the gate answers 429 FreeUsageLimitError on a router-minted stable
//     session, rotate() installs a new session and the replay goes out once on
//     it. A client-supplied session travels as rotate == nil: it identifies the
//     caller's own conversation upstream and is never swapped behind its back.
//
// A 403 or a non-overload 429 means upstream spent or flagged the session the
// request traveled on (#5559). drop() removes that id from the stable-session
// cache, so the next request mints a fresh one instead of reusing the spent id
// until restart.
//
// Each recovery is bounded: the overload backoff covers openCodeOverloadAttempts
// attempts total, and the session rotation runs once. A replay still shedding
// after that budget, or a fresh session that also answers 429, is the caller's
// fallback machinery to advance, not another loop here.
func forwardOpenCodeRotating(ctx context.Context, send, sendDirect func() (*http.Response, error), rotate, drop func()) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		resp, err := forwardOpenCode(ctx, send, sendDirect)
		if err == nil {
			return resp, nil
		}
		var ue *proxy.UpstreamError
		if !errors.As(err, &ue) {
			return nil, err
		}
		if isOpenCodeSpentSession(ue) && drop != nil {
			drop()
		}
		if isOpenCodeOverload(ue) && attempt < openCodeOverloadAttempts {
			delay := min(openCodeOverloadBaseDelay*time.Duration(attempt), openCodeOverloadMaxDelay)
			log.Debug("opencode", "free lane overloaded, retrying with backoff",
				"attempt", attempt, "max_attempts", openCodeOverloadAttempts, "delay", delay)
			if !sleepOpenCode(ctx, delay) {
				return nil, ue
			}
			continue
		}
		if rotate != nil && isOpencodeUsageLimit(ue) {
			log.Warn("opencode", "free session budget spent, rotating session and replaying once")
			rotate()
			return forwardOpenCode(ctx, send, sendDirect)
		}
		return nil, err
	}
}

// sleepOpenCode waits for delay unless the caller goes away first. It reports
// false when the context ended, so the caller answers with the upstream status
// instead of retrying for a client that has gone.
func sleepOpenCode(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
