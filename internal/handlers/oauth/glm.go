package oauth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// --- glm: Z.AI GLM Coding (dual-auth) ZCode CLI polling OAuth ---
//
// Port of upstream src/lib/oauth/providers/glm.js (068ce87d). Like kimi, this
// is dual-auth: an API key may be pasted, or the Z.ai account signed in through
// the ZCode CLI polling protocol — no PKCE, no local callback:
//
//  1. POST https://zcode.z.ai/api/v1/oauth/cli/init
//     Authorization: Bearer <hex poll token>
//     {"provider":"zai"}
//     → { code: 0, data: { authorize_url, flow_id, poll_interval_sec, expires_at } }
//  2. Browser opens authorize_url; user signs in with the Z.ai account
//  3. GET https://zcode.z.ai/api/v1/oauth/cli/poll/<flow_id>
//     Authorization: Bearer <poll token>
//     → { code: 0, data: { status: "pending" } } until
//     { code: 0, data: { status: "ready", token, user, zai: { access_token } } }
//  4. access_token (Z.AI OAuth token) → POST https://api.z.ai/api/auth/z/login
//     {"token": ...} → { data: { access_token } } (platform business JWT)
//  5. Business JWT → coding-plan API key via getCustomerInfo → api_keys
//     list/create("zcode-api-key") → copy → "apiKey.secretKey"
//
// The coding-plan key is the long-lived model credential, stored by
// saveDeviceConnection in both apiKey and accessToken like the other
// OAuth-minted keys. Z.ai has no refresh grant — expiry means re-login, the
// same as the official CLI — so the connection stores no refreshToken and the
// refresher has no glm entry.
var (
	glmCLIInitURL        = "https://zcode.z.ai/api/v1/oauth/cli/init"
	glmCLIPollURL        = "https://zcode.z.ai/api/v1/oauth/cli/poll"
	glmBusinessLoginURL  = "https://api.z.ai/api/auth/z/login"
	glmBizAPIBaseURL     = "https://api.z.ai"
	glmPlanAPIKeyName    = "zcode-api-key"
	glmPollDefaultSource = 300
)

func glmStart() (map[string]any, error) {
	// The site parses the bearer poll token as a hex flow identifier: the CLI
	// and upstream send 32 random bytes hex-encoded, and anything else is
	// answered with HTTP 400 code 3004 "invalid_flow" (confirmed against the
	// live endpoint).
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("ZCode OAuth init failed: %w", err)
	}
	pollToken := hex.EncodeToString(raw)
	data, status, err := postJSON(glmCLIInitURL, map[string]any{"provider": "zai"},
		map[string]string{"Authorization": "Bearer " + pollToken})
	if err != nil {
		return nil, fmt.Errorf("ZCode OAuth init failed: %w", err)
	}
	if status != http.StatusOK || !glmSuccessCode(strVal(data, "code")) {
		return nil, fmt.Errorf("ZCode OAuth init failed (HTTP %d): %s", status, strVal(data, "msg", "error"))
	}
	inner, _ := data["data"].(map[string]any)
	flowID := strVal(inner, "flow_id")
	authorizeURL := strVal(inner, "authorize_url")
	if flowID == "" || authorizeURL == "" {
		return nil, fmt.Errorf("ZCode OAuth init response missing flow_id/authorize_url")
	}
	interval := positiveIntOr(inner, "poll_interval_sec", 3)
	expiresIn := floatOr(inner, "expires_in", 0)
	return map[string]any{
		"device_code":               flowID,
		"user_code":                 "",
		"verification_uri":          authorizeURL,
		"verification_uri_complete": authorizeURL,
		"expires_in":                glmRelativeSeconds(floatOr(inner, "expires_at", 0), time.Now()),
		"interval":                  interval,
		"session": map[string]any{
			"zcodePollToken": pollToken,
			"expiresIn":      expiresIn,
		},
	}, nil
}

// glmRelativeSeconds converts an absolute epoch (s or ms) into the seconds
// remaining from now, defaulting to pollDefaultSource when absent/invalid —
// upstream surfaces a relative deadline for the UI (relativeSeconds in glm.js).
func glmRelativeSeconds(epoch float64, now time.Time) int {
	if epoch <= 0 {
		return glmPollDefaultSource
	}
	ms := epoch
	if ms < 1e12 {
		ms *= 1000
	}
	remaining := time.UnixMilli(int64(ms)).Sub(now)
	if remaining <= 0 {
		return glmPollDefaultSource
	}
	return int(remaining.Seconds())
}

// glmPoll exchanges the Z.AI OAuth token for a business JWT and finally mints
// the long-lived coding-plan API key. Mirrors upstream pollToken +
// resolveCodingPlanApiKey; the provider-specific tokens ride along in
// deviceTokens.extra exactly as upstream's mapTokens stores them.
func glmPoll(session map[string]any, code string) (deviceTokens, error) {
	var t deviceTokens
	pollToken, _ := session["zcodePollToken"].(string)
	if pollToken == "" {
		return t, fmt.Errorf("access_denied: missing ZCode poll token — restart the login flow")
	}
	if code == "" {
		return t, fmt.Errorf("authorization_pending")
	}

	// GET {cliPollUrl}/{flow_id} — Bearer pollToken
	u := glmCLIPollURL + "/" + url.PathEscape(code)
	data, status, err := getJSON(u, map[string]string{"Authorization": "Bearer " + pollToken})
	if err != nil {
		return t, fmt.Errorf("poll_failed: %w", err)
	}
	if status == http.StatusNotFound {
		// flow_id unknown: the init ttl expired before the user signed in.
		return t, fmt.Errorf("authorization_pending")
	}
	if status != http.StatusOK {
		return t, fmt.Errorf("poll_failed (HTTP %d)", status)
	}
	if !glmSuccessCode(strVal(data, "code")) {
		return t, fmt.Errorf("access_denied: %s", strVal(data, "msg", "error"))
	}
	inner, _ := data["data"].(map[string]any)
	pollState := strVal(inner, "status")
	if pollState == "pending" {
		return t, fmt.Errorf("authorization_pending")
	}
	if pollState == "failed" {
		return t, fmt.Errorf("access_denied: ZCode authorization failed or was cancelled")
	}
	if pollState != "ready" {
		return t, fmt.Errorf("authorization_pending: unknown status %q", pollState)
	}

	// ready nests the ZAI OAuth tokens under data[providerId] ("zai") — upstream
	// parseReadyData (apps/zcode-cli cli-oauth.ts). Fall back to top-level fields
	// for resilience against payload drift.
	zaiData, _ := inner["zai"].(map[string]any)
	zaiAccessToken := firstNonEmptyStr(strVal(zaiData, "access_token"), strVal(zaiData, "accessToken"), strVal(inner, "accessToken"), strVal(inner, "access_token"))
	if zaiAccessToken == "" {
		return t, fmt.Errorf("access_denied: ZCode poll response missing access token")
	}

	planKey, businessToken, err := glmResolveCodingPlanKey(zaiAccessToken)
	if err != nil {
		return t, err
	}

	t.access = planKey
	t.refresh = ""
	t.extra = map[string]any{
		"apiKey":           planKey,
		"authMethod":       "cli_poll",
		"zcodeJwtToken":    strVal(inner, "token"),
		"zaiBusinessToken": businessToken,
	}
	if user, _ := inner["user"].(map[string]any); user != nil {
		t.name = strVal(user, "name")
		t.email = strVal(user, "email")
		if uid := strVal(user, "user_id"); uid != "" {
			t.extra["userId"] = uid
		}
		if refresh := strVal(zaiData, "refresh_token"); refresh != "" {
			t.extra["zaiRefreshToken"] = refresh
		}
	}
	return t, nil
}

// glmResolveCodingPlanKey mirrors upstream resolveCodingPlanApiKey: business
// login → getCustomerInfo → default org/project → api_keys list/create("zcode-api-key")
// → copy → "apiKey.secretKey". The business JWT is returned for
// providerSpecificData (quota/usage debugging), bounded like upstream.
func glmResolveCodingPlanKey(zaiAccessToken string) (planKey, businessToken string, err error) {
	businessToken, err = glmExchangeBusinessToken(zaiAccessToken)
	if err != nil {
		return "", "", err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + businessToken,
		"Content-Type":  "application/json",
	}

	customerInfo, status, gerr := getJSON(glmBizAPIBaseURL+"/api/biz/customer/getCustomerInfo", headers)
	if gerr != nil || status != http.StatusOK || !glmSuccessCode(strVal(customerInfo, "code")) {
		return "", "", fmt.Errorf("getCustomerInfo for Z.ai failed (HTTP %d): %w", status, gerr)
	}
	// fetchBusinessJson unwraps payload.data before pickOrgAndProject runs, so the
	// customer info object (with organizations) is what we pick from.
	customerData, _ := customerInfo["data"].(map[string]any)
	if customerData == nil {
		customerData = customerInfo
	}
	location, lok := glmPickOrgAndProject(customerData)
	if !lok {
		return "", "", fmt.Errorf("unable to resolve Z.ai organization and project for the coding plan")
	}

	keysURL := fmt.Sprintf("%s/api/biz/v1/organization/%s/projects/%s/api_keys",
		glmBizAPIBaseURL, url.PathEscape(location.organizationID), url.PathEscape(location.projectID))
	keys, status, gerr := getJSON(keysURL, headers)
	if gerr != nil || status != http.StatusOK {
		return "", "", fmt.Errorf("listing api_keys for Z.ai failed (HTTP %d): %w", status, gerr)
	}
	apiKey := ""
	if list, ok := keys["data"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok && strVal(m, "name") == glmPlanAPIKeyName {
				apiKey = strVal(m, "apiKey")
				break
			}
		}
	}
	if apiKey == "" {
		// No coding-plan key yet: create it. The single-key reply nests under
		// data like the rest of the business API: { code: 200, data: { apiKey, name } }.
		created, cstatus, cerr := postJSON(keysURL, map[string]any{"name": glmPlanAPIKeyName}, headers)
		if cerr != nil || cstatus != http.StatusOK {
			return "", "", fmt.Errorf("creating api_key for Z.ai failed (HTTP %d): %w", cstatus, cerr)
		}
		createdInner, _ := created["data"].(map[string]any)
		if createdInner != nil {
			apiKey = strVal(createdInner, "apiKey")
		}
	}
	if strings.TrimSpace(apiKey) == "" {
		return "", "", fmt.Errorf("api_keys response from Z.ai is missing apiKey")
	}

	copied, cstatus, cerr := getJSON(keysURL+"/copy/"+url.PathEscape(apiKey), headers)
	if cerr != nil || cstatus != http.StatusOK {
		return "", "", fmt.Errorf("copying api key for Z.ai failed (HTTP %d): %w", cstatus, cerr)
	}
	copiedInner, _ := copied["data"].(map[string]any)
	if copiedInner == nil {
		copiedInner = copied
	}
	secret := strVal(copiedInner, "secretKey")
	if strings.TrimSpace(secret) == "" {
		return "", "", fmt.Errorf("api key copy response from Z.ai is missing secretKey")
	}
	return apiKey + "." + secret, businessToken, nil
}

// glmExchangeBusinessToken mirrors upstream exchangeBusinessToken: the Z.AI
// OAuth token is spent once for a platform business JWT.
func glmExchangeBusinessToken(zaiAccessToken string) (string, error) {
	data, status, err := postJSON(glmBusinessLoginURL, map[string]any{"token": zaiAccessToken},
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return "", fmt.Errorf("business login for Z.ai failed: %w", err)
	}
	if status != http.StatusOK || !glmSuccessCode(strVal(data, "code")) {
		return "", fmt.Errorf("business login for Z.ai failed (HTTP %d): %s", status, strVal(data, "msg", "error"))
	}
	inner, _ := data["data"].(map[string]any)
	token := strVal(inner, "access_token")
	if token == "" {
		token = strVal(inner, "accessToken")
	}
	if token == "" {
		return "", fmt.Errorf("business login response from Z.ai is missing access_token")
	}
	return token, nil
}

// glmPickOrgAndProject mirrors upstream pickOrgAndProject: prefer the org named
// "默认机构"/"default" and the non-team project named "默认项目"/"default"
// (projectType "2" = team), falling back to the first entries.
func glmPickOrgAndProject(customerInfo map[string]any) (struct{ organizationID, projectID string }, bool) {
	var out struct{ organizationID, projectID string }
	orgs, _ := customerInfo["organizations"].([]any)
	type cand struct {
		orgID, projectID string
	}
	var first cand
	var defaultOrg cand
	for _, o := range orgs {
		om, _ := o.(map[string]any)
		orgID := strVal(om, "organizationId")
		if orgID == "" {
			continue
		}
		projects, _ := om["projects"].([]any)
		for _, p := range projects {
			pm, _ := p.(map[string]any)
			if strVal(pm, "projectType") == "2" {
				continue // team project
			}
			projectID := strVal(pm, "projectId")
			if projectID == "" {
				continue
			}
			c := cand{orgID, projectID}
			if first.orgID == "" {
				first = c
			}
			if glmIsDefaultName(strVal(om, "organizationName")) && glmIsDefaultName(strVal(pm, "projectName")) {
				defaultOrg = c
				break
			}
		}
		if defaultOrg.orgID != "" {
			break
		}
	}
	chosen := defaultOrg
	if chosen.orgID == "" {
		chosen = first
	}
	if chosen.orgID == "" {
		return out, false
	}
	out.organizationID = chosen.orgID
	out.projectID = chosen.projectID
	return out, true
}

// glmIsDefaultName mirrors upstream isDefaultName.
func glmIsDefaultName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(n, "默认机构") || strings.Contains(n, "默认项目") || n == "default"
}

// glmSuccessCode reports success on the business envelope {code,msg,data},
// where 0/200 (or absent) means success — mirrors upstream isSuccessCode.
func glmSuccessCode(code string) bool {
	switch code {
	case "", "0", "200":
		return true
	}
	return false
}

// floatOr reads a numeric field, returning fallback when absent/invalid.
func floatOr(m map[string]any, key string, fallback float64) float64 {
	if f, ok := m[key].(float64); ok && f == f {
		return f
	}
	if s, ok := m[key].(string); ok {
		var f float64
		if _, err := fmt.Sscanf(s, "%g", &f); err == nil {
			return f
		}
	}
	return fallback
}
