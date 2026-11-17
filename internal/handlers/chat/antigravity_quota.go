package chat

import (
	json "9router/proxy/internal/fastjson"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/samber/lo"
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
	agQuotaMu    sync.RWMutex
	agQuotaCache = make(map[string]map[string]AntigravityModelQuota) // connectionID -> model -> quota
	// agRosterCache mirrors OMP dynamic-models-authoritative: the account's
	// served roster from fetchAvailableModels. Bundled catalog rows that
	// the account does not serve must be blocked before routing, or every
	// request to them 404s at Cloud Code.
	agRosterMu      sync.RWMutex
	agRosterCache   = make(map[string]map[string]struct{}) // connectionID -> served model id set
	agLastRefreshAt = make(map[string]time.Time)           // connectionID -> last refresh timestamp
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

	agRosterMu.Lock()
	agRosterCache = make(map[string]map[string]struct{})
	agRosterMu.Unlock()

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
	shortConn := shortConnID(connectionID)
	log.Warn("ag_quota", "model locked until reset", "connection", shortConn, "model", model, "resetAt", resetAt.Format(time.RFC3339))
}

// IsAntigravityModelServed reports whether the account's roster lists model.
// Absence is false only when the roster is known; an unknown roster (never
// refreshed) lets the request through so a fresh account is not blanked by
// a missing discovery.
func IsAntigravityModelServed(connectionID, model string) bool {
	agRosterMu.RLock()
	defer agRosterMu.RUnlock()
	roster, ok := agRosterCache[connectionID]
	if !ok || len(roster) == 0 {
		return true
	}
	if _, ok := roster[model]; ok {
		return true
	}
	if canonical, exists := translator.AntigravityModelSynonyms[model]; exists {
		if _, ok := roster[canonical]; ok {
			return true
		}
	}
	return false
}

// IsAntigravityModelBlocked reports whether connectionID has an exhausted quota for model until resetAt.
func IsAntigravityModelBlocked(connectionID, model string) bool {
	if connectionID == "" || model == "" {
		return false
	}
	if !IsAntigravityModelServed(connectionID, model) {
		return true
	}

	agQuotaMu.RLock()
	modelsMap, ok := agQuotaCache[connectionID]
	q, known := antigravityQuotaForModel(modelsMap, model)
	agQuotaMu.RUnlock()
	return ok && known && quotaEntryExhausted(q, time.Now().UTC())
}

// antigravityQuotaForModel resolves the quota that governs model: its own
// entry and tier aliases first, then the shared family session/weekly buckets.
// Any exhausted bucket wins over a healthy per-model reading; otherwise the
// most constrained reading is returned (upstream getAntigravityModelQuota).
// known is false when the map says nothing about the model.
func antigravityQuotaForModel(quotas map[string]AntigravityModelQuota, model string) (AntigravityModelQuota, bool) {
	if len(quotas) == 0 || model == "" {
		return AntigravityModelQuota{}, false
	}

	checkModels := []string{model}
	if canonical, exists := translator.AntigravityModelSynonyms[model]; exists && canonical != model {
		checkModels = append(checkModels, canonical)
	}
	if model == "gemini-4-argon" {
		checkModels = append(checkModels, "gemini-4-argon-high", "gemini-4-argon-medium")
	}
	if model == "gemini-4-carbon" {
		checkModels = append(checkModels, "gemini-4-carbon-high", "gemini-4-carbon-medium")
	}
	if model == "claude-sonnet-5-5" {
		checkModels = append(checkModels, "claude-sonnet-5-5-high")
	}
	if model == "claude-opus-5-5" {
		checkModels = append(checkModels, "claude-opus-5-5-high")
	}

	candidates := make([]AntigravityModelQuota, 0, len(checkModels)+2)
	for _, m := range checkModels {
		if q, exists := quotas[m]; exists {
			candidates = append(candidates, q)
		}
	}
	for _, q := range antigravityFamilyQuotas(quotas, model) {
		candidates = append(candidates, q)
	}
	if len(candidates) == 0 {
		return AntigravityModelQuota{}, false
	}

	now := time.Now().UTC()
	best := candidates[0]
	bestExhausted := quotaEntryExhausted(best, now)
	for _, q := range candidates[1:] {
		exhausted := quotaEntryExhausted(q, now)
		// Any exhausted bucket wins; the latest reset keeps the lock honest.
		if exhausted && (!bestExhausted || q.ResetAt.After(best.ResetAt)) {
			best, bestExhausted = q, true
			continue
		}
		if !exhausted && !bestExhausted && q.RemainingPercentage < best.RemainingPercentage {
			best = q
		}
	}
	return best, true
}

// antigravityFamilyQuotas returns the shared session/weekly buckets for the
// model's family. Argon falls back to the Gemini buckets on Ultra plans, where
// no separate Argon summary exists (upstream decolua/9router#8a833631).
func antigravityFamilyQuotas(quotas map[string]AntigravityModelQuota, model string) []AntigravityModelQuota {
	var keys []string
	switch {
	case strings.Contains(model, "argon"):
		keys = []string{"gemini_argon_weekly", "argon_weekly", "gemini_argon_session", "argon_session"}
		hasArgonSummary := lo.SomeBy(keys, func(k string) bool { _, ok := quotas[k]; return ok })
		if !hasArgonSummary {
			keys = []string{"gemini_weekly", "gemini_session"}
		}
	case strings.HasPrefix(model, "gemini-"):
		keys = []string{"gemini_weekly", "gemini_session"}
	case strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "gpt-"):
		keys = []string{"claude_gpt_weekly", "claude_gpt_session"}
	}
	out := make([]AntigravityModelQuota, 0, len(keys))
	for _, k := range keys {
		if q, ok := quotas[k]; ok {
			out = append(out, q)
		}
	}
	return out
}

// quotaEntryExhausted reports whether a cached quota entry means "do not route
// here until reset".
func quotaEntryExhausted(q AntigravityModelQuota, now time.Time) bool {
	return q.RemainingPercentage <= 0 && !q.ResetAt.IsZero() && q.ResetAt.After(now)
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
	req.Header.Set("User-Agent", providers.AntigravityCLIUserAgent)
	req.Header.Set("X-Client-Name", "antigravity")
	req.Header.Set("X-Client-Version", providers.AntigravityCLIVersion)

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

	// Roster-authoritative: the discovery RPC lists what this account is
	// actually served; bundled rows absent from it are dropped from the
	// cache so routing never sends a request Cloud Code will 404.
	roster := make(map[string]struct{}, len(data.Models))
	for modelKey := range data.Models {
		roster[modelKey] = struct{}{}
	}
	if len(roster) > 0 {
		agRosterMu.Lock()
		agRosterCache[connectionID] = roster
		agRosterMu.Unlock()
	}

	agQuotaMu.Lock()
	agQuotaCache[connectionID] = quotas
	agQuotaMu.Unlock()

	return quotas, nil
}

// AntigravityQuotaError bundles the inputs to HandleAntigravityQuotaError.
// Kept as a struct: the handler takes 8 inputs and positional calls proved
// error-prone to extend.
type AntigravityQuotaError struct {
	Ctx          context.Context
	Client       *http.Client
	ConnectionID string
	Status       int
	Model        string
	AccessToken  string
	ProjectID    string
}

// HandleAntigravityQuotaError handles Antigravity 409/429 errors by refreshing
// live quota and returning model resetAt.
//
// Strike breaker (upstream antigravityQuota.js): every 409/429 whose quota
// reading is optimistic (remaining > 0) or unavailable (quota API error/403)
// counts toward the pair's strikes. 3 strikes within the window block the
// connection+model pair for 15m. A body's wording is never consulted: Google
// answers a spent quota with "Resource has been exhausted (e.g. check quota)."
// and no marker field, so marker-gating left real exhaustion unstruck.
func HandleAntigravityQuotaError(p AntigravityQuotaError) *time.Time {
	if p.Status != http.StatusConflict && p.Status != http.StatusTooManyRequests {
		return nil
	}

	shortConn := shortConnID(p.ConnectionID)
	log.Info("ag_quota", "refreshing quota on error", "connection", shortConn, "status", p.Status, "model", p.Model)

	now := time.Now().UTC()
	quotas, err := RefreshAntigravityQuota(p.Ctx, p.Client, p.ConnectionID, p.AccessToken, p.ProjectID)
	if err != nil {
		quotas = nil
	}
	q, known := antigravityQuotaForModel(quotas, p.Model)

	if !known || q.RemainingPercentage > 0 {
		return strikeAntigravityQuota(p.ConnectionID, p.Model, shortConn, p.Status, q, known, now)
	}
	if !quotaEntryExhausted(q, now) {
		return nil
	}

	log.Warn("ag_quota", "quota exhausted; CACHE_BLOCK until reset", "connection", shortConn, "model", p.Model, "resetAt", q.ResetAt.Format(time.RFC3339))
	res := q.ResetAt
	return &res
}

// strikeAntigravityQuota records one strike for the connection+model pair and
// returns the 15m block deadline once the threshold is reached.
func strikeAntigravityQuota(connectionID, model, shortConn string, status int, q AntigravityModelQuota, known bool, now time.Time) *time.Time {
	key := connectionID + "|" + model
	agStrikeMu.Lock()
	defer agStrikeMu.Unlock()

	cutoff := now.Add(-agStrikeWindow)
	strikes := agStrikes[key]
	kept := make([]time.Time, 0, len(strikes)+1)
	for _, t := range strikes {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	agStrikes[key] = kept
	if len(kept) < agStrikeThreshold {
		return nil
	}

	blockUntil := now.Add(agStrikeBlockDuration)
	agStrikeBlocks[key] = blockUntil
	// Store blocked quota so IsAntigravityModelBlocked sees it even after an
	// optimistic refresh resurrects the pair.
	agQuotaMu.Lock()
	if _, ok := agQuotaCache[connectionID]; !ok {
		agQuotaCache[connectionID] = make(map[string]AntigravityModelQuota)
	}
	agQuotaCache[connectionID][model] = AntigravityModelQuota{
		RemainingPercentage: 0,
		ResetAt:             blockUntil,
	}
	agQuotaMu.Unlock()

	reading := "unknown"
	if known {
		reading = fmt.Sprintf("%.0f%%", q.RemainingPercentage)
	}
	log.Warn("ag_quota", "optimistic quota strike-break: 3x error within 60s while quota optimistic, CACHE_BLOCK 15m",
		"connection", shortConn, "model", model, "status", status, "quota", reading, "blockUntil", blockUntil.Format(time.RFC3339))
	return &blockUntil
}

// ClearAntigravityStrikes clears strike history for a connection/model (called on success).
func ClearAntigravityStrikes(connectionID, model string) {
	if connectionID == "" || model == "" {
		return
	}
	keys := []string{model}
	if canonical, exists := translator.AntigravityModelSynonyms[model]; exists && canonical != model {
		keys = append(keys, canonical)
	}

	agStrikeMu.Lock()
	blocks := make(map[string]time.Time, len(keys))
	for _, k := range keys {
		if until, ok := agStrikeBlocks[connectionID+"|"+k]; ok {
			blocks[k] = until
		}
		delete(agStrikes, connectionID+"|"+k)
		delete(agStrikeBlocks, connectionID+"|"+k)
	}
	agStrikeMu.Unlock()

	// Drop the synthesized 0% entry this breaker wrote: a real upstream 0%
	// reading carries a different reset and must survive.
	if len(blocks) == 0 {
		return
	}
	agQuotaMu.Lock()
	defer agQuotaMu.Unlock()
	cached := agQuotaCache[connectionID]
	for k, until := range blocks {
		if q, ok := cached[k]; ok && q.RemainingPercentage <= 0 && q.ResetAt.Equal(until) {
			delete(cached, k)
		}
	}
}

// ParseWeeklyQuotaSummary parses retrieveUserQuotaSummary response JSON into family quotas.
func ParseWeeklyQuotaSummary(raw []byte) map[string]AntigravityWeeklyQuota {
	var payload struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Buckets     []struct {
				BucketID          string   `json:"bucketId"`
				DisplayName       string   `json:"displayName"`
				Window            string   `json:"window"`
				Disabled          bool     `json:"disabled"`
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
			} `json:"buckets"`
		} `json:"groups"`
		QuotaSummary *struct {
			Groups []struct {
				DisplayName string `json:"displayName"`
				Buckets     []struct {
					BucketID          string   `json:"bucketId"`
					DisplayName       string   `json:"displayName"`
					Window            string   `json:"window"`
					Disabled          bool     `json:"disabled"`
					RemainingFraction *float64 `json:"remainingFraction"`
					ResetTime         string   `json:"resetTime"`
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
		isArgon := strings.Contains(strings.ToLower(gName), "argon")
		isGemini := !isArgon && strings.Contains(strings.ToLower(gName), "gemini")
		isClaudeGPT := strings.Contains(strings.ToLower(gName), "claude") || strings.Contains(strings.ToLower(gName), "gpt")
		if !isArgon && !isGemini && !isClaudeGPT {
			continue
		}

		for _, b := range g.Buckets {
			// Classify weekly vs 5h-session buckets (parity with
			// decolua/9router#4209). A disabled session bucket is kept at
			// 0 (upstream marks it disabled when weekly is hit); disabled
			// weekly buckets are truly disabled and skipped.
			windowType := strings.ToLower(b.Window)
			bText := strings.ToLower(b.BucketID + " " + b.DisplayName)
			isWeekly := windowType == "weekly" || strings.Contains(bText, "weekly")
			isSession := windowType == "5h" || windowType == "daily" ||
				strings.Contains(bText, "five hour") ||
				strings.Contains(bText, "5h") ||
				strings.Contains(bText, "daily")
			if !isWeekly && !isSession {
				continue
			}
			if b.Disabled && isWeekly {
				continue
			}
			var frac float64
			if b.Disabled {
				frac = 0
			} else if b.RemainingFraction != nil {
				frac = *b.RemainingFraction
			} else {
				// null or missing: skip the bucket so the quota stays unknown
				// rather than being reported as exhausted.
				continue
			}
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
			if isArgon {
				key = "gemini_argon_weekly"
				dName = "Gemini 4 Argon (Weekly)"
			} else if isClaudeGPT {
				key = "claude_gpt_weekly"
				dName = "Claude & GPT (Weekly)"
			}
			if !isWeekly {
				if isArgon {
					key = "gemini_argon_session"
					dName = "Gemini 4 Argon (5h)"
				} else if strings.HasPrefix(key, "gemini") {
					key = "gemini_session"
					dName = "Gemini (5h)"
				} else {
					key = "claude_gpt_session"
					dName = "Claude & GPT (5h)"
				}
			}

			// First matching bucket per type wins (no break: one group can
			// yield both a weekly and a session bucket).
			if _, exists := result[key]; !exists {
				entry := AntigravityWeeklyQuota{
					Used:                used,
					Total:               total,
					RemainingPercentage: frac * 100,
					ResetAt:             resetAt,
					DisplayName:         dName,
				}
				result[key] = entry
				switch key {
				case "gemini_argon_weekly":
					result["argon_weekly"] = entry
				case "gemini_argon_session":
					result["argon_session"] = entry
				}
			}
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
	if ok {
		if now.Sub(cachedAt) < agWeeklyTTL {
			return cached, nil
		}
		// Lazy eviction: token-rotated keys must not accumulate forever
		// (OAuth tokens refresh hourly, minting a fresh key each time).
		agWeeklyMu.Lock()
		if at, ok := agWeeklyAt[cacheKey]; ok && at.Equal(cachedAt) {
			delete(agWeeklyCache, cacheKey)
			delete(agWeeklyAt, cacheKey)
		}
		agWeeklyMu.Unlock()
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
	req.Header.Set("User-Agent", providers.AntigravityCLIUserAgent)
	req.Header.Set("X-Client-Name", "antigravity")
	req.Header.Set("X-Client-Version", providers.AntigravityCLIVersion)

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
		// Opportunistic sweep: drop entries older than 2x TTL so dead
		// token keys drain without a dedicated goroutine.
		cutoff := now.Add(-2 * agWeeklyTTL)
		for k, at := range agWeeklyAt {
			if at.Before(cutoff) {
				delete(agWeeklyCache, k)
				delete(agWeeklyAt, k)
			}
		}
		agWeeklyMu.Unlock()
	}
	return res, nil
}
