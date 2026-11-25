package chat

import (
	"net/http"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// parkRejectedOAuthAccount cools a connection down after the provider rejected
// its OAuth grant in a terminal way (401, or 400 with a dead-grant body such as
// invalid_grant / refresh_token_reused — OmniRoute #14917), so the next request
// skips the account instead of spending another refresh call on a grant only a
// re-login can fix. It reports whether the account was parked; a refresh that
// failed any other way is left to the caller, because a 5xx or a transport
// error is no evidence the credential is gone.
func (h *ChatHandler) parkRejectedOAuthAccount(connectionID string, refreshErr error) bool {
	if connectionID == "" || h.Repo == nil || !providers.IsRefreshGrantDead(refreshErr) {
		return false
	}
	until, failures, err := h.Repo.RecordConnectionOAuthFailure(connectionID, http.StatusUnauthorized, refreshErr)
	if err != nil {
		log.Warn("oauth", "park rejected account failed", "conn", connectionID, "error", err)
		return false
	}
	log.Warn("oauth", "oauth grant rejected, account parked",
		"conn", connectionID, "until", until.Format(time.RFC3339), "failures", failures)
	return true
}

// clearOAuthAccountPark releases the cooldown once the account refreshed
// again, so one rejection does not keep the next one out of rotation at the
// longer backoff.
func (h *ChatHandler) clearOAuthAccountPark(connectionID string) {
	if connectionID == "" || h.Repo == nil {
		return
	}
	if err := h.Repo.ClearConnectionOAuthLock(connectionID); err != nil {
		log.Warn("oauth", "clear account park failed", "conn", connectionID, "error", err)
	}
}

// connectionParkedForReauth reports whether a connection is already parked
// for a dead OAuth grant (testStatus reauth_required). A forced refresh
// against such an account re-spends a single-use token that has already
// been refused, so callers that would refresh reactively skip it.
func connectionParkedForReauth(h *ChatHandler, connectionID string) bool {
	if h == nil || h.Repo == nil || connectionID == "" {
		return false
	}
	conn, err := h.Repo.GetProviderConnectionByID(connectionID)
	if err != nil || conn == nil {
		return false
	}
	return db.ConnectionRequiresReauth(conn.Data)
}
