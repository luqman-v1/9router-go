package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/oauth"
)

// Credential refresh for GET /api/usage/{connectionId}, ported from upstream
// src/app/api/usage/[connectionId]/route.js (refreshAndUpdateCredentials + GET).
//
// Without it a connection whose access token aged out answers the quota API
// with 401/403 while chat traffic — which refreshes on its own path — keeps
// working. That is the report in #78 item 4: "quota API rejected the current
// token. Chat may still work." Upstream refreshes before the read and retries
// once when the provider reports an auth-expired message; this file ports both
// halves.
//
// Two deliberate differences from upstream:
//
//   - Upstream returns HTTP 401 with `Credential refresh failed: …` when the
//     pre-read refresh throws. The dashboard's request() helper treats a 401 as
//     a session logout, so a recoverable refresh failure would bounce the user
//     to the login page over a quota panel. The failure is surfaced as a quota
//     message on a 200 instead: the reading is empty either way, the page
//     renders, and the row states why.
//   - Upstream re-reads the row before refreshing because OpenAI rotates the
//     refresh token on every call and reusing a stale snapshot revokes the
//     session. The handler reads the row once per request and persists the
//     rotated token below, which is what makes the next read correct.

// usageAuthExpiredPatterns mirrors upstream AUTH_EXPIRED_PATTERNS. A provider
// that reports an auth failure as a message rather than a status (Kiro answers
// a 401 with "Kiro quota API authentication expired. …") is only detectable
// through its wording.
var usageAuthExpiredPatterns = []string{"expired", "authentication", "unauthorized", "401", "re-authorize"}

// isAuthExpiredUsageMessage ports upstream isAuthExpiredMessage.
func isAuthExpiredUsageMessage(res usageResult) bool {
	if res.message == "" {
		return false
	}
	msg := strings.ToLower(res.message)
	for _, p := range usageAuthExpiredPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// usageCredentials is the mutable view of a connection's stored blob that a
// quota read and its refresh operate on.
type usageCredentials struct {
	raw      map[string]any
	provider string
}

// accessToken reports the token a quota read must present. Kiro and other
// providers that store an OAuth token in apiKey are covered by the fallback.
func (c usageCredentials) accessToken() string {
	if s, _ := c.raw["accessToken"].(string); s != "" {
		return s
	}
	s, _ := c.raw["apiKey"].(string)
	return s
}

func (c usageCredentials) refreshToken() string {
	s, _ := c.raw["refreshToken"].(string)
	return s
}

// needsRefresh ports upstream needsRefresh. An absent or unparseable expiresAt
// means "no expiry to trust" and answers false, so the expiry window is not the
// thing that rejects a Kiro connection whose awsDate expiresAt is in the past
// but whose token is still live.
func (c usageCredentials) needsRefresh() bool {
	expiresAt, _ := c.raw["expiresAt"].(string)
	if expiresAt = strings.TrimSpace(expiresAt); expiresAt == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(ts.Add(-connectionRefreshLead))
}

// isOAuth reports whether this connection carries refreshable credentials.
// Upstream gates its refresh on authType === "oauth"; an api_key provider has
// no refresh token to exchange.
func isUsageRefreshable(conn *models.ProviderConnection, raw map[string]any) bool {
	if conn.AuthType != "oauth" {
		return false
	}
	s, _ := raw["refreshToken"].(string)
	return strings.TrimSpace(s) != ""
}

// refresh exchanges the refresh token and persists the rotated credentials.
// A nil result with a nil error means "nothing to refresh with" — no registered
// refresher, or a refused exchange — which callers treat as "keep the stored
// token and read with it", matching upstream's fallback when refreshCredentials
// returns nothing but an access token remains.
func (h *DashboardHandler) refreshUsageCredentials(ctx context.Context, conn *models.ProviderConnection, creds usageCredentials, client *http.Client) (*oauth.TokenResult, error) {
	if client == nil {
		client = &http.Client{Timeout: usageHTTPTimeout}
	}
	psd, _ := creds.raw["providerSpecificData"].(map[string]any)
	result, err := oauth.Refresh(ctx, &oauth.Params{
		Client:               client,
		Provider:             providers.ResolveAlias(conn.Provider),
		RefreshToken:         creds.refreshToken(),
		AccessToken:          creds.accessToken(),
		ProviderSpecificData: oauth.StringMap(psd),
	})
	if err != nil {
		return nil, fmt.Errorf("refresh %s credentials: %w", conn.Provider, err)
	}
	if result == nil || result.AccessToken == "" {
		return nil, nil
	}
	h.persistUsageTokens(conn, creds.raw, result)
	return result, nil
}

// persistUsageTokens stores the refreshed token material back on the row.
func (h *DashboardHandler) persistUsageTokens(conn *models.ProviderConnection, raw map[string]any, result *oauth.TokenResult) {
	// Kiro writes its OAuth token into both fields on login (HandleKiroAPIKey,
	// HandleKiroImport), so apiKey mirrors accessToken there. Rotating only
	// accessToken would leave the stale copy in place for every reader that takes
	// apiKey. An api_key connection whose key differs from the OAuth token keeps
	// its own key — that one is not ours to overwrite.
	stored, _ := raw["accessToken"].(string)
	mirrored, _ := raw["apiKey"].(string)
	if mirrored != "" && mirrored == stored {
		raw["apiKey"] = result.AccessToken
	}
	if result.AccessToken != "" {
		raw["accessToken"] = result.AccessToken
	}
	if result.RefreshToken != "" {
		raw["refreshToken"] = result.RefreshToken
	}
	if result.ExpiresIn > 0 {
		raw["expiresIn"] = result.ExpiresIn
		raw["expiresAt"] = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	if result.ProjectID != "" {
		raw["projectId"] = result.ProjectID
	}
	if encoded, err := json.Marshal(raw); err == nil {
		_ = h.Repo.UpdateConnectionData(conn.ID, string(encoded))
	}
}
