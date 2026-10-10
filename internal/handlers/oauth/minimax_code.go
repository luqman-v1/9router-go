package oauth

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// --- minimax-code: PKCE S256 device flow against MiniMax's account hosts ---

// MiniMax Code's own sign-in (client mcode-public, PKCE S256), run with our
// own tokens: ~/.minimax is never read or written, so signing in here does not
// sign the CLI out and several accounts can coexist. Port of upstream
// src/lib/oauth/providers/minimax-code-shared.js (decolua/9router 85bc33ce).
//
//	1) POST {account}/oauth2/device/code
//	     client_id, scope, audience, code_challenge, code_challenge_method=S256
//	     → user_code, verification_uri(_complete), device_code (sometimes
//	       absent — MiniMax's variant polls with the user_code instead),
//	       expiry as expired_in (ms epoch or seconds) or expires_in, interval
//	       (seconds on the standard shape, milliseconds on the user-code one)
//	2) POST {account}/oauth2/token  (one POST per poll; the dashboard drives)
//	     grant_type=urn:ietf:params:oauth:grant-type:device_code,
//	     device_code|user_code, client_id, code_verifier
//	     → {status: pending|slow_down|denied|expired} on 200, or a standard
//	       OAuth {error} body; success carries access/refresh_token + expires_in

const (
	miniMaxCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// miniMaxCodeSignInSeconds is the default window a device code lives when
	// the reply names no expiry. Upstream signs in for 10 minutes.
	miniMaxCodeSignInSeconds = 10 * 60
	// miniMaxCodeDefaultIntervalSeconds is the poll cadence a reply without an
	// interval implies.
	miniMaxCodeDefaultIntervalSeconds = 5
	// miniMaxCodeDefaultExpiresIn covers a token reply without expires_in. mcode
	// access tokens live about an hour; storing zero would mark the connection
	// expired and drive a refresh on every single request.
	miniMaxCodeDefaultExpiresIn = 3600
	// miniMaxCodePollByUser marks the variant in which the token endpoint is
	// polled with the user_code itself because the device reply carried no
	// device_code. The modal threads one value through, so the user code is
	// surfaced as device_code and this flag tells the poll which field to send.
	miniMaxCodePollByUser = "minimaxPollByUser"
)

// miniMaxCodeSite is one mcode site's OAuth host.
type miniMaxCodeSite struct {
	accountBase string
}

// miniMaxCodeSites maps each mcode provider id to its account host. The two
// sites hold separate sign-ins — an account on account.minimax.cn says
// nothing about account.minimax.io — so they are never cross-falled
// (AGENTS.md §3.A).
var miniMaxCodeSites = map[string]miniMaxCodeSite{
	"minimax-code":        {accountBase: "https://account.minimax.cn"},
	"minimax-code-global": {accountBase: "https://account.minimax.io"},
}

func miniMaxCodeSiteFor(provider string) (miniMaxCodeSite, bool) {
	site, ok := miniMaxCodeSites[provider]
	return site, ok
}

// minimaxCodeStart requests a device code. The PKCE verifier rides along in
// the session so the poll can present it: MiniMax's device grant requires a
// code_verifier, and the dashboard has no other place to keep it.
func minimaxCodeStart(provider string) (map[string]any, error) {
	site, ok := miniMaxCodeSiteFor(provider)
	if !ok {
		return nil, fmt.Errorf("unsupported MiniMax Code site: %s", provider)
	}
	verifier := randomString(64)
	challenge := sha256Base64(verifier)

	form := url.Values{
		"client_id":             {"mcode-public"},
		"scope":                 {"agent.default"},
		"audience":              {"agent-backend"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	data, status, err := postForm(site.accountBase+"/oauth2/device/code", form, nil)
	if err != nil {
		return nil, fmt.Errorf("MiniMax device code request failed: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("MiniMax device code request failed: HTTP %d %s", status, strVal(data, "error", "error_description", "message"))
	}

	userCode := strVal(data, "user_code")
	deviceCode := strVal(data, "device_code")
	uri := firstNonEmptyStr(
		strVal(data, "verification_uri_complete"),
		strVal(data, "verification_uri"),
		strVal(data, "verification_url"),
	)
	if userCode == "" || uri == "" {
		return nil, fmt.Errorf("MiniMax device code response unusable (HTTP %d)", status)
	}

	// No device_code → MiniMax's variant: the token endpoint is polled with the
	// user_code itself. The modal threads one value through, so the user code
	// becomes the device_code and the session flag switches the poll's field.
	pollByUser := deviceCode == ""
	if pollByUser {
		deviceCode = userCode
	}

	return map[string]any{
		"device_code":               deviceCode,
		"user_code":                 userCode,
		"verification_uri":          uri,
		"verification_uri_complete": uri,
		"expires_in":                miniMaxCodeDeadline(data, time.Now()),
		"interval":                  miniMaxCodeInterval(data, pollByUser),
		"session":                   map[string]any{"codeVerifier": verifier, miniMaxCodePollByUser: pollByUser},
	}, nil
}

// miniMaxCodeDeadline normalizes the device code's end to seconds. MiniMax
// states it in odd units: expired_in may be a millisecond epoch or a plain
// number of seconds, and expires_in is always seconds.
func miniMaxCodeDeadline(data map[string]any, now time.Time) int {
	if secs, ok := positiveInt(data, "expires_in"); ok {
		return secs
	}
	raw, ok := positiveInt(data, "expired_in")
	if !ok {
		return miniMaxCodeSignInSeconds
	}
	if raw < 1e12 {
		return raw
	}
	remaining := time.UnixMilli(int64(raw)).Sub(now)
	if remaining <= 0 {
		return 1
	}
	return int(math.Ceil(remaining.Seconds()))
}

// miniMaxCodeInterval normalizes the poll cadence to seconds: the standard
// device shape states seconds, MiniMax's user-code variant states milliseconds.
func miniMaxCodeInterval(data map[string]any, pollByUser bool) int {
	raw, ok := positiveInt(data, "interval")
	if !ok {
		return miniMaxCodeDefaultIntervalSeconds
	}
	if !pollByUser {
		return raw
	}
	if raw >= 60 {
		return (raw + 500) / 1000
	}
	return raw
}

// firstNonEmptyStr returns the first non-blank value, for a field MiniMax
// publishes under any of several names across its two sites.
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// positiveInt reads a strictly positive finite number field as an int.
func positiveInt(data map[string]any, key string) (int, bool) {
	f, ok := data[key].(float64)
	if !ok || f <= 0 || f != f {
		return 0, false
	}
	return int(f), true
}

// positiveIntOr reads a numeric field, falling back when it is absent or
// non-positive.
func positiveIntOr(data map[string]any, key string, fallback int) int {
	if v, ok := positiveInt(data, key); ok {
		return v
	}
	return fallback
}

// minimaxCodePoll exchanges the device code for tokens, or reports that the
// user has not finished signing in yet.
func minimaxCodePoll(provider, code string, session map[string]any) (deviceTokens, error) {
	var t deviceTokens
	site, ok := miniMaxCodeSiteFor(provider)
	if !ok {
		return t, fmt.Errorf("unsupported MiniMax Code site: %s", provider)
	}
	if code == "" {
		return t, fmt.Errorf("authorization_pending")
	}

	form := url.Values{
		"grant_type": {miniMaxCodeGrantType},
		"client_id":  {"mcode-public"},
	}
	if pollByUserCode(session) {
		form.Set("user_code", code)
	} else {
		form.Set("device_code", code)
	}
	if verifier := psdStr(session, "codeVerifier"); verifier != "" {
		form.Set("code_verifier", verifier)
	}

	data, _, err := postForm(site.accountBase+"/oauth2/token", form, nil)
	if err != nil {
		return t, fmt.Errorf("poll_failed: %w", err)
	}

	// MiniMax answers a still-pending poll with its own envelope on HTTP 200;
	// every other value ends the wait one way or another.
	switch strVal(data, "status") {
	case "pending":
		return t, fmt.Errorf("authorization_pending")
	case "slow_down":
		return t, fmt.Errorf("slow_down")
	case "denied", "access_denied":
		return t, fmt.Errorf("access_denied: sign-in denied")
	case "expired", "expired_token":
		return t, fmt.Errorf("expired_token: the sign-in window closed")
	case "":
		// Not MiniMax's envelope: fall through to the token/error shape.
	default:
		return t, fmt.Errorf("unexpected_status:%s", strVal(data, "status"))
	}
	// Standard OAuth error body on the same call.
	if e := strVal(data, "error"); e != "" {
		if e == "authorization_pending" || e == "slow_down" {
			return t, fmt.Errorf("%s", e)
		}
		return t, fmt.Errorf("%s: %s", e, strVal(data, "error_description"))
	}

	t.access = strVal(data, "access_token")
	t.refresh = strVal(data, "refresh_token")
	if t.access == "" {
		return t, fmt.Errorf("no access_token in the MiniMax token reply")
	}
	t.expiresIn = positiveIntOr(data, "expires_in", miniMaxCodeDefaultExpiresIn)
	// The usage panel signs its account-API calls with the account id mcode
	// answers /v1/api/user/info with. The login reply carries none, so it is
	// resolved lazily on the first usage read rather than guessed here.
	t.extra = map[string]any{}
	return t, nil
}

// pollByUserCode reports whether this poll targets MiniMax's user-code variant.
func pollByUserCode(session map[string]any) bool {
	v, _ := session[miniMaxCodePollByUser].(bool)
	return v
}

// psdStr reads a trimmed string field from a session blob.
func psdStr(session map[string]any, key string) string {
	s, _ := session[key].(string)
	return strings.TrimSpace(s)
}
