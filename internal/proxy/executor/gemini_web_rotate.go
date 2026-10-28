package executor

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

// Cookie rotation on Gemini Web.
//
// The session timestamp cookie — `__Secure-1PSIDTS` — is what carries a
// session into the model registry, and the site rotates it every few minutes.
// Measured live against gemini.google.com on 2026-10-21:
//
//   - a header carrying only `__Secure-1PSID` was served Flash-Lite, and
//     reported no quota;
//   - the same header with `__Secure-1PSIDTS` added was served Flash-Lite,
//     Flash, Pro and Argon, and reported the 5-hour and weekly windows.
//
// The site issues that cookie from one place only: the rotation endpoints on
// accounts.google.com. A plain page load hands out `NID` and `COMPASS` and
// nothing else, so a session that is not rotated goes back to one model after
// a few minutes and the operator has to paste the cookie again.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/cookies/rotate.go — the
// endpoints, the two paths, the sentinel payload and the rate-limit guard are
// that project's shape.

// geminiWebRotateEndpoints are the addresses the renewal goes to.
//
// They are variables rather than constants so a test can point them at its
// own server: the endpoints live on another host from the site, so a test
// that could not move them could only ever assert on a request that never
// left the process.
var (
	geminiWebRotatePostURL = "https://accounts.google.com/RotateCookies"
	// geminiWebRotatePageURL is the page the browser's own iframe loads. Its
	// body carries the rotation id and the interval the site wants, and the
	// renew request follows it.
	geminiWebRotatePageURL = "https://accounts.google.com/RotateCookiesPage?og_pid=658&rot=3&origin=https%3A%2F%2Fgemini.google.com&exp_id=0"
	// geminiWebRotateOrigin is the site the endpoints belong to. The renewal
	// request names it as its origin and refers to its page, because a
	// request that named the chat site while posting to accounts.google.com
	// is not a request the site made.
	geminiWebRotateOrigin = "https://accounts.google.com"
)

// geminiWebRotateSentinel is the payload that renews the short-lived
// timestamp cookies. It must be sent verbatim, leading zeros included — it
// is not a JSON value this code builds.
const geminiWebRotateSentinel = `[000,"-0000000000000000000"]`

// geminiWebRotateMinInterval is the shortest gap between two renewals.
//
// The site answers a second call in the same minute with HTTP 429, so a
// rotation that runs more often than this is a rotation that gets itself
// rate-limited.
const geminiWebRotateMinInterval = 60 * time.Second

// geminiWebRotateDefaultInterval is the gap between two renewals when the
// site does not name one.
//
// The reference bridge uses nine minutes, which is a quarter of the cookie's
// own lifetime and leaves room for two failures before the session lapses.
const geminiWebRotateDefaultInterval = 9 * time.Minute

// geminiWebRotateMaxInterval is the longest gap the site may ask for.
const geminiWebRotateMaxInterval = time.Hour

// geminiWebRotateInitRe reads the rotation id and the interval out of the
// page the browser's iframe loads.
//
// The page calls `init('<id>', <seconds>, <seconds>)`, and the renew request
// carries the id back.
var geminiWebRotateInitRe = regexp.MustCompile(`init\('([^']{4,64})'\s*,\s*([0-9.]+)\s*,[^)]*?([0-9.]+)\s*\)`)

// geminiWebRotateResult is the outcome of one rotation.
type geminiWebRotateResult struct {
	// Status is the status the site answered with, 0 when no call was made.
	Status int
	// Rotated reports whether the site issued a new cookie.
	Rotated bool
	// Cookie is the credential after the rotation.
	Cookie string
	// NextInterval is when the next rotation should run.
	NextInterval time.Duration
}

// geminiWebRotate renews a credential's session cookies.
//
// Two paths, in the order the browser uses them:
//
//  1. a POST of the sentinel payload, which renews the short-lived timestamp
//     cookies and the country codes in one call;
//  2. the iframe page, whose body carries a rotation id that a second POST
//     uses.
//
// The second path is the fallback rather than a choice: the site answers a
// second call within the same minute with HTTP 429, so calling both at once
// is self-defeating.
func geminiWebRotate(ctx context.Context, session *geminiWebSession, credential string) geminiWebRotateResult {
	result := geminiWebRotateResult{Cookie: credential, NextInterval: geminiWebRotateDefaultInterval}
	if session == nil || session.stealth == nil {
		return result
	}
	stack := session.stealth

	// The rotation endpoints live on accounts.google.com, so the session's
	// cookies have to travel there. The jar is per-host, so they are copied
	// rather than looked up.
	stack.shareCredentialTo(geminiWebRotatePostURL)

	status, setCookies, err := stack.postRotate(ctx, geminiWebRotatePostURL, geminiWebRotateSentinel)
	if err != nil {
		return result
	}
	result.Status = status
	merged, changed := geminiWebAbsorbSetCookies(credential, setCookies)
	if changed {
		result.Cookie = merged
		result.Rotated = true
	}
	if status == http.StatusOK && result.Rotated {
		return result
	}

	// The sentinel path did not renew anything. The iframe path is the site's
	// own fallback, and it also carries the interval the site wants.
	pageStatus, pageCookies, pageBody, pageErr := stack.getRotatePage(ctx)
	if pageErr != nil {
		return result
	}
	if result.Status == 0 {
		result.Status = pageStatus
	}
	if merged, changed := geminiWebAbsorbSetCookies(result.Cookie, pageCookies); changed {
		result.Cookie = merged
		result.Rotated = true
	}
	if pageStatus != http.StatusOK {
		return result
	}
	rotationID, interval, ok := geminiWebParseRotatePage(pageBody)
	if !ok {
		return result
	}
	if interval > 0 {
		result.NextInterval = geminiWebClampRotateInterval(interval)
	}
	body := fmt.Sprintf("[658,%q]", rotationID)
	status, setCookies, err = stack.postRotate(ctx, geminiWebRotatePostURL, body)
	if err != nil {
		return result
	}
	result.Status = status
	if merged, changed := geminiWebAbsorbSetCookies(result.Cookie, setCookies); changed {
		result.Cookie = merged
		result.Rotated = true
	}
	return result
}

// geminiWebParseRotatePage reads the rotation id and the interval out of the
// page the iframe loads.
func geminiWebParseRotatePage(body string) (string, time.Duration, bool) {
	match := geminiWebRotateInitRe.FindStringSubmatch(body)
	if len(match) < 4 {
		return "", 0, false
	}
	interval := time.Duration(0)
	if seconds, err := strconv.ParseFloat(match[3], 64); err == nil {
		interval = time.Duration(seconds) * time.Second
	}
	return match[1], interval, true
}

// geminiWebClampRotateInterval keeps the site's own interval inside what the
// cookie's lifetime allows.
//
// A gap below a minute earns a 429, and a gap above an hour is longer than
// the cookie itself lasts, so the session would lapse before the next
// renewal.
func geminiWebClampRotateInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return geminiWebRotateDefaultInterval
	}
	if interval < geminiWebRotateMinInterval {
		return geminiWebRotateMinInterval
	}
	if interval > geminiWebRotateMaxInterval {
		return geminiWebRotateMaxInterval
	}
	return interval
}

// geminiWebAbsorbSetCookies merges the cookies the site issued into a
// credential.
//
// A cookie the site deleted — an empty value, a past expiry, or a negative
// max-age — is removed rather than kept, and a cookie the site did not
// mention keeps its value. The result is the credential to store, and
// `changed` reports whether a cookie that decides the session moved.
//
// "Changed" deliberately means the cookie that carries the session, and not
// "the site sent something": the site issues a fresh `NID` on every answer,
// including a refusal, so counting that as a renewal would report a refused
// renewal as a successful one — and the caller would then store a credential
// that reaches one model and believe the session had been renewed.
func geminiWebAbsorbSetCookies(cookie string, setCookies []*fhttp.Cookie) (string, bool) {
	if len(setCookies) == 0 {
		return cookie, false
	}
	values := map[string]string{}
	order := []string{}
	for _, part := range strings.Split(cookie, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found || strings.TrimSpace(name) == "" {
			continue
		}
		name = strings.TrimSpace(name)
		if _, seen := values[name]; !seen {
			order = append(order, name)
		}
		values[name] = strings.TrimSpace(value)
	}
	changed := false
	for _, issued := range setCookies {
		name := strings.TrimSpace(issued.Name)
		if name == "" {
			continue
		}
		if geminiWebCookieDeleted(issued) {
			if _, present := values[name]; present {
				delete(values, name)
				if geminiWebSessionCarryingCookies[name] {
					changed = true
				}
			}
			continue
		}
		if existing, present := values[name]; !present || existing != issued.Value {
			if geminiWebSessionCarryingCookies[name] {
				changed = true
			}
		}
		if _, present := values[name]; !present {
			order = append(order, name)
		}
		values[name] = issued.Value
	}
	if !changed {
		return cookie, false
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		value, present := values[name]
		if !present {
			continue
		}
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, "; "), true
}

// geminiWebCookieDeleted reports whether a Set-Cookie removes its cookie.
func geminiWebCookieDeleted(cookie *fhttp.Cookie) bool {
	if cookie.MaxAge < 0 {
		return true
	}
	if strings.TrimSpace(cookie.Value) == "" {
		return true
	}
	if !cookie.Expires.IsZero() && !cookie.Expires.After(time.Now()) {
		return true
	}
	return false
}
