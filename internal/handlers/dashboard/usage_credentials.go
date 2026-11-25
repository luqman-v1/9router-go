package dashboard

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
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
//     session. That re-read is ported below (adoptStoredUsageCredentials): the
//     caller has already read the row once, and a writer that rotated the
//     token since — the 5-minute background refresher, an auto-ping — would
//     otherwise be answered with a token OpenAI has already revoked, which
//     logs the account out permanently.

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
	// adoptedRefreshToken, when set, is token material read back from the row
	// immediately before the refresh; it wins over the snapshot's own value.
	adoptedRefreshToken string
	raw                 map[string]any
	provider            string
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

// refreshTokenFor reports the refresh token to spend: the one stored on the
// row when that differs from the caller's snapshot, the snapshot otherwise.
func (c usageCredentials) refreshTokenFor() string {
	if adopted := c.adoptedRefreshToken; adopted != "" {
		return adopted
	}
	return c.refreshToken()
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
	return time.Now().After(ts.Add(-providers.RefreshLead(c.provider)))
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

// canAttemptRefresh reports whether this connection is worth spending a refresh
// call on at all. oauth.Refresh can only succeed for a provider with a
// registered refresher, and qoder has none — upstream's executor answers
// `async refreshCredentials() { return null; }` — so asking it is a guaranteed
// failure that logs on every panel open.
func canAttemptRefresh(conn *models.ProviderConnection, raw map[string]any) bool {
	return isUsageRefreshable(conn, raw) && oauth.Get(providers.ResolveAlias(conn.Provider)) != nil
}

// markDeadGrant records a refresh the provider refused in a way only a re-login
// can fix, so the dashboard shows the account as needing re-authorization
// instead of leaving it to surface as a warning nobody reads.
//
// Upstream's route throws on these (open-sse/services/tokenRefresh.js
// isUnrecoverableRefreshError), which surfaces as an HTTP 500 on the quota
// panel. Parking the account matches the chat path's existing treatment of a
// revoked grant and keeps the reading available, so the row still shows what
// the account has left while the badge says to re-authorize.
func (h *DashboardHandler) markDeadGrant(conn *models.ProviderConnection, refreshErr error) {
	if h.Repo == nil || !providers.IsRefreshGrantDead(refreshErr) {
		return
	}
	status := 0
	var refreshErrType *providers.OAuthRefreshError
	if errors.As(refreshErr, &refreshErrType) {
		status = refreshErrType.Status
	}
	until, failures, err := h.Repo.RecordConnectionOAuthFailure(conn.ID, status, refreshErr)
	if err != nil {
		log.Warn("usage", "record dead oauth grant failed", "provider", conn.Provider, "conn", conn.ID, "error", err)
		return
	}
	log.Warn("usage", "oauth grant dead, re-authorize this account",
		"provider", conn.Provider, "conn", conn.ID,
		"until", until.Format(time.RFC3339), "failures", failures)
}

// adoptStoredUsageCredentials re-reads the connection row and, when the stored
// refresh token differs from the caller's snapshot, carries the stored one so
// the refresh below spends the token the provider last handed out.
//
// OpenAI rotates the refresh token on every exchange and revokes the entire
// session when a rotated token is presented again, so a snapshot taken before
// a concurrent refresh would log the account out rather than renew it. A read
// that fails, or a row that carries no token, leaves the snapshot in place:
// this narrows what the refresh spends, it does not decide whether to refresh.
func (h *DashboardHandler) adoptStoredUsageCredentials(ctx context.Context, conn *models.ProviderConnection, creds usageCredentials) usageCredentials {
	if h.Repo == nil || conn == nil || conn.ID == "" {
		return creds
	}
	latest, err := h.Repo.GetProviderConnectionByID(conn.ID)
	if err != nil || latest == nil {
		return creds
	}
	stored, _ := latestStoredCredentials(latest)
	if stored.refreshToken() == "" || stored.refreshToken() == creds.refreshToken() {
		return creds
	}
	log.Warn("usage", "adopting rotated refresh token from stored row",
		"provider", conn.Provider, "conn", conn.ID)
	creds.adoptedRefreshToken = stored.refreshToken()
	return creds
}

// latestStoredCredentials decodes the token material a freshly read row
// carries. A blob that will not decode yields empty credentials, which the
// caller reads as "nothing newer stored".
func latestStoredCredentials(latest *models.ProviderConnection) (usageCredentials, error) {
	creds := usageCredentials{provider: latest.Provider}
	if latest.Data == "" {
		return creds, nil
	}
	err := json.Unmarshal([]byte(latest.Data), &creds.raw)
	return creds, err
}

// usageRefreshOutcome is what one guarded refresh did, so the caller can tell a
// skipped refresh (no refresher registered) from a real failure.
type usageRefreshOutcome struct {
	attempted bool
	refreshed bool
	err       error
}

// runUsageRefresh performs one guarded refresh. It returns err only for a
// failure the caller should surface; a provider with no refresher is not an
// error, it is simply nothing to do.
//
// An account that already carries a dead-grant park (testStatus
// reauth_required) is skipped: re-spending the single-use refresh token
// would answer invalid_grant again and bump the failure counter behind a
// request the caller must not make. The caller keeps the stored access
// token and reads with it, so the panel still renders while the badge
// says to re-authorize.
func (h *DashboardHandler) runUsageRefresh(ctx context.Context, conn *models.ProviderConnection, creds usageCredentials) usageRefreshOutcome {
	if !canAttemptRefresh(conn, creds.raw) {
		return usageRefreshOutcome{}
	}
	if h.Repo != nil {
		if latest, err := h.Repo.GetProviderConnectionByID(conn.ID); err == nil && latest != nil {
			if db.ConnectionRequiresReauth(latest.Data) {
				return usageRefreshOutcome{}
			}
		}
	}
	creds = h.adoptStoredUsageCredentials(ctx, conn, creds)
	result, err := h.refreshUsageCredentials(ctx, conn, creds, nil)
	if err != nil {
		h.markDeadGrant(conn, err)
		return usageRefreshOutcome{attempted: true, err: err}
	}
	return usageRefreshOutcome{attempted: true, refreshed: result != nil}
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
		RefreshToken:         creds.refreshTokenFor(),
		AccessToken:          creds.accessToken(),
		ProviderSpecificData: oauth.StringMap(psd),
		Force:                true,
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
	oauth.SyncMirroredAPIKey(raw, conn.Provider, stored, result.AccessToken)
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

// =============================================================================
// Claude quota auto-ping catch-up tracking
// Ports upstream decolua/9router PR #5531 / Issue #4001:
// "fix(quota): catch Claude resets missed between auto-ping ticks"
// =============================================================================

// ClaudeAutoPingCatchupState tracks observed reset deadlines and failure cooldowns
// for Claude auto-ping reactive cycles.
type ClaudeAutoPingCatchupState struct {
	mu           sync.Mutex
	ResetCache   map[string]string    // key: "claude:<connId>", value: resetAt (RFC3339)
	FailureCache map[string]time.Time // key: "claude:<connId>", value: failedAt
}

// GlobalClaudeAutoPingState is the process-wide state for Claude auto-ping catchup.
var GlobalClaudeAutoPingState = NewClaudeAutoPingCatchupState()

// NewClaudeAutoPingCatchupState creates an initialized state tracker.
func NewClaudeAutoPingCatchupState() *ClaudeAutoPingCatchupState {
	return &ClaudeAutoPingCatchupState{
		ResetCache:   make(map[string]string),
		FailureCache: make(map[string]time.Time),
	}
}

// Reset clears all cached reset deadlines and failures (for testing).
func (s *ClaudeAutoPingCatchupState) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResetCache = make(map[string]string)
	s.FailureCache = make(map[string]time.Time)
}

// NormalizeResetKey rounds resetAt timestamp down to the nearest minute,
// matching upstream normalizeResetKey.
func NormalizeResetKey(resetAt string) string {
	t, err := time.Parse(time.RFC3339, resetAt)
	if err != nil {
		return resetAt
	}
	return t.Truncate(time.Minute).UTC().Format(time.RFC3339)
}

// ClaudeAutoPingLeadDuration matches upstream QUOTA_AUTOPING_CONFIG.pingLeadMs (5 seconds).
const ClaudeAutoPingLeadDuration = 5 * time.Second

// ClaudeAutoPingFailureCooldown matches upstream QUOTA_AUTOPING_CONFIG.failureCooldownMs (15 minutes).
const ClaudeAutoPingFailureCooldown = 15 * time.Minute

// ShouldPingForClaudeReset checks if current time has reached resetAt - pingLeadDuration.
func ShouldPingForClaudeReset(resetAt string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, resetAt)
	if err != nil {
		return false
	}
	return !now.Before(t.Add(-ClaudeAutoPingLeadDuration))
}

// ShouldSkipAfterClaudePingFailure checks if connection is in failure cooldown.
func (s *ClaudeAutoPingCatchupState) ShouldSkipAfterFailure(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	failedAt, ok := s.FailureCache[key]
	if !ok {
		return false
	}
	return now.Sub(failedAt) < ClaudeAutoPingFailureCooldown
}

// ClaudeAutoPingTickPlan describes the decision made for a connection on this tick.
type ClaudeAutoPingTickPlan struct {
	ShouldPing         bool
	EffectiveResetAt   string
	EffectiveResetKey  string
	CatchingUpReset    bool
	NewlyObservedReset string
	Key                string
}

// PlanClaudeAutoPing evaluates whether Claude auto-ping should fire, handling
// catch-up of due, unpinged deadlines missed between ticks.
func (s *ClaudeAutoPingCatchupState) PlanClaudeAutoPing(
	connID string,
	lastPingedResetKey string,
	newlyObservedResetAt string,
	now time.Time,
) ClaudeAutoPingTickPlan {
	key := "claude:" + connID
	s.mu.Lock()
	defer s.mu.Unlock()

	if failedAt, ok := s.FailureCache[key]; ok && now.Sub(failedAt) < ClaudeAutoPingFailureCooldown {
		return ClaudeAutoPingTickPlan{Key: key}
	}

	cachedReset := s.ResetCache[key]
	resetAt := newlyObservedResetAt
	if resetAt != "" {
		s.ResetCache[key] = resetAt
	}

	resetKey := ""
	if resetAt != "" {
		resetKey = NormalizeResetKey(resetAt)
	}

	// Catch-up check:
	// A tick may miss Claude's short lead window and observe the next reset.
	// Catch up the unpinged cached deadline, without marking that next cycle
	// as pinged. Keep the fresh reset in resetCache for subsequent ticks.
	if cachedReset != "" &&
		lastPingedResetKey != NormalizeResetKey(cachedReset) &&
		ShouldPingForClaudeReset(cachedReset, now) {
		resetAt = cachedReset
		resetKey = NormalizeResetKey(cachedReset)
	}

	if resetAt == "" {
		return ClaudeAutoPingTickPlan{Key: key}
	}

	if !ShouldPingForClaudeReset(resetAt, now) {
		return ClaudeAutoPingTickPlan{Key: key}
	}

	if lastPingedResetKey == resetKey {
		return ClaudeAutoPingTickPlan{Key: key}
	}

	catchingUp := resetAt != newlyObservedResetAt
	if catchingUp {
		// Retain the caught-up deadline in cache while pending
		s.ResetCache[key] = resetAt
	}

	return ClaudeAutoPingTickPlan{
		ShouldPing:         true,
		EffectiveResetAt:   resetAt,
		EffectiveResetKey:  resetKey,
		CatchingUpReset:    catchingUp,
		NewlyObservedReset: newlyObservedResetAt,
		Key:                key,
	}
}

// RecordPingSuccess marks ping succeeded and updates resetCache to the newly observed reset if caught up.
func (s *ClaudeAutoPingCatchupState) RecordPingSuccess(plan ClaudeAutoPingTickPlan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.FailureCache, plan.Key)
	if plan.CatchingUpReset && plan.NewlyObservedReset != "" {
		s.ResetCache[plan.Key] = plan.NewlyObservedReset
	}
}

// RecordPingFailure records ping failure and enters failure cooldown.
// Retains the caught-up deadline in resetCache so subsequent retries keep catching it up.
func (s *ClaudeAutoPingCatchupState) RecordPingFailure(plan ClaudeAutoPingTickPlan, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FailureCache[plan.Key] = now
	if plan.CatchingUpReset {
		s.ResetCache[plan.Key] = plan.EffectiveResetAt
	}
}
