package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Muse Code subscription login (upstream src/lib/oauth/providers/muse.js):
// a Meta account runs the device-code grant against auth.meta.com, and the
// granted account token is then exchanged for the Model API key the chat
// transport actually uses. The device code is one-shot and Meta issues no
// refresh token; a failed key mint no longer forces a full re-login — the
// poll loop remembers the account token and retries only the mint.

const (
	museClientID      = "1031625952748946"
	museDeviceCodeURL = "https://auth.meta.com/oidc/device/authorization/"
	museTokenURL      = "https://auth.meta.com/oidc/device/token/"
	museKeyURL        = "https://api.meta.ai/muse-code/key"
	museAPIVersion    = "1.0.0"

	// museMintAttempts mirrors upstream's postExchange retry budget for the
	// key mint, which Meta rate-limits hard (429).
	museMintAttempts = 3
	// museMintBackoff is upstream's 5000 * (attempt + 1) wait between attempts.
	museMintBackoff = 5 * time.Second

	// musePendingMintTTL bounds how long a consumed device code's account
	// token is remembered for mint retries. Device codes themselves live
	// ~10-15 minutes; the entry only needs to survive the modal's poll loop.
	musePendingMintTTL = 15 * time.Minute
	// musePendingMintMaxTries caps how many polls may retry the key mint
	// before the login fails for real. Each poll already retries the mint up
	// to museMintAttempts times with backoff.
	musePendingMintMaxTries = 3
)

// musePendingMint remembers a Meta account token whose device code has
// already been consumed but whose Model API key mint has not finished. The
// device code is one-shot: once exchanged it can never be used again, so a
// failed mint must not force the user through the browser flow again. The
// next poll reuses the remembered token and retries only the mint.
type musePendingMint struct {
	accountToken string
	mintTries    int
	createdAt    time.Time
}

// musePendingMints maps device_code -> musePendingMint.
var musePendingMints sync.Map

func musePendingMintGet(code string) (musePendingMint, bool) {
	v, ok := musePendingMints.Load(code)
	if !ok {
		return musePendingMint{}, false
	}
	pm, ok := v.(musePendingMint)
	if !ok || time.Since(pm.createdAt) > musePendingMintTTL {
		musePendingMints.Delete(code)
		return musePendingMint{}, false
	}
	return pm, true
}

// errMuseMintTransient marks a key-mint failure worth retrying on a later
// poll (rate limit, upstream 5xx, transport error). Permanent failures
// (inactive subscription, payment required, other 4xx) are returned unwrapped
// so they surface to the user immediately.
var errMuseMintTransient = errors.New("transient mint failure")

// museHeaders are the two headers both Meta endpoints want; without
// x-api-version the device-code and key endpoints reject the request.
func museHeaders(extra map[string]string) map[string]string {
	headers := make(map[string]string, len(extra)+2)
	headers["Accept"] = "application/json"
	headers["x-api-version"] = museAPIVersion
	for k, v := range extra {
		headers[k] = v
	}
	return headers
}

// museStart asks Meta for a device code.
func museStart(ctx context.Context) (map[string]any, error) {
	form := url.Values{"client_id": {museClientID}}
	data, status, err := postForm(ctx, museDeviceCodeURL, form, museHeaders(map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}))
	if err != nil {
		return nil, fmt.Errorf("Muse Code device code request failed: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("Muse Code device code request failed: status %d", status)
	}
	if strVal(data, "device_code") == "" {
		return nil, fmt.Errorf("Muse Code device code response missing device_code")
	}
	if strVal(data, "user_code") == "" {
		return nil, fmt.Errorf("Muse Code device code response missing user_code")
	}
	if strVal(data, "verification_uri") == "" {
		return nil, fmt.Errorf("Muse Code device code response missing verification_uri")
	}
	data["session"] = map[string]any{}
	return data, nil
}

// musePoll trades the approved device code for a Model API key. Meta answers
// authorization_pending while the user is still in the browser, which the
// shared poll loop turns back into a "pending" state for the modal.
func musePoll(ctx context.Context, code string) (deviceTokens, error) {
	var t deviceTokens

	// If an earlier poll already consumed this (one-shot) device code but the
	// key mint did not finish, reuse the remembered account token and skip the
	// exchange: the user must not approve in the browser twice.
	access := ""
	if pm, ok := musePendingMintGet(code); ok {
		access = pm.accountToken
	}
	if access == "" {
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {code},
			"client_id":   {museClientID},
		}
		data, status, err := postForm(ctx, museTokenURL, form, museHeaders(map[string]string{
			"Content-Type": "application/x-www-form-urlencoded",
		}))
		if err != nil {
			return t, fmt.Errorf("poll_failed: %w", err)
		}
		// A non-200 without a parseable OAuth error body (WAF block, proxy
		// 403, HTML error page) must surface as an error. Reporting it as
		// authorization_pending would make the frontend poll forever.
		if status != http.StatusOK {
			if e := strVal(data, "error"); e != "" {
				if e == "authorization_pending" || e == "slow_down" {
					return t, errors.New(e)
				}
				return t, fmt.Errorf("%s: %s", e, strVal(data, "error_description"))
			}
			return t, fmt.Errorf("poll_failed: Muse token endpoint returned status %d", status)
		}
		if e := strVal(data, "error"); e == "authorization_pending" || e == "slow_down" {
			return t, errors.New(e)
		}
		access = strVal(data, "access_token")
		if access == "" {
			if e := strVal(data, "error"); e != "" {
				return t, fmt.Errorf("%s: %s", e, strVal(data, "error_description"))
			}
			return t, errors.New("authorization_pending")
		}
	}

	key, err := museMintKey(ctx, access)
	if err != nil {
		if errors.Is(err, errMuseMintTransient) {
			pm, _ := musePendingMintGet(code)
			pm.accountToken = access
			pm.mintTries++
			if pm.createdAt.IsZero() {
				pm.createdAt = time.Now()
			}
			if pm.mintTries < musePendingMintMaxTries {
				musePendingMints.Store(code, pm)
				// devicePoll maps any "*pending*" message to the pending
				// status, so the modal keeps polling with the same device
				// code and the next poll retries only the mint.
				return t, fmt.Errorf("mint_pending: Muse key mint failed (try %d/%d): %v",
					pm.mintTries, musePendingMintMaxTries, err)
			}
			musePendingMints.Delete(code)
			return t, fmt.Errorf("Muse Code key mint failed after %d tries: %w — please try logging in again", pm.mintTries, err)
		}
		return t, err
	}
	musePendingMints.Delete(code)

	apiKey := strVal(key, "api_key")
	if apiKey == "" {
		return t, fmt.Errorf("Muse Code key mint returned no api_key")
	}
	// The stored credential is the minted Model API key, not the Meta account
	// token; the account token is kept so a future re-mint needs no new login.
	t.access = apiKey
	t.email = museAccountEmail(ctx, key, access)
	t.extra = map[string]any{
		"authMethod":       "device_code",
		"oauthAccessToken": access,
	}
	if tier := strVal(key, "subs_tier_name"); tier != "" {
		t.extra["subscriptionTier"] = tier
	}
	return t, nil
}

// museAccountEmail resolves the connection email: prefer the mint response,
// fall back to the Meta account token's JWT claims. The minted API key
// itself is not a JWT, so extractEmailFromJWT on it would never work.
func museAccountEmail(ctx context.Context, key map[string]any, accountToken string) string {
	if email := strings.ToLower(strings.TrimSpace(strVal(key, "user_email"))); email != "" {
		return email
	}
	return strings.ToLower(strings.TrimSpace(extractEmailFromJWT(accountToken)))
}

// museMintKey exchanges a Meta account token for the Model API key the chat
// transport uses. The endpoint answers 429 often enough that upstream retries
// transient failures here rather than failing the whole login.
func museMintKey(ctx context.Context, accessToken string) (map[string]any, error) {
	headers := museHeaders(map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Content-Type":  "application/json",
	})
	var data map[string]any
	var status int
	for attempt := 0; ; attempt++ {
		var err error
		data, status, err = postJSON(ctx, museKeyURL, map[string]any{"onboard": true}, headers)
		if err != nil {
			return nil, fmt.Errorf("Muse Code key mint failed (%w): %v", errMuseMintTransient, err)
		}
		if status < 500 && status != http.StatusTooManyRequests {
			break
		}
		if attempt >= museMintAttempts-1 {
			break
		}
		time.Sleep(museMintBackoff * time.Duration(attempt+1))
	}
	if status != http.StatusOK {
		err := fmt.Errorf("Muse Code key mint failed: status %d %s", status, museKeyErrorText(data))
		if status == http.StatusTooManyRequests || status >= 500 {
			return nil, fmt.Errorf("%w: %v", errMuseMintTransient, err)
		}
		return nil, err
	}
	// Meta's envelope distinguishes "subscription not activated" from "no key
	// because payment is required", and only the second carries a fix-it URL.
	if inactive, ok := data["is_subs_active"].(bool); ok && !inactive {
		return nil, fmt.Errorf("Muse Code subscription is inactive — activate it on muse.ai first")
	}
	if _, needsPayment := data["require_payment"]; needsPayment {
		return nil, fmt.Errorf("Muse Code subscription required%s", museActionURL(data))
	}
	return data, nil
}

// museKeyErrorText renders Meta's error envelope (title + detail, plus the
// action URL that fixes it) rather than a bare status code.
func museKeyErrorText(data map[string]any) string {
	title, detail, action := strVal(data, "title"), strVal(data, "detail"), museActionURL(data)
	if title == "" && detail == "" {
		return action
	}
	msg := title
	if detail != "" {
		msg += ": " + detail
	}
	return msg + action
}

// museActionURL returns the fix-it link Meta attaches to a payment or
// subscription problem, prefixed so it reads as a message tail.
func museActionURL(data map[string]any) string {
	u := strVal(data, "action_url", "require_payment_action_url")
	if u == "" {
		return ""
	}
	return " — " + u
}
