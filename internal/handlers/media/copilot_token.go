package media

import (
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// copilotTokenExchangeURL is the endpoint that trades a GitHub OAuth access
// token for the short-lived Copilot token. A var so tests can point it at an
// httptest server; every other caller leaves it alone.
var copilotTokenExchangeURL = "https://api.github.com/copilot_internal/v2/token"

// copilotTokenSkew is how long before the recorded expiry a token is already
// treated as stale. The token is minted upstream and then spends the rest of
// its life in transit and in the request queue, so refreshing exactly at the
// recorded second still sends a token that expires mid-flight.
const copilotTokenSkew = 2 * time.Minute

// copilotTokenExpiry reads the stored expiry in any of the shapes a row can
// carry it. The Copilot endpoint answers `expires_at` as a unix second count,
// which a string-only reader leaves behind as an empty string; the login path
// now stores the number as a string, but rows written before that (and any row
// hand-edited in the dashboard) can still hold a number or an RFC 3339 stamp.
// An unreadable expiry is reported as "unknown" rather than "expired": sending
// a token of unknown age is what the 401 path is for.
func copilotTokenExpiry(raw any) (time.Time, bool) {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}, false
		}
		if secs, err := strconv.ParseInt(s, 10, 64); err == nil {
			return unixOrMillis(secs)
		}
		if ts, err := time.Parse(time.RFC3339, s); err == nil {
			return ts, true
		}
		return time.Time{}, false
	case float64:
		return unixOrMillis(int64(v))
	case int64:
		return unixOrMillis(v)
	case int:
		return unixOrMillis(int64(v))
	}
	return time.Time{}, false
}

// unixOrMillis accepts both spellings of an absolute instant: seconds (what the
// Copilot endpoint sends) and milliseconds (what a JS port that multiplied by
// 1000 writes). Anything past the year 5000 in seconds is read as milliseconds.
func unixOrMillis(n int64) (time.Time, bool) {
	if n <= 0 {
		return time.Time{}, false
	}
	if n > 1e11 {
		return time.UnixMilli(n), true
	}
	return time.Unix(n, 0), true
}

// storedCopilotToken returns the connection's short-lived token and whether it
// can still be sent as-is. `known` reports whether the row carried an expiry at
// all, so the caller can tell "fresh" apart from "no idea, send it and let the
// upstream answer".
func storedCopilotToken(data map[string]any, now time.Time) (token string, fresh bool, known bool) {
	if data == nil {
		return "", false, false
	}
	raw, ok := data["copilotToken"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", false, false
	}
	token = raw
	expiry, ok := copilotTokenExpiry(data["copilotTokenExpiresAt"])
	if !ok || expiry.IsZero() {
		return token, false, false
	}
	return token, now.Add(copilotTokenSkew).Before(expiry), true
}

// exchangeCopilotToken trades the connection's OAuth access token for a fresh
// short-lived Copilot token. The bearer here is the OAuth credential, not the
// Copilot one: this call is what mints the latter.
func (h *MediaHandler) exchangeCopilotToken(ctx context.Context, oauthToken string) (string, time.Time, error) {
	if strings.TrimSpace(oauthToken) == "" {
		return "", time.Time{}, fmt.Errorf("no oauth token to exchange")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenExchangeURL, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+oauthToken)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// Upstream asks for this exact agent; the endpoint gates on it.
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.7")

	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("copilot token exchange: status %d", resp.StatusCode)
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", time.Time{}, fmt.Errorf("copilot token exchange: invalid JSON")
	}
	token, _ := data["token"].(string)
	if strings.TrimSpace(token) == "" {
		return "", time.Time{}, fmt.Errorf("copilot token exchange: empty token")
	}
	expiry, _ := copilotTokenExpiry(data["expires_at"])
	return token, expiry, nil
}

// refreshCopilotToken exchanges and persists in one step, then brings the
// in-memory connection data up to date so the caller can use the token on the
// request it is already building. Persistence failures are logged, not
// returned: the token itself is still good for this request, and refusing to
// use a working credential because the write failed would be worse than
// re-exchanging on the next one.
func (h *MediaHandler) refreshCopilotToken(ctx context.Context, conn *models.ProviderConnection, connData *chat.ConnectionData, oauthToken string) (string, error) {
	token, expiry, err := h.exchangeCopilotToken(ctx, oauthToken)
	if err != nil {
		return "", err
	}
	expiryStr := ""
	if !expiry.IsZero() {
		expiryStr = strconv.FormatInt(expiry.Unix(), 10)
	}
	if connData != nil {
		if connData.ProviderSpecificData == nil {
			connData.ProviderSpecificData = map[string]any{}
		}
		connData.ProviderSpecificData["copilotToken"] = token
		if expiryStr != "" {
			connData.ProviderSpecificData["copilotTokenExpiresAt"] = expiryStr
		}
	}
	if conn != nil && h.Repo != nil {
		if err := h.persistCopilotToken(conn.ID, token, expiryStr); err != nil {
			// Not fatal: the caller asked for a usable token, not a durable one.
			log.Warn("media", "persist copilot token failed", "conn", conn.ID, "error", err)
		}
	}
	return token, nil
}

// persistCopilotToken merges the two keys into the stored payload and writes it
// back through UpdateConnectionData, the writer the token-refresh loop uses: a
// full-row update would write back the name/priority/isActive this request read
// and revert a toggle the operator made in the meantime.
//
// Both keys go inside providerSpecificData because that is the object the
// connection reader hydrates into ConnectionData; the device-login writer put
// them there too (device.go nests t.extra under that key), and a top-level copy
// is parsed away as an unknown field and never seen again.
func (h *MediaHandler) persistCopilotToken(connID, token, expiry string) error {
	row, err := h.Repo.GetProviderConnectionByID(connID)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("connection %s not found", connID)
	}
	data := map[string]any{}
	if strings.TrimSpace(row.Data) != "" {
		if err := json.Unmarshal([]byte(row.Data), &data); err != nil {
			return fmt.Errorf("connection %s: unreadable data: %w", connID, err)
		}
	}
	if data == nil {
		data = map[string]any{}
	}
	psd, _ := data["providerSpecificData"].(map[string]any)
	if psd == nil {
		psd = map[string]any{}
	}
	psd["copilotToken"] = token
	if expiry != "" {
		psd["copilotTokenExpiresAt"] = expiry
	}
	data["providerSpecificData"] = psd
	// An older writer could have left the pair at the top level, where the
	// reader never looks; drop the dead copy so the row has one answer.
	delete(data, "copilotToken")
	delete(data, "copilotTokenExpiresAt")
	merged, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return h.Repo.UpdateConnectionData(connID, string(merged))
}

// copilotTokenForRequest returns the bearer the embeddings lane should send in
// place of the connection's own credential, or "" when this connection has no
// short-lived token at all.
//
// A stale token is re-exchanged before the request rather than after the
// refusal: the expiry is known, so waiting for the 401 spends a round trip and
// a usage-log entry on an answer the gateway can already predict.
func (h *MediaHandler) copilotTokenForRequest(ctx context.Context, conn *models.ProviderConnection, connData *chat.ConnectionData, oauthToken string) string {
	if connData == nil {
		return ""
	}
	token, fresh, known := storedCopilotToken(connData.ProviderSpecificData, time.Now())
	if token == "" {
		return ""
	}
	if fresh || !known {
		// Unknown age: send it. The 401 path still refreshes once.
		return token
	}
	refreshed, err := h.refreshCopilotToken(ctx, conn, connData, oauthToken)
	if err != nil {
		log.Warn("media", "copilot token refresh failed", "conn", conn.ID, "error", err)
		return token
	}
	return refreshed
}
