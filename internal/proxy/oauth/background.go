package oauth

import (
	"context"
	json "encoding/json/v2"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// Refresh when expiry is within 30 minutes (upstream BACKGROUND_REFRESH_LEAD_MS parity).
const (
	backgroundRefreshLead     = 30 * time.Minute
	backgroundRefreshInterval = 5 * time.Minute
	backgroundInitialDelay    = 10 * time.Second
	backgroundNormalDelay     = 1500 * time.Millisecond
	backgroundSensitiveDelay  = 12 * time.Second
	// defaultBackgroundExpiresIn is used when a token endpoint omits
	// expiresIn, so the refreshed token is not stamped as already-expired.
	defaultBackgroundExpiresIn = 3600
)

// sensitiveProviders mirrors upstream SENSITIVE_PROVIDERS: Google Cloud accounts
// get a longer gap between refreshes to avoid bursting the token endpoint.
var sensitiveProviders = map[string]bool{
	"antigravity": true,
	"gemini-cli":  true,
}

var (
	bgMu      sync.Mutex
	bgStarted bool
)

// ConnectionNeedingRefresh mirrors upstream selectConnectionsNeedingRefresh:
// an active OAuth connection with a refreshToken whose access token expires
// within the lead window.
type ConnectionNeedingRefresh struct {
	ID                   string
	Provider             string
	RefreshToken         string
	AccessToken          string
	ProviderSpecificData map[string]string
}

// SelectConnectionsNeedingRefresh is the pure selection step, exported for tests.
func SelectConnectionsNeedingRefresh(conns []*models.ProviderConnection, now time.Time) []ConnectionNeedingRefresh {
	var out []ConnectionNeedingRefresh
	for _, c := range conns {
		if c == nil {
			continue
		}
		var data struct {
			AccessToken  string         `json:"accessToken"`
			RefreshToken string         `json:"refreshToken"`
			ExpiresAt    string         `json:"expiresAt"`
			PSD          map[string]any `json:"providerSpecificData"`
		}
		if c.Data != "" {
			_ = json.Unmarshal([]byte(c.Data), &data)
		}
		if !strings.EqualFold(strings.ReplaceAll(c.AuthType, "_", ""), "oauth") {
			continue
		}
		if strings.TrimSpace(data.RefreshToken) == "" {
			continue
		}
		// A grant the provider already rejected is not retried on every tick:
		// the account stays parked until its cooldown expires, or until it is
		// re-logged in and the row is rewritten.
		if until, ok := db.ConnectionOAuthLockUntil(c.Data); ok && until.After(now) {
			continue
		}
		exp, err := time.Parse(time.RFC3339, strings.TrimSpace(data.ExpiresAt))
		if err != nil {
			continue
		}
		if exp.Sub(now) < backgroundRefreshLead {
			out = append(out, ConnectionNeedingRefresh{
				ID:                   c.ID,
				Provider:             c.Provider,
				RefreshToken:         data.RefreshToken,
				AccessToken:          data.AccessToken,
				ProviderSpecificData: StringMap(data.PSD),
			})
		}
	}
	return out
}

// StartBackgroundRefresh launches the proactive OAuth token refresh loop
// (upstream backgroundTokenRefresh.js parity). Safe to call multiple times.
// Fail-open: per-connection and per-tick failures are logged, never fatal.
func StartBackgroundRefresh(ctx context.Context, repo *db.Repo) {
	bgMu.Lock()
	if bgStarted {
		bgMu.Unlock()
		return
	}
	bgStarted = true
	bgMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backgroundInitialDelay):
		}

		ticker := time.NewTicker(backgroundRefreshInterval)
		defer ticker.Stop()

		runBackgroundRefreshTick(ctx, repo)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runBackgroundRefreshTick(ctx, repo)
			}
		}
	}()
}

// runBackgroundRefreshTick refreshes every due connection, sequentially with
// inter-account delays (upstream parity: Google accounts spaced 12s + jitter).
func runBackgroundRefreshTick(ctx context.Context, repo *db.Repo) {
	if repo == nil {
		return
	}
	conns, err := repo.GetProviderConnections("", true)
	if err != nil {
		log.Printf("[BG_TOKEN_REFRESH] load active connections failed: %v", err)
		return
	}

	due := SelectConnectionsNeedingRefresh(conns, time.Now())
	for i, c := range due {
		refreshBackgroundConnection(ctx, repo, c)
		if i < len(due)-1 {
			delay := backgroundNormalDelay
			if sensitiveProviders[c.Provider] {
				delay = backgroundSensitiveDelay
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}
}

// refreshBackgroundConnection refreshes one due connection and persists the
// result back to the row (fail-open: errors only logged).
func refreshBackgroundConnection(ctx context.Context, repo *db.Repo, c ConnectionNeedingRefresh) {
	result, err := Refresh(ctx, &Params{
		Client:               &http.Client{Timeout: 15 * time.Second},
		Provider:             c.Provider,
		RefreshToken:         c.RefreshToken,
		AccessToken:          c.AccessToken,
		ProviderSpecificData: c.ProviderSpecificData,
	})
	if err != nil || result == nil || result.AccessToken == "" {
		log.Printf("[BG_TOKEN_REFRESH] refresh failed conn=%s provider=%s: %v", c.ID, c.Provider, err)
		// Record the rejection so the tick, and the request path, stop
		// hammering the token endpoint for a grant only a re-login fixes.
		if providers.IsRefreshUnauthorized(err) {
			if until, failures, lockErr := repo.RecordConnectionOAuthFailure(c.ID, http.StatusUnauthorized, err.Error()); lockErr != nil {
				log.Printf("[BG_TOKEN_REFRESH] park failed conn=%s: %v", c.ID, lockErr)
			} else {
				log.Printf("[BG_TOKEN_REFRESH] parked conn=%s until=%s failures=%d", c.ID, until.Format(time.RFC3339), failures)
			}
		}
		return
	}

	conn, err := repo.GetProviderConnectionByID(c.ID)
	if err != nil || conn == nil {
		log.Printf("[BG_TOKEN_REFRESH] read row failed conn=%s: %v", c.ID, err)
		return
	}
	var existing map[string]any
	if conn.Data != "" {
		_ = json.Unmarshal([]byte(conn.Data), &existing)
	}
	if existing == nil {
		existing = make(map[string]any)
	}
	// A provider that omits expiresIn yields ExpiresIn=0; feeding that to
	// BuildConnectionUpdate stamps expiresAt=now, so the next tick re-selects
	// this connection forever. Fall back to a conservative window.
	refreshed := result
	if refreshed.ExpiresIn <= 0 {
		refreshed = &TokenResult{
			AccessToken:  result.AccessToken,
			RefreshToken: result.RefreshToken,
			ExpiresIn:    defaultBackgroundExpiresIn,
			Scope:        result.Scope,
			ProjectID:    result.ProjectID,
		}
	}
	for k, v := range BuildConnectionUpdate(refreshed) {
		existing[k] = v
	}
	if result.ProjectID != "" {
		existing["projectId"] = result.ProjectID
	}
	merged, err := json.Marshal(existing)
	if err != nil {
		log.Printf("[BG_TOKEN_REFRESH] marshal failed conn=%s: %v", c.ID, err)
		return
	}
	if err := repo.UpdateConnectionData(c.ID, string(merged)); err != nil {
		log.Printf("[BG_TOKEN_REFRESH] persist failed conn=%s: %v", c.ID, err)
		return
	}
	log.Printf("[BG_TOKEN_REFRESH] refreshed conn=%s provider=%s", c.ID, c.Provider)
}
