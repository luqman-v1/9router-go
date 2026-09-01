package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"9router/proxy/internal/log"
	"9router/proxy/internal/translator"
)

// AntigravityModelQuota represents live quota information for a single model on an Antigravity connection.
type AntigravityModelQuota struct {
	RemainingPercentage float64   `json:"remainingPercentage"`
	ResetAt             time.Time `json:"resetAt"`
}

// antigravityQuotaBaseURL hosts the v1internal:fetchAvailableModels discovery RPC.
// Discovery stays on PROD (parity with the Next.js reference); the daily host only
// serves chat traffic. A var (not const) so tests can point it at a local server.
var antigravityQuotaBaseURL = "https://cloudcode-pa.googleapis.com"

var (
	agQuotaMu       sync.RWMutex
	agQuotaCache    = make(map[string]map[string]AntigravityModelQuota) // connectionID -> model -> quota
	agLastRefreshAt = make(map[string]time.Time)                        // connectionID -> last refresh timestamp
	agInflightMu    sync.Mutex
	agInflight      = make(map[string]chan struct{}) // connectionID -> in-flight barrier channel

	// MinAntigravityQuotaRefreshInterval prevents hammering quota API during 429 bursts.
	MinAntigravityQuotaRefreshInterval = 30 * time.Second
)

// ClearAntigravityQuotaCache resets the in-memory cache (primarily for unit tests).
func ClearAntigravityQuotaCache() {
	agQuotaMu.Lock()
	defer agQuotaMu.Unlock()
	agQuotaCache = make(map[string]map[string]AntigravityModelQuota)
	agLastRefreshAt = make(map[string]time.Time)
}

// IsAntigravityModelBlocked reports whether connectionID has an exhausted quota for model until resetAt.
func IsAntigravityModelBlocked(connectionID, model string) bool {
	if connectionID == "" || model == "" {
		return false
	}

	agQuotaMu.RLock()
	modelsMap, ok := agQuotaCache[connectionID]
	if !ok || len(modelsMap) == 0 {
		agQuotaMu.RUnlock()
		return false
	}

	// Resolve synonym if present
	checkModels := []string{model}
	if canonical, exists := translator.AntigravityModelSynonyms[model]; exists && canonical != model {
		checkModels = append(checkModels, canonical)
	}

	now := time.Now().UTC()
	for _, m := range checkModels {
		if q, exists := modelsMap[m]; exists {
			if q.RemainingPercentage <= 0 && !q.ResetAt.IsZero() && q.ResetAt.After(now) {
				agQuotaMu.RUnlock()
				return true
			}
		}
	}
	agQuotaMu.RUnlock()

	return false
}

// RefreshAntigravityQuota fetches live quota for a connection from v1internal:fetchAvailableModels.
func RefreshAntigravityQuota(ctx context.Context, client *http.Client, connectionID, accessToken, projectID string) (map[string]AntigravityModelQuota, error) {
	if connectionID == "" || accessToken == "" {
		return nil, fmt.Errorf("missing connectionID or accessToken")
	}

	now := time.Now().UTC()

	// In-flight refresh coalescing
	agInflightMu.Lock()
	if ch, ok := agInflight[connectionID]; ok {
		agInflightMu.Unlock()
		// Wait for existing in-flight call to finish
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		agQuotaMu.RLock()
		res := agQuotaCache[connectionID]
		agQuotaMu.RUnlock()
		return res, nil
	}

	// Check 30s throttle
	agQuotaMu.RLock()
	lastRef := agLastRefreshAt[connectionID]
	cached := agQuotaCache[connectionID]
	agQuotaMu.RUnlock()

	if now.Sub(lastRef) < MinAntigravityQuotaRefreshInterval && cached != nil {
		agInflightMu.Unlock()
		return cached, nil
	}

	barrier := make(chan struct{})
	agInflight[connectionID] = barrier
	agInflightMu.Unlock()

	defer func() {
		agInflightMu.Lock()
		delete(agInflight, connectionID)
		close(barrier)
		agInflightMu.Unlock()
	}()

	agQuotaMu.Lock()
	agLastRefreshAt[connectionID] = now
	agQuotaMu.Unlock()

	quotaURL := antigravityQuotaBaseURL + "/v1internal:fetchAvailableModels"

	reqBodyMap := map[string]any{}
	if projectID != "" {
		reqBodyMap["project"] = projectID
	}
	bodyBytes, _ := json.Marshal(reqBodyMap)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, quotaURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create quota request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity/1.0.0")
	req.Header.Set("X-Client-Name", "antigravity")

	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Warn("ag_quota", "refresh failed", "connection", connectionID[:min(8, len(connectionID))], "error", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		log.Warn("ag_quota", "quota API auth error", "connection", connectionID[:min(8, len(connectionID))], "status", resp.StatusCode)
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		log.Warn("ag_quota", "quota API non-200", "connection", connectionID[:min(8, len(connectionID))], "status", resp.StatusCode, "body", string(respBody))
		return nil, fmt.Errorf("quota API returned status %d", resp.StatusCode)
	}

	var data struct {
		Models map[string]struct {
			IsInternal bool `json:"isInternal"`
			QuotaInfo  *struct {
				RemainingFraction float64 `json:"remainingFraction"`
				ResetTime         string  `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}

	if err := json.UnmarshalRead(resp.Body, &data); err != nil {
		return nil, fmt.Errorf("decode quota response: %w", err)
	}

	quotas := make(map[string]AntigravityModelQuota)
	for modelKey, modelData := range data.Models {
		if modelData.QuotaInfo == nil || modelData.IsInternal {
			continue
		}
		var resetAt time.Time
		if modelData.QuotaInfo.ResetTime != "" {
			if t, err := time.Parse(time.RFC3339, modelData.QuotaInfo.ResetTime); err == nil {
				resetAt = t.UTC()
			}
		}
		quotas[modelKey] = AntigravityModelQuota{
			RemainingPercentage: modelData.QuotaInfo.RemainingFraction * 100,
			ResetAt:             resetAt,
		}
	}

	agQuotaMu.Lock()
	agQuotaCache[connectionID] = quotas
	agQuotaMu.Unlock()

	return quotas, nil
}

// HandleAntigravityQuotaError handles Antigravity 409/429 errors by refreshing live quota and returning model resetAt.
func HandleAntigravityQuotaError(ctx context.Context, client *http.Client, connectionID string, status int, model, accessToken, projectID string) *time.Time {
	if status != http.StatusConflict && status != http.StatusTooManyRequests {
		return nil
	}

	shortConn := connectionID
	if len(shortConn) > 8 {
		shortConn = shortConn[:8]
	}
	log.Info("ag_quota", "refreshing quota on error", "connection", shortConn, "status", status, "model", model)

	quotas, err := RefreshAntigravityQuota(ctx, client, connectionID, accessToken, projectID)
	if err != nil || len(quotas) == 0 {
		return nil
	}

	checkModels := []string{model}
	if canonical, exists := translator.AntigravityModelSynonyms[model]; exists && canonical != model {
		checkModels = append(checkModels, canonical)
	}

	now := time.Now().UTC()
	for _, m := range checkModels {
		if q, ok := quotas[m]; ok {
			if q.RemainingPercentage <= 0 && !q.ResetAt.IsZero() && q.ResetAt.After(now) {
				log.Warn("ag_quota", "quota exhausted; CACHE_BLOCK until reset", "connection", shortConn, "model", m, "resetAt", q.ResetAt.Format(time.RFC3339))
				res := q.ResetAt
				return &res
			}
		}
	}

	return nil
}
