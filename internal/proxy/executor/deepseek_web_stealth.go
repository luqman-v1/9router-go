package executor

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/log"
)

// Stealth mode: the difference between a browser-login provider that survives
// and one whose account gets muted.
//
// chat.deepseek.com is not an API plan. The credential is a consumer account,
// and everything about the request says whether a real browser made it: the
// header set of the current web client, the client-attestation headers the site
// fetches from its own attestation hosts, how many requests a single account
// makes at once, and whether a conversation is replayed as one growing thread
// or re-sent whole on every turn. The last one is what a sibling implementation
// (snake-aabb-wtf/deepseek-web2api-free) names as the main cause of account
// mutes: re-sending the whole transcript each turn makes every message in the
// upstream session embed all previous turns, a quadratic growth no human client
// produces, and DeepSeek answers account-level enforcement for it — a plain
// JSON 200 carrying `{"data":{"biz_code":5,"biz_msg":"user is muted"}}`.
//
// So stealth mode is four things, all of them observable browser behaviour
// rather than a heuristic:
//
//  1. The fingerprint the current web build sends, including the
//     client-attestation headers (`x-hif-leim` / `x-hif-dliq`) that the real
//     client fetches from `hif-leim.deepseek.com` / `hif-dliq.deepseek.com`.
//  2. One upstream session per client conversation, with only the new tail of
//     the conversation sent on each turn.
//  3. A cap on how many requests one account makes at once, because an agent
//     client dispatches parallel sub-agents and a burst is what turns a busy
//     account into a limited one.
//  4. Recognition of the enforcement bodies (mute, rate-limit hint) so the
//     router cools the account down instead of retrying it.
//
// It is on by default because a browser-login provider has no other honest
// behaviour, and off per connection (`providerSpecificData.stealth = false`)
// for a diagnosed comparison against the plain path.

const (
	// deepSeekStealthDefaultConcurrency mirrors the sibling implementation's
	// DEEPSEEK_MAX_CONCURRENCY (2). Requests over the cap queue rather than
	// fail, so an agent fanning out sub-agents serializes instead of
	// bombarding one account.
	deepSeekStealthDefaultConcurrency = 2

	// deepSeekStealthDefaultJitterMs mirrors DEEPSEEK_JITTER_SECS (0.4s): a
	// random pause before the first upstream call of a turn. A burst of turns
	// that arrives in lockstep does not look like a person typing.
	deepSeekStealthDefaultJitterMs = 400

	// deepSeekStealthQueueWait bounds how long a turn waits for a slot before
	// giving up. Long enough to absorb a fan-out, short enough that a caller
	// waiting behind a stuck turn eventually gets an answer it can act on.
	deepSeekStealthQueueWait = 30 * time.Second
)

// deepSeekStealth is the per-connection stealth configuration.
type deepSeekStealth struct {
	// enabled turns the whole mode off. Off restores the plain path: the
	// minimal header set this provider shipped with, a fresh session per
	// request, and no attestation fetch.
	enabled bool

	// persistSession keeps one upstream chat session per client conversation
	// and sends only the new tail of each turn.
	persistSession bool

	// maxConcurrency caps in-flight requests per account. 0 disables the cap.
	maxConcurrency int

	// jitter is the upper bound of the random pause before the first upstream
	// call. 0 disables it.
	jitter time.Duration
}

// deepSeekStealthFor reads the per-connection stealth options.
//
// Every knob defaults to the browser-parity value, so a connection that
// configures nothing gets the stealth path. Only an explicit `stealth: false`
// opts out.
func deepSeekStealthFor(connData map[string]any) deepSeekStealth {
	stealth := deepSeekStealth{
		enabled:        deepSeekConnBool(connData, "stealth", true),
		persistSession: deepSeekConnBool(connData, "persistSession", true),
		maxConcurrency: deepSeekConnInt(connData, "maxConcurrency", deepSeekStealthDefaultConcurrency),
		jitter:         time.Duration(deepSeekConnInt(connData, "jitterMs", deepSeekStealthDefaultJitterMs)) * time.Millisecond,
	}
	if !stealth.enabled {
		return deepSeekStealth{}
	}
	if stealth.maxConcurrency < 0 {
		stealth.maxConcurrency = 0
	}
	if stealth.jitter < 0 {
		stealth.jitter = 0
	}
	return stealth
}

// deepSeekConnBool reads a boolean option, accepting the numeric spelling a
// JSON body produces for a value an operator typed by hand.
func deepSeekConnBool(connData map[string]any, key string, fallback bool) bool {
	if connData == nil {
		return fallback
	}
	switch value := connData[key].(type) {
	case bool:
		return value
	case float64:
		return value != 0
	case int:
		return value != 0
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

// deepSeekConnInt reads an integer option. Go decodes JSON numbers as float64,
// so both spellings are accepted.
func deepSeekConnInt(connData map[string]any, key string, fallback int) int {
	if connData == nil {
		return fallback
	}
	switch value := connData[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
}

// deepSeekWebUserAgent is the build the fingerprint below was captured from.
//
// The site is reached without TLS impersonation — Go's ClientHello is Go's —
// so no UA can be consistent at the TLS layer. What is worth being consistent
// about is the HTTP layer, where a newer UA beside an older `Sec-Ch-Ua` is
// free evidence of a scripted client. The sibling implementation downgrades
// its captured UA to match the profile curl_cffi negotiates; here the declared
// client and the client-hint headers are simply kept on the same version.
const deepSeekWebUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// deepSeekWebSecChUA is the client-hint value for the UA above.
const deepSeekWebSecChUA = `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`

// deepSeekWebClientVersion is the version the current web build sends. The
// 2.0.0 stamp this provider shipped with is stale, and "update to the latest
// version" is one of the signals the site reports through an `event: toast`
// error frame.
const deepSeekWebClientVersion = "2.3.0"

// deepSeekBrowserHeaders is the header set the current chat.deepseek.com web
// client sends on every /api/v0/* call, in stealth mode.
//
// It is built from the captured request set of the live client rather than
// from what the endpoint strictly requires: `Sec-Fetch-*`, `Priority` and the
// client hints are not validated individually today, but a request carrying
// only some of them is a request no browser ever made. `Accept-Encoding` is
// deliberately absent — Go's transport manages compression, and setting it
// would suppress transparent gzip.
//
// Origin and Referer follow whatever host the connection dials, because a
// request whose headers name a different host than the one it is sent to is
// the single most obvious sign of a scripted client.
func deepSeekBrowserHeaders(origin string, extra map[string]string) map[string]string {
	headers := map[string]string{
		"Accept":                   "*/*",
		"Accept-Language":          "en-US,en;q=0.9",
		"Origin":                   origin,
		"Referer":                  origin + "/",
		"Priority":                 "u=1, i",
		"Sec-Ch-Ua":                deepSeekWebSecChUA,
		"Sec-Ch-Ua-Mobile":         "?0",
		"Sec-Ch-Ua-Platform":       `"Windows"`,
		"Sec-Fetch-Dest":           "empty",
		"Sec-Fetch-Mode":           "cors",
		"Sec-Fetch-Site":           "same-origin",
		"User-Agent":               deepSeekWebUserAgent,
		"X-Client-Bundle-Id":       "com.deepseek.chat",
		"X-Client-Locale":          "en-US",
		"X-Client-Platform":        "web",
		"X-Client-Version":         deepSeekWebClientVersion,
		"X-Client-Timezone-Offset": strconv.Itoa(deepSeekTimezoneOffsetSeconds()),
	}
	for k, v := range extra {
		headers[k] = v
	}
	return headers
}

// deepSeekPlainHeaders is the set this provider sent before stealth mode
// existed, kept for the `stealth: false` path so opting out is a real
// comparison rather than a different flavour of the same thing.
func deepSeekPlainHeaders(origin string, extra map[string]string) map[string]string {
	headers := map[string]string{
		"Accept":             "*/*",
		"Accept-Language":    "en-US,en;q=0.9",
		"Origin":             origin,
		"Referer":            origin + "/",
		"User-Agent":         deepSeekWebUserAgent,
		"X-Client-Bundle-Id": "com.deepseek.chat",
		"X-Client-Locale":    "en-US",
		"X-Client-Platform":  "web",
		"X-Client-Version":   deepSeekWebClientVersion,
	}
	for k, v := range extra {
		headers[k] = v
	}
	return headers
}

// DeepSeekBrowserHeaders is the exported fingerprint, for the dashboard probe.
//
// The probe and the executor must judge a credential against the same headers,
// or a probe that passes says nothing about whether the turn will: a header set
// maintained in two places drifts, and the drift shows up as a credential the
// dashboard calls valid and the executor is then refused for.
func DeepSeekBrowserHeaders() map[string]string {
	return deepSeekBrowserHeaders(deepSeekWebBase, nil)
}

// deepSeekTimezoneOffsetSeconds is the value `X-Client-Timezone-Offset`
// carries: seconds west of UTC, which is what the browser sends (UTC+7 → -25200).
func deepSeekTimezoneOffsetSeconds() int {
	_, offsetSeconds := time.Now().Zone()
	return -offsetSeconds
}

// deepSeekPause waits for a random part of the jitter window, or returns as
// soon as the caller's context ends.
func deepSeekPause(ctx context.Context, window time.Duration) {
	if window <= 0 {
		return
	}
	delay := time.Duration(rand.Int64N(int64(window)))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// deepSeekGate caps how many requests one account has in flight.
//
// The key is the credential, not the client: the cap exists to stop one
// consumer account from being hammered, and an account is per connection. The
// map is therefore bounded by the connection table and needs no eviction, and
// an entry lives as long as the process because a dropping policy would have to
// decide what to do with a slot that is currently held.
type deepSeekGate struct {
	mu    sync.Mutex
	slots map[string]chan struct{}
}

var deepSeekGates = &deepSeekGate{slots: make(map[string]chan struct{})}

// acquire takes a slot for key, waiting up to wait. The returned release is
// safe to call more than once and must be called on every path, including the
// failed ones — a leaked slot permanently narrows the account.
func (g *deepSeekGate) acquire(ctx context.Context, key string, capacity int, wait time.Duration) (func(), error) {
	if capacity <= 0 || key == "" {
		return func() {}, nil
	}

	slots := g.slotsFor(key, capacity)

	// A free slot is taken before any timer is armed. With the wait and the
	// slot both ready, select picks at random, so a zero or short wait would
	// otherwise reject a request the account could serve right now.
	select {
	case slots <- struct{}{}:
		return deepSeekGateRelease(slots), nil
	default:
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("deepseek-web: %d requests already in flight for this account; waited %s", capacity, wait)
	}

	return deepSeekGateRelease(slots), nil
}

// deepSeekGateRelease returns the idempotent release for one held slot. A
// second call must not free the slot twice: the cap would leak capacity on
// every path that releases more than once.
func deepSeekGateRelease(slots chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() { <-slots })
	}
}

func (g *deepSeekGate) slotsFor(key string, capacity int) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	slots, ok := g.slots[key]
	if !ok {
		slots = make(chan struct{}, capacity)
		g.slots[key] = slots
	}
	return slots
}

// deepSeekStealthLog reports the mode once per turn so a diagnosis can tell a
// stealth turn from a plain one without guessing from the dashboard.
func deepSeekStealthLog(stealth deepSeekStealth, reused bool) {
	if !stealth.enabled {
		log.Debug("deepseek-web", "stealth disabled for this connection")
		return
	}
	log.Debug("deepseek-web", "stealth turn",
		"session_reused", reused,
		"persist_session", stealth.persistSession,
		"max_concurrency", stealth.maxConcurrency,
		"jitter_ms", stealth.jitter.Milliseconds(),
	)
}

