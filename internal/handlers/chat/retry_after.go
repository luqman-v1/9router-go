package chat

import (
	json "encoding/json/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/constants"
)

// brief429RetryTolerance is the longest wait a rate-limited upstream may ask
// for and still be worth repeating on the same client turn. It is the router's
// own base backoff (providers.BackoffConfig.BaseMs): at or under it the account
// is very likely free again by the time the next attempt runs, so an ordinary
// burst is absorbed instead of surfacing a 429. Above it the quota window is
// spent, and re-sending to the same account only deepens Google's backoff
// instead of letting the next account serve the turn.
const brief429RetryTolerance = 2 * time.Second

// comboRetryWaitCap bounds how long a fully-failed combo pass will hold the
// request before retrying. Longer upstream Retry-After values are surfaced via
// the Retry-After header instead so the client decides. A 429 is held to the
// stricter brief429RetryTolerance: unlike a transient 5xx, a long Retry-After
// on a 429 names a quota window that waiting out here cannot shorten.
const comboRetryWaitCap = 8 * time.Second

// comboPassRetryWait returns how long to hold a fully-failed combo pass before
// repeating it, or 0 when the pass must not be repeated.
//
// earliest is the smallest Retry-After any candidate asked for; rateLimited
// reports whether a 429 was among the failures. When it was, only a wait
// inside brief429RetryTolerance earns another pass — a longer one means every
// candidate in the pool is out of quota, and retrying is what kept the same
// limited accounts absorbing the client's retries.
func comboPassRetryWait(earliest time.Duration, rateLimited bool) time.Duration {
	limit := comboRetryWaitCap
	if rateLimited {
		limit = brief429RetryTolerance
	}
	if earliest <= 0 || earliest > limit {
		return 0
	}
	return earliest
}

// retryableCooldownSec returns how long a failed account must stay out of
// rotation, in seconds, given the classifier's own backoff as the baseline.
//
// A 429 names the window the account is out for, in either direction: honour a
// long one and the next request no longer re-picks an exhausted account (the
// old code locked it for the classifier's 2s base, so the same limited account
// came back two seconds later and absorbed another failing request), and
// honour a short one and the bounded second pass below actually finds the
// account again instead of waiting out a lock it set itself. Every other
// status keeps the quota-reset reading it has always had. maxResetCooldown
// stops a hostile or nonsensical duration from parking an account forever.
func retryableCooldownSec(statusCode int, base time.Duration, ue *upstreamError) int {
	cooldown := base
	if statusCode == http.StatusTooManyRequests {
		if wait, ok := retryAfterWait(ue); ok {
			cooldown = wait
		}
	} else if dur, ok := extractResetDuration(ue.Body); ok {
		cooldown = dur
	}
	return ceilSeconds(min(cooldown, maxResetCooldown))
}

// passRetry is the rate-limit bookkeeping for one combo pass. The OpenAI and
// Claude combo loops need the same three answers — how long to hold before
// repeating the pass, what to tell the client, and whether a 429 was among the
// failures — and keeping them together stops the two endpoints from drifting
// apart on a contract only one of them would enforce.
type passRetry struct {
	earliest    time.Duration
	rateLimited bool
}

// note folds an upstream failure into the pass-wide wait.
func (p *passRetry) note(ue *upstreamError) {
	if ue.StatusCode == http.StatusTooManyRequests {
		p.rateLimited = true
	}
	if wait, ok := retryAfterWait(ue); ok && (p.earliest == 0 || wait < p.earliest) {
		p.earliest = wait
	}
}

// reset starts a fresh pass: the previous one's exclusions and waits do not
// carry over.
func (p *passRetry) reset() { *p = passRetry{} }

// wait returns how long to hold before repeating the pass, or 0 when the pass
// must not be repeated.
func (p *passRetry) wait() time.Duration { return comboPassRetryWait(p.earliest, p.rateLimited) }

// writeError answers the client with lastErr, publishing the earliest
// Retry-After the pass collected three ways: the Retry-After header, the
// human-readable suffix on the error message, and the reset_at / retry_after
// pair in the JSON error object. The body fields exist because a client that
// never surfaces headers — a browser SDK, a log-only integration — has no
// other way to learn when the combo becomes usable again.
//
// Without a usable reset the upstream body is forwarded untouched: an invented
// reset instant would send clients into a retry loop against a quota with no
// known end.
func (p *passRetry) writeError(cw *committedResponseWriter, lastErr *upstreamError) {
	// Set before any branch so the pass-through path below is a JSON body
	// too. The upstream content type is not trusted here: a 429 served as
	// text/html is what makes an SDK report a parse crash instead of a quota.
	cw.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
	if p.earliest > 0 {
		sec := max(ceilSeconds(p.earliest), 1)
		cw.Header().Set("Retry-After", strconv.Itoa(sec))
		retryAt := time.Now().UTC().Add(p.earliest)
		retryHuman := formatRetryAfter(retryAt.Format(time.RFC3339))
		var errBody map[string]any
		if err := json.Unmarshal(lastErr.Body, &errBody); err == nil {
			if errObj, ok := errBody["error"].(map[string]any); ok {
				if msg, _ := errObj["message"].(string); msg != "" {
					errObj["message"] = msg + " (" + retryHuman + ")"
					// The instant has to travel as data, not only as prose:
					// only a human reads "reset after 2m 30s", and a client
					// that never surfaces the header has nothing else left.
					errObj["reset_at"] = retryAt.Format(time.RFC3339)
					errObj["retry_after"] = sec
					updated, _ := json.Marshal(errBody)
					cw.WriteHeader(lastErr.StatusCode)
					cw.Write(updated)
					return
				}
			}
		}
	}
	cw.WriteHeader(lastErr.StatusCode)
	cw.Write(lastErr.Body)
}

// retryAfterWait reports how long the upstream asked the client to wait before
// re-sending. It reads both carriers a rate limit uses — the Retry-After
// response header and the google.rpc.RetryInfo payload in the JSON body — plus
// the quota-reset forms extractRetryAfter already knows. The longest value wins,
// because a wait is only over when every source's window has passed.
func retryAfterWait(ue *upstreamError) (time.Duration, bool) {
	if ue == nil {
		return 0, false
	}
	var longest time.Duration
	found := false
	note := func(d time.Duration, ok bool) {
		if ok && d > longest {
			longest, found = d, true
		}
	}
	note(parseRetryAfterHeader(ue.Header.Get("Retry-After")))
	note(retryInfoDelay(ue.Body))
	// extractRetryAfter already resolves quotaResetDelay, "Resets in X" and the
	// retryAfter/retry_after/resetsAt/resets_at fields; re-reading its string
	// keeps one owner of the field names.
	note(parseRetryAfterValue(extractRetryAfter(ue.Body)))
	return longest, found
}

// retryInfoDelay reads the google.rpc.RetryInfo detail a rate-limited Gemini
// request carries. retryDelay sits on the detail itself, not inside ErrorInfo
// metadata, so extractResetDuration (which only looks for quotaResetDelay there)
// never sees it — and Google's most common carrier of the wait was therefore
// invisible to the whole cooldown/failover path.
func retryInfoDelay(body []byte) (time.Duration, bool) {
	if len(body) == 0 {
		return 0, false
	}
	var rpcErr struct {
		Error struct {
			Details []struct {
				Type        string `json:"@type"`
				RetryDelay  string `json:"retryDelay"`
				MetadataMap struct {
					RetryDelay string `json:"retryDelay"`
				} `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpcErr); err != nil {
		return 0, false
	}
	for _, d := range rpcErr.Error.Details {
		if !strings.Contains(d.Type, "RetryInfo") && d.RetryDelay == "" && d.MetadataMap.RetryDelay == "" {
			continue
		}
		if dur, ok := parseDurationString(d.RetryDelay); ok {
			return dur, true
		}
		if dur, ok := parseDurationString(d.MetadataMap.RetryDelay); ok {
			return dur, true
		}
	}
	return 0, false
}

// parseRetryAfterHeader reads an RFC 9110 Retry-After value: delta-seconds or
// an HTTP-date. Anything else — an empty header, a bare word, a date already in
// the past — carries no usable wait.
func parseRetryAfterHeader(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
	}
	return 0, false
}

// parseRetryAfterValue reads a Retry-After carried in a JSON body, which
// upstreams spell as an RFC3339 timestamp, a Go duration ("120s") or bare
// seconds ("120").
func parseRetryAfterValue(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, false
	}
	return parseDurationString(v)
}

func parseDurationString(v string) (time.Duration, bool) {
	v = strings.TrimRight(strings.TrimSpace(v), ".")
	if v == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d, d > 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second, secs > 0
	}
	return 0, false
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}
