package oauth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Muse Code subscription login (upstream src/lib/oauth/providers/muse.js):
// a Meta account runs the device-code grant against auth.meta.com, and the
// granted account token is then exchanged for the Model API key the chat
// transport actually uses. The device code is one-shot and Meta issues no
// refresh token, so a failed mint means the user signs in again rather than
// the gateway retrying a grant.

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
)

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
func museStart() (map[string]any, error) {
	form := url.Values{"client_id": {museClientID}}
	data, status, err := postForm(museDeviceCodeURL, form, museHeaders(map[string]string{
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
	data["session"] = map[string]any{}
	return data, nil
}

// musePoll trades the approved device code for a Model API key. Meta answers
// authorization_pending while the user is still in the browser, which the
// shared poll loop turns back into a "pending" state for the modal.
func musePoll(code string) (deviceTokens, error) {
	var t deviceTokens
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {code},
		"client_id":   {museClientID},
	}
	data, _, err := postForm(museTokenURL, form, museHeaders(map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}))
	if err != nil {
		return t, fmt.Errorf("poll_failed: %w", err)
	}
	if e := strVal(data, "error"); e == "authorization_pending" || e == "slow_down" {
		return t, errors.New(e)
	}
	access := strVal(data, "access_token")
	if access == "" {
		if e := strVal(data, "error"); e != "" {
			return t, fmt.Errorf("%s: %s", e, strVal(data, "error_description"))
		}
		return t, errors.New("authorization_pending")
	}

	key, err := museMintKey(access)
	if err != nil {
		return t, err
	}
	apiKey := strVal(key, "api_key")
	if apiKey == "" {
		return t, fmt.Errorf("Muse Code key mint returned no api_key")
	}
	// The stored credential is the minted Model API key, not the Meta account
	// token; the account token is kept so a future re-mint needs no new login.
	t.access = apiKey
	t.email = strings.ToLower(strings.TrimSpace(strVal(key, "user_email")))
	t.extra = map[string]any{
		"authMethod":       "device_code",
		"oauthAccessToken": access,
	}
	if tier := strVal(key, "subs_tier_name"); tier != "" {
		t.extra["subscriptionTier"] = tier
	}
	return t, nil
}

// museMintKey exchanges a Meta account token for the Model API key the chat
// transport uses. The endpoint answers 429 often enough that upstream retries
// transient failures here rather than failing the whole login.
func museMintKey(accessToken string) (map[string]any, error) {
	headers := museHeaders(map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Content-Type":  "application/json",
	})
	var data map[string]any
	var status int
	for attempt := 0; ; attempt++ {
		var err error
		data, status, err = postJSON(museKeyURL, map[string]any{"onboard": true}, headers)
		if err != nil {
			return nil, fmt.Errorf("Muse Code key mint failed: %w", err)
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
		return nil, fmt.Errorf("Muse Code key mint failed: status %d %s", status, museKeyErrorText(data))
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
