package chat

import (
	"context"
	"fmt"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// resolveACPCredentials returns the OAuth token pair an agy-acp turn should
// write into its isolated account dir. Two modes are supported:
//
//   - reference: providerSpecificData.sourceConnection names an existing
//     antigravity connection; the token is read from that connection (and
//     refreshed on expiry) so one account can back many ACP connections.
//   - copy: the agy-acp connection itself carries accessToken/refreshToken.
func (h *ChatHandler) resolveACPCredentials(ctx context.Context, connData map[string]any, connectionID string) (string, string, time.Time, error) {
	source := ""
	if v, ok := connData["sourceConnection"]; ok {
		source, _ = v.(string)
	}
	if source == "" {
		// Also accept the reference nested in providerSpecificData, which is
		// where the dashboard stores provider-specific config.
		if psd, ok := connData["providerSpecificData"].(map[string]any); ok {
			source, _ = psd["sourceConnection"].(string)
		}
	}
	if source == "" {
		// No reference: use this connection's own token. The request path
		// only carries the fields ConnectionData parses (no refreshToken),
		// so read the stored row by id — that also picks up tokens a
		// background refresh has since rotated.
		return h.ownACPCredentials(connData, connectionID)
	}

	if h.Repo == nil || h.Repo.RawDB() == nil {
		return "", "", time.Time{}, fmt.Errorf("agy-acp: no database for referenced connection")
	}
	conn, err := h.Repo.GetProviderConnectionByID(source)
	if err != nil || conn == nil {
		return "", "", time.Time{}, fmt.Errorf("agy-acp: referenced antigravity connection %q not found", source)
	}
	if conn.Provider != "antigravity" {
		return "", "", time.Time{}, fmt.Errorf("agy-acp: source connection %q is provider %q, want antigravity", source, conn.Provider)
	}

	var data map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &data); err != nil {
		return "", "", time.Time{}, fmt.Errorf("agy-acp: decode source connection: %w", err)
	}
	oauth := providers.ParseOAuthConnection(data)
	if oauth == nil || oauth.RefreshToken == "" {
		return "", "", time.Time{}, fmt.Errorf("agy-acp: source connection has no refresh token; re-login antigravity")
	}

	access := oauth.AccessToken
	if oauth.IsExpired() || access == "" {
		// The shared refresh helper rotates the single-use grant under the
		// per-connection lock and writes the new token back to the source row.
		refreshed, _, rerr := h.forceRefreshOAuthToken(source)
		if rerr != nil {
			log.Warn("agy-acp", "source token refresh failed", "conn", source, "error", rerr)
			return "", "", time.Time{}, fmt.Errorf("agy-acp: refresh source token: %w", rerr)
		}
		if refreshed != "" {
			access = refreshed
		}
	}
	if access == "" {
		access = handlerutil.GetString(data, "apiKey")
	}
	expiry, _ := time.Parse(time.RFC3339, oauth.ExpiresAt)
	return access, oauth.RefreshToken, expiry, nil
}

func localACPCredentials(connData map[string]any) (string, string, time.Time, error) {
	oauth := providers.ParseOAuthConnection(connData)
	if oauth == nil || oauth.AccessToken == "" || oauth.RefreshToken == "" {
		return "", "", time.Time{}, fmt.Errorf("%w", errNoACPCredentials)
	}
	expiry, _ := time.Parse(time.RFC3339, oauth.ExpiresAt)
	return oauth.AccessToken, oauth.RefreshToken, expiry, nil
}

// ownACPCredentials resolves the connection's own token pair. It first tries
// the map handed down the request path, then falls back to the stored row:
// ConnectionData drops refreshToken, so a Google-login connection would
// otherwise always fail copy mode with errNoACPCredentials.
func (h *ChatHandler) ownACPCredentials(connData map[string]any, connectionID string) (string, string, time.Time, error) {
	if access, refresh, expiry, err := localACPCredentials(connData); err == nil {
		return access, refresh, expiry, nil
	}
	if connectionID == "" || h.Repo == nil || h.Repo.RawDB() == nil {
		return "", "", time.Time{}, fmt.Errorf("%w", errNoACPCredentials)
	}
	conn, err := h.Repo.GetProviderConnectionByID(connectionID)
	if err != nil || conn == nil {
		return "", "", time.Time{}, fmt.Errorf("%w", errNoACPCredentials)
	}
	var stored map[string]any
	if err := json.Unmarshal([]byte(conn.Data), &stored); err != nil {
		return "", "", time.Time{}, fmt.Errorf("agy-acp: decode own connection data: %w", err)
	}
	return localACPCredentials(stored)
}

var errNoACPCredentials = fmt.Errorf("agy-acp: connection carries no accessToken/refreshToken and no sourceConnection")
