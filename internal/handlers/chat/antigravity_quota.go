package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
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

// AntigravityWeeklyQuota holds parsed weekly quota values for a model family.
type AntigravityWeeklyQuota struct {
	Used                int       `json:"used"`
	Total               int       `json:"total"`
	RemainingPercentage float64   `json:"remainingPercentage"`
	ResetAt             time.Time `json:"resetAt"`
	DisplayName         string    `json:"displayName"`
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

	// Strike-breaker for optimistic quota (PR #3684): when quota reports >0 but we keep getting 429,
	// after 3 consecutive 429s within 60s, treat as exhausted for 15m.
	agStrikeMu            sync.Mutex
	agStrikes             = make(map[string][]time.Time) // key = connectionID|model -> 429 timestamps
	agStrikeBlocks        = make(map[string]time.Time)   // key -> blockUntil (15m strike block)
	agStrikeWindow        = 60 * time.Second
	agStrikeThreshold     = 3
	agStrikeBlockDuration = 15 * time.Minute

	// Explicit quota-error markers (parity with decolua/9router#4197): only
	// 429s carrying one of these count toward the strike-breaker. Generic or
	// content-triggered 429s must not synthesize 15m blocks while quota reads
	// optimistic. NOTE: bare "RESOURCE_EXHAUSTED" is deliberately NOT a
	// marker — both false-bucket and real-quota 429s carry it; real quota
	// errors additionally carry QUOTA_EXHAUSTED / "Individual quota reached".
	agQuotaErrorMarkers = []string{
		"RATE_LIMIT_EXCEEDED",
		"QUOTA_EXHAUSTED",
		"Individual quota reached",
	}
)
var (
	agWeeklyMu    sync.RWMutex
	agWeeklyCache = make(map[string]map[string]AntigravityWeeklyQuota) // cacheKey -> weekly quotas
	agWeeklyTTL   = 3 * time.Minute
	agWeeklyAt    = make(map[string]time.Time)
)

// applyActiveStrikeBlocks re-asserts active 15m strike blocks into quotas so a fresh
// optimistic quota reading cannot resurrect a pair we just circuit-broke (#3714).
func applyActiveStrikeBlocks(connectionID string, quotas map[string]AntigravityModelQuota) map[string]AntigravityModelQuota {
	now := time.Now().UTC()
	agStrikeMu.Lock()
	defer agStrikeMu.Unlock()
	for key, blockUntil := range agStrikeBlocks {
		if !strings.HasPrefix(key, connectionID+"|") {
			continue
		}
		if blockUntil.After(now) {
			model := strings.TrimPrefix(key, connectionID+"|")
			quotas[model] = AntigravityModelQuota{
				RemainingPercentage: 0,
				ResetAt:             blockUntil,
			}
		} else {
			delete(agStrikeBlocks, key)
			delete(agStrikes, key)
		}
	}
	return quotas
}

// ClearAntigravityQuotaCache resets the in-memory cache (primarily for unit tests).
func ClearAntigravityQuotaCache() {
	agQuotaMu.Lock()
	defer agQuotaMu.Unlock()
	agQuotaCache = make(map[string]map[string]AntigravityModelQuota)
	agLastRefreshAt = make(map[string]time.Time)

	agWeeklyMu.Lock()
	agWeeklyCache = make(map[string]map[string]AntigravityWeeklyQuota)
	agWeeklyAt = make(map[string]time.Time)
	agWeeklyMu.Unlock()
}

// BlockAntigravityModelUntil caches an exhausted model quota in memory until resetAt.
func BlockAntigravityModelUntil(connectionID, model string, resetAt time.Time) {
	if connectionID == "" || model == "" || resetAt.IsZero() {
		return
	}
	checkModels := []string{model}
	if canonical, exists := translator.AntigravityModelSynonyms[model]; exists && canonical != model {
		checkModels = append(checkModels, canonical)
	}
	if lockKey := canonicalLockModel("antigravity", model); lockKey != "" && lockKey != model {
		checkModels = append(checkModels, lockKey)
	}

	agQuotaMu.Lock()
	defer agQuotaMu.Unlock()
	if _, ok := agQuotaCache[connectionID]; !ok {
		agQuotaCache[connectionID] = make(map[string]AntigravityModelQuota)
	}
	for _, m := range checkModels {
		agQuotaCache[connectionID][m] = AntigravityModelQuota{
			RemainingPercentage: 0,
			ResetAt:             resetAt,
		}
	}
	shortConn := connectionID
	if len(shortConn) > 8 {
		shortConn = shortConn[:8]
	}
	log.Warn("ag_quota", "model locked until reset", "connection", shortConn, "model", model, "resetAt", resetAt.Format(time.RFC3339))
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

	// Check family weekly quota
	if strings.HasPrefix(model, "gemini-") {
		if wq, exists := modelsMap["gemini_weekly"]; exists {
			if wq.RemainingPercentage <= 0 && !wq.ResetAt.IsZero() && wq.ResetAt.After(now) {
				agQuotaMu.RUnlock()
				return true
			}
		}
	} else if strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "gpt-") {
		if wq, exists := modelsMap["claude_gpt_weekly"]; exists {
			if wq.RemainingPercentage <= 0 && !wq.ResetAt.IsZero() && wq.ResetAt.After(now) {
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

	// If no model quotas found (e.g. Free-tier accounts have weekly quotas only),
	// best-effort fetch weekly quota summary (parity with Next.js open-sse/services/usage/google.js #3892)
	if len(quotas) == 0 {
		if weekly, err := FetchAntigravityWeeklyQuota(ctx, client, accessToken, projectID); err == nil && len(weekly) > 0 {
			for k, wq := range weekly {
				quotas[k] = AntigravityModelQuota{
					RemainingPercentage: wq.RemainingPercentage,
					ResetAt:             wq.ResetAt,
				}
			}
		}
	}

	// Re-assert active strike blocks so optimistic quota cannot resurrect blocked pair
	quotas = applyActiveStrikeBlocks(connectionID, quotas)

	agQuotaMu.Lock()
	agQuotaCache[connectionID] = quotas
	agQuotaMu.Unlock()

	return quotas, nil
}

// isExplicitQuotaError reports whether an upstream error message explicitly
// indicates quota/rate limiting (parity with decolua/9router#4197).
func isExplicitQuotaError(errorMessage string) bool {
	for _, marker := range agQuotaErrorMarkers {
		if strings.Contains(errorMessage, marker) {
			return true
		}
	}
	return false
}

// HandleAntigravityQuotaError handles Antigravity 409/429 errors by refreshing live quota and returning model resetAt.
// errorMessage is the raw upstream error body/text: 429s only feed the
// optimistic strike-breaker when it carries an explicit quota marker;
// generic/content-triggered 429s return nil without striking (#4197 parity).
func HandleAntigravityQuotaError(ctx context.Context, client *http.Client, connectionID string, status int, model, accessToken, projectID, errorMessage string) *time.Time {
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
			// Optimistic quota strike-breaker (PR #3684, gated by #4197):
			// remaining >0 but still 429. Generic/content-triggered 429s
			// (no explicit quota marker) must not contribute strikes.
			if status == http.StatusTooManyRequests && !isExplicitQuotaError(errorMessage) && q.RemainingPercentage > 0 {
				return nil
			}
			if q.RemainingPercentage > 0 && isExplicitQuotaError(errorMessage) {
				key := connectionID + "|" + m
				agStrikeMu.Lock()
				// Prune strikes outside window
				cutoff := now.Add(-agStrikeWindow)
				strikes := agStrikes[key]
				var kept []time.Time
				for _, t := range strikes {
					if t.After(cutoff) {
						kept = append(kept, t)
					}
				}
				kept = append(kept, now)
				agStrikes[key] = kept
				shouldBlock := len(kept) >= agStrikeThreshold
				if shouldBlock {
					blockUntil := now.Add(agStrikeBlockDuration)
					agStrikeBlocks[key] = blockUntil
					agStrikeMu.Unlock()
					// Store blocked quota so IsAntigravityModelBlocked sees it even after optimistic refresh
					agQuotaMu.Lock()
					if _, ok := agQuotaCache[connectionID]; !ok {
						agQuotaCache[connectionID] = make(map[string]AntigravityModelQuota)
					}
					agQuotaCache[connectionID][m] = AntigravityModelQuota{
						RemainingPercentage: 0,
						ResetAt:             blockUntil,
					}
					agQuotaMu.Unlock()
					log.Warn("ag_quota", "optimistic quota strike-break: 3x429 within 60s while quota>0, CACHE_BLOCK 15m", "connection", shortConn, "model", m, "blockUntil", blockUntil.Format(time.RFC3339))
					return &blockUntil
				}
				agStrikeMu.Unlock()
			}
		}
	}

	return nil
}

// ClearAntigravityStrikes clears strike history for a connection/model (called on success).
func ClearAntigravityStrikes(connectionID, model string) {
	if connectionID == "" || model == "" {
		return
	}
	agStrikeMu.Lock()
	defer agStrikeMu.Unlock()
	delete(agStrikes, connectionID+"|"+model)
	delete(agStrikeBlocks, connectionID+"|"+model)
	if canonical, exists := translator.AntigravityModelSynonyms[model]; exists {
		delete(agStrikes, connectionID+"|"+canonical)
		delete(agStrikeBlocks, connectionID+"|"+canonical)
	}
}

// ParseWeeklyQuotaSummary parses retrieveUserQuotaSummary response JSON into family quotas.
func ParseWeeklyQuotaSummary(raw []byte) map[string]AntigravityWeeklyQuota {
	var payload struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Buckets     []struct {
				BucketID          string  `json:"bucketId"`
				DisplayName       string  `json:"displayName"`
				Disabled          bool    `json:"disabled"`
				RemainingFraction float64 `json:"remainingFraction"`
				ResetTime         string  `json:"resetTime"`
			} `json:"buckets"`
		} `json:"groups"`
		QuotaSummary *struct {
			Groups []struct {
				DisplayName string `json:"displayName"`
				Buckets     []struct {
					BucketID          string  `json:"bucketId"`
					DisplayName       string  `json:"displayName"`
					Disabled          bool    `json:"disabled"`
					RemainingFraction float64 `json:"remainingFraction"`
					ResetTime         string  `json:"resetTime"`
				} `json:"buckets"`
			} `json:"groups"`
		} `json:"quotaSummary"`
	}

	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}

	groups := payload.Groups
	if len(groups) == 0 && payload.QuotaSummary != nil {
		groups = payload.QuotaSummary.Groups
	}
	if len(groups) == 0 {
		return nil
	}

	result := make(map[string]AntigravityWeeklyQuota)
	for _, g := range groups {
		gName := g.DisplayName
		isGemini := strings.Contains(strings.ToLower(gName), "gemini")
		isClaudeGPT := strings.Contains(strings.ToLower(gName), "claude") || strings.Contains(strings.ToLower(gName), "gpt")
		if !isGemini && !isClaudeGPT {
			continue
		}

		for _, b := range g.Buckets {
			bText := strings.ToLower(b.BucketID + " " + b.DisplayName)
			if !strings.Contains(bText, "weekly") || b.Disabled {
				continue
			}
			frac := b.RemainingFraction
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			total := 1000
			remaining := int(math.Round(float64(total) * frac))
			used := total - remaining
			if used < 0 {
				used = 0
			}

			var resetAt time.Time
			if b.ResetTime != "" {
				if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil {
					resetAt = t.UTC()
				}
			}

			key := "gemini_weekly"
			dName := "Gemini (Weekly)"
			if isClaudeGPT {
				key = "claude_gpt_weekly"
				dName = "Claude & GPT (Weekly)"
			}

			if _, exists := result[key]; !exists {
				result[key] = AntigravityWeeklyQuota{
					Used:                used,
					Total:               total,
					RemainingPercentage: frac * 100,
					ResetAt:             resetAt,
					DisplayName:         dName,
				}
			}
			break
		}
	}
	return result
}

// FetchAntigravityWeeklyQuota fetches the weekly quota summary from Google's retrieveUserQuotaSummary endpoint.
func FetchAntigravityWeeklyQuota(ctx context.Context, client *http.Client, accessToken, projectID string) (map[string]AntigravityWeeklyQuota, error) {
	cacheKey := accessToken + "::" + projectID
	now := time.Now().UTC()

	agWeeklyMu.RLock()
	cached, ok := agWeeklyCache[cacheKey]
	cachedAt := agWeeklyAt[cacheKey]
	agWeeklyMu.RUnlock()
	if ok && now.Sub(cachedAt) < agWeeklyTTL {
		return cached, nil
	}

	url := antigravityQuotaBaseURL + "/v1internal:retrieveUserQuotaSummary"
	reqBody := map[string]any{}
	if projectID != "" {
		reqBody["project"] = projectID
	}
	bodyBytes, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "antigravity/1.0.0")
	req.Header.Set("X-Client-Name", "antigravity")
	req.Header.Set("X-Client-Version", "0.1.0")

	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("retrieveUserQuotaSummary returned %d", resp.StatusCode)
	}

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	res := ParseWeeklyQuotaSummary(respBytes)
	if len(res) > 0 {
		agWeeklyMu.Lock()
		agWeeklyCache[cacheKey] = res
		agWeeklyAt[cacheKey] = now
		agWeeklyMu.Unlock()
	}
	return res, nil
}

