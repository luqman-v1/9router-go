package executor

import (
	"context"
	json "encoding/json/v2"
	"os"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
)

// The background rotation of a Gemini Web session.
//
// The `__Secure-1PSIDTS` cookie the site rotates every few minutes is what
// carries a session into the model registry, so a credential that is not
// renewed falls back to one model within minutes and the operator has to
// paste the cookie again. This loop renews it.
//
// The loop is the same shape as the OAuth token refresher's: a guarded start,
// an initial delay, a ticker, and a fail-open tick. Fail-open means a
// rotation that fails is logged and skipped, never fatal — a third party's
// refusal must not take the gateway down.
//
// Reference: azrinaldi/gemini-stealth-bridge internal/cookies/rotate.go, and
// the reference's own background worker that drives it.

const (
	// geminiWebRotationInitialDelay is how long the loop waits before its
	// first pass. The cookie outlives it, so there is nothing to rush.
	geminiWebRotationInitialDelay = 30 * time.Second
	// geminiWebRotationTickInterval is how often the loop looks for
	// connections that are due. The gap between two renewals of one account
	// is set by the site and is far longer; this is only how often the loop
	// notices that a connection is due.
	geminiWebRotationTickInterval = time.Minute
	// geminiWebRotationGrace is how long after a renewal the account is left
	// alone. The site answers a second call within the same minute with HTTP
	// 429, so the account has to be left alone for a moment even when the
	// site asks for a shorter gap.
	geminiWebRotationGrace = 2 * time.Minute
)

var (
	geminiWebRotationMu      sync.Mutex
	geminiWebRotationStarted bool
	// geminiWebRotationDue holds the time each account is next due for a
	// renewal, so the loop knows which connections are due without asking the
	// site.
	geminiWebRotationDue = map[string]time.Time{}
)

// StartGeminiWebRotation launches the loop that renews the session cookies of
// every Gemini Web connection.
//
// Safe to call more than once: the second call does nothing.
//
// The loop can be turned off with GEMINI_WEB_ROTATION=off. Turning it off is
// the right choice when the credential belongs to a browser that is still
// running: the browser rotates the same cookie, and the two rotations cancel
// each other out. A credential copied from a closed Incognito window is the
// right one for this loop, because nothing else is rotating it.
func StartGeminiWebRotation(ctx context.Context, repo *db.Repo) {
	if !geminiWebRotationEnabled() {
		log.Info("gemini-web", "the session renewal is off", "setting", geminiWebRotationEnv)
		return
	}
	geminiWebRotationMu.Lock()
	if geminiWebRotationStarted {
		geminiWebRotationMu.Unlock()
		return
	}
	geminiWebRotationStarted = true
	geminiWebRotationMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(geminiWebRotationInitialDelay):
		}
		ticker := time.NewTicker(geminiWebRotationTickInterval)
		defer ticker.Stop()
		geminiWebRotationTick(ctx, repo)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				geminiWebRotationTick(ctx, repo)
			}
		}
	}()
}

// geminiWebRotationEnv is the setting that turns the renewal off.
const geminiWebRotationEnv = "GEMINI_WEB_ROTATION"

// geminiWebRotationEnabled reports whether the renewal runs.
//
// Only an explicit "off" turns it off. Anything else — including an unset
// variable — leaves it on, because a credential that is never renewed falls
// back to one model within minutes, which is the failure the loop exists to
// prevent.
func geminiWebRotationEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(geminiWebRotationEnv))) {
	case "off", "0", "false", "no", "disabled":
		return false
	default:
		return true
	}
}

// geminiWebRotationTick renews every Gemini Web connection that is due.
func geminiWebRotationTick(ctx context.Context, repo *db.Repo) {
	if repo == nil {
		return
	}
	conns, err := repo.GetProviderConnections("gemini-web", true)
	if err != nil {
		log.Warn("gemini-web", "rotation could not load the connections", "error", err)
		return
	}
	live := make(map[string]bool, len(conns))
	for _, conn := range conns {
		if conn != nil {
			live[conn.ID] = true
		}
	}
	for _, stale := range geminiWebRotationDueKeys(live) {
		geminiWebRotationForget(stale)
	}
	for _, conn := range conns {
		if ctx.Err() != nil {
			return
		}
		geminiWebRotateConnection(ctx, repo, conn)
	}
}

// geminiWebRotateConnection renews one connection's session cookie.
//
// A connection with no credential, or one whose credential carries no
// session timestamp, is left alone: there is nothing to renew, and the
// anonymous path needs no renewal.
func geminiWebRotateConnection(ctx context.Context, repo *db.Repo, conn *models.ProviderConnection) {
	if conn == nil {
		return
	}
	raw := map[string]any{}
	if conn.Data != "" {
		if err := json.Unmarshal([]byte(conn.Data), &raw); err != nil {
			log.Warn("gemini-web", "rotation could not read the connection's data", "conn", conn.ID, "error", err)
			return
		}
	}
	credential, _ := raw["apiKey"].(string)
	cookie := GeminiWebCookieHeader(credential)
	if cookie == "" || !GeminiWebHasSessionTimestamp(cookie) {
		return
	}
	if !geminiWebRotationIsDue(conn.ID) {
		return
	}

	session := geminiWebSessionForCredential(cookie)
	result := geminiWebRotate(ctx, session, cookie)
	geminiWebRotationNote(conn.ID, result.NextInterval)
	if !result.Rotated {
		log.Warn("gemini-web", "the rotation renewed nothing", "conn", conn.ID, "status", result.Status)
		return
	}
	if result.Cookie == cookie {
		return
	}
	raw["apiKey"] = result.Cookie
	encoded, err := json.Marshal(raw)
	if err != nil {
		log.Warn("gemini-web", "rotation could not encode the new credential", "conn", conn.ID, "error", err)
		return
	}
	if err := repo.UpdateConnectionData(conn.ID, string(encoded)); err != nil {
		log.Warn("gemini-web", "rotation could not store the new credential", "conn", conn.ID, "error", err)
		return
	}
	// The session is keyed by the credential it was built from, so the new
	// credential is a different session. The old one is dropped here rather
	// than left in the map: every renewal would otherwise leave one behind.
	geminiWebEvictSession(cookie)
	log.Info("gemini-web", "renewed the session cookie", "conn", conn.ID, "status", result.Status)
}

// geminiWebRotationIsDue reports whether an account is due for a renewal.
//
// The gap is the one the site asked for, and the grace is added on top of
// it: the site answers a second call within the same minute with HTTP 429, so
// an account is left alone for a moment after its renewal even when the site
// asks for a shorter gap.
func geminiWebRotationIsDue(connectionID string) bool {
	geminiWebRotationMu.Lock()
	defer geminiWebRotationMu.Unlock()
	next, seen := geminiWebRotationDue[connectionID]
	if !seen {
		return true
	}
	return time.Now().After(next)
}

// geminiWebRotationNote records when an account is next due for a renewal.
func geminiWebRotationNote(connectionID string, interval time.Duration) {
	if interval <= 0 {
		interval = geminiWebRotateDefaultInterval
	}
	geminiWebRotationMu.Lock()
	defer geminiWebRotationMu.Unlock()
	geminiWebRotationDue[connectionID] = time.Now().Add(interval + geminiWebRotationGrace)
}

// geminiWebRotationForget drops an account's schedule, so a connection that
// is deleted or deactivated stops being renewed.
func geminiWebRotationForget(connectionID string) {
	geminiWebRotationMu.Lock()
	defer geminiWebRotationMu.Unlock()
	delete(geminiWebRotationDue, connectionID)
}

// geminiWebRotationDueKeys lists the accounts the loop is keeping a schedule
// for. It exists so the map cannot grow without bound: a connection that is
// deleted leaves its entry behind otherwise.
func geminiWebRotationDueKeys(live map[string]bool) []string {
	geminiWebRotationMu.Lock()
	defer geminiWebRotationMu.Unlock()
	stale := make([]string, 0)
	for id := range geminiWebRotationDue {
		if !live[id] {
			stale = append(stale, id)
		}
	}
	return stale
}
