package chat

import (
	"os"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

// forceFallbackEnv is the deployment-level override upstream reads from the
// process environment (decolua/9router PR #130, .env.example). It forces the
// strategy on without a dashboard write, so a deployment can keep serving
// traffic through a temporary provider outage before anyone opens the UI.
const forceFallbackEnv = "FORCE_FALLBACK_ON_ALL_UNAVAILABLE"

// forceFallbackEnabled reports whether the operator asked the gateway to keep
// serving requests from accounts that are still cooling down. The dashboard
// toggle writes settings.forceFallback; the env var alone is enough (upstream
// parity), and neither is on unless asked for.
func forceFallbackEnabled(settings *db.SettingsData) bool {
	if settings != nil && settings.ForceFallback {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv(forceFallbackEnv)), "true")
}

// forceMinCooldownConnection returns the account whose cooldown expires first
// among the candidates the selector just rejected, so a provider outage costs a
// client turn instead of an error (upstream decolua/9router PR #130: sort the
// forced candidates by rateLimitedUntil and take the head).
//
// Only accounts that really are in account cooldown are eligible. A candidate
// skipped for a per-model lock or a quota cache carries no cooldown to shorten,
// and forcing a request onto it would discard the very lock that removed it —
// so those stay out and the caller keeps reporting the ordinary failure.
func (h *ChatHandler) forceMinCooldownConnection(
	provider string,
	conns []*models.ProviderConnection,
	excludeSet map[string]bool,
	model string,
	settings *db.SettingsData,
) (*models.ProviderConnection, time.Time, bool) {
	if !forceFallbackEnabled(settings) {
		return nil, time.Time{}, false
	}

	now := time.Now()
	var best *models.ProviderConnection
	var bestUntil time.Time
	for _, c := range conns {
		if c == nil || excludeSet[c.ID] || !codexAccountServesModel(c, model) {
			continue
		}
		until, ok := db.ConnectionBlockedUntil(c.Data)
		if !ok || !until.After(now) {
			continue
		}
		// Ties keep list order, which is the strategy's priority order.
		if best == nil || until.Before(bestUntil) {
			best, bestUntil = c, until
		}
	}
	if best == nil {
		return nil, time.Time{}, false
	}
	return best, bestUntil, true
}