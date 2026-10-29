package chat

import (
	"context"
	json "encoding/json/v2"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/models"
)

type QuotaWindow struct {
	Name                string
	RemainingPercentage *float64
	Used                *float64
	Total               *float64
	ResetAt             *time.Time
	Unlimited           bool
}

// ScoreQuotas scores a slice of quota windows given current time.
// Port of upstream open-sse/services/tokenmaxxing.js scoreQuotas.
func ScoreQuotas(quotas []QuotaWindow, now time.Time) *float64 {
	var validQuotas []QuotaWindow
	var finitePercentages []float64

	for _, q := range quotas {
		rem := q.RemainingPercentage
		if rem == nil && q.Used != nil && q.Total != nil && *q.Total > 0 {
			calc := math.Max(0, (*q.Total-*q.Used) / *q.Total * 100)
			rem = &calc
		}
		if q.Unlimited {
			continue
		}
		if rem == nil || math.IsNaN(*rem) || math.IsInf(*rem, 0) {
			continue
		}
		if *rem <= 0 {
			zeroVal := 0.0
			return &zeroVal
		}
		validQuotas = append(validQuotas, QuotaWindow{
			Name:                q.Name,
			RemainingPercentage: rem,
			ResetAt:             q.ResetAt,
			Unlimited:           q.Unlimited,
		})
		finitePercentages = append(finitePercentages, *rem)
	}

	if len(finitePercentages) == 0 {
		return nil
	}

	for _, p := range finitePercentages {
		if p <= 0 {
			zeroVal := 0.0
			return &zeroVal
		}
	}

	// Fix pickAnchor resetAt filtering (Upstream Parity):
	// Filter quotas to only entries where resetAt != nil before checking for /month/i
	var withReset []QuotaWindow
	for _, q := range validQuotas {
		if q.ResetAt != nil && !q.ResetAt.IsZero() {
			withReset = append(withReset, q)
		}
	}

	var anchor *QuotaWindow
	for i := range withReset {
		qw := &withReset[i]
		if strings.Contains(strings.ToLower(qw.Name), "month") {
			if anchor == nil || qw.ResetAt.After(*anchor.ResetAt) {
				anchor = qw
			}
		}
	}

	if anchor == nil && len(withReset) > 0 {
		latestReset := withReset[0].ResetAt
		anchor = &withReset[0]
		for i := 1; i < len(withReset); i++ {
			if withReset[i].ResetAt.After(*latestReset) {
				latestReset = withReset[i].ResetAt
				anchor = &withReset[i]
			}
		}
	}

	minFinite := finitePercentages[0]
	for _, p := range finitePercentages {
		if p < minFinite {
			minFinite = p
		}
	}

	if anchor == nil || anchor.ResetAt == nil {
		score := minFinite / 30.0
		return &score
	}

	days := math.Max(anchor.ResetAt.Sub(now).Hours()/24.0, 1.0/24.0)
	anchorRem := *anchor.RemainingPercentage
	minVal := math.Min(anchorRem, minFinite)
	score := minVal / days
	return &score
}

// OrderByTokenmaxxingScores ranks models by their tokenmaxxing scores.
// score > 0 -> rank: 1.0 + *score
// score == nil -> rank: 0.5
// score == 0 -> rank: 0.0
// Stable sort descending by rank.
func OrderByTokenmaxxingScores(modelsList []string, scores map[string]*float64) []string {
	if len(modelsList) <= 1 {
		return modelsList
	}

	order := make([]string, len(modelsList))
	copy(order, modelsList)

	getRank := func(m string) float64 {
		sc := scores[m]
		if sc == nil {
			return 0.5
		}
		if *sc <= 0 {
			return 0.0
		}
		return 1.0 + *sc
	}

	sort.SliceStable(order, func(i, j int) bool {
		ri := getRank(order[i])
		rj := getRank(order[j])
		return ri > rj
	})

	return order
}

// OrderModelsByTokenmaxxing is an alias for OrderByTokenmaxxingScores.
func OrderModelsByTokenmaxxing(modelsList []string, scores map[string]*float64) []string {
	return OrderByTokenmaxxingScores(modelsList, scores)
}

type tokenmaxxingCacheEntry struct {
	score     *float64
	fetchedAt time.Time
}

var (
	tokenmaxxingCacheMu      sync.RWMutex
	tokenmaxxingCache        = make(map[string]tokenmaxxingCacheEntry)
	tokenmaxxingTTL          = 60 * time.Second
	maxTokenmaxxingCacheSize = 500
)

// ClearTokenmaxxingCache resets the in-memory cache (for testing).
func ClearTokenmaxxingCache() {
	tokenmaxxingCacheMu.Lock()
	defer tokenmaxxingCacheMu.Unlock()
	tokenmaxxingCache = make(map[string]tokenmaxxingCacheEntry)
}

func (h *ChatHandler) applyTokenmaxxingRouting(ctx context.Context, modelsList []string) []string {
	if len(modelsList) <= 1 {
		return modelsList
	}

	scores := make(map[string]*float64)
	now := time.Now()

	// Group models by canonical provider to avoid N+1 DB queries and resolve aliases (e.g. ag -> antigravity)
	providerModels := make(map[string][]string)
	for _, m := range modelsList {
		prefix, _, ok := strings.Cut(m, "/")
		if !ok {
			scores[m] = nil
			continue
		}
		canonical := resolveProviderAlias(prefix)
		providerModels[canonical] = append(providerModels[canonical], m)
	}

	for canonical, groupModels := range providerModels {
		// Check cache first for all models in group
		allCached := true
		for _, m := range groupModels {
			tokenmaxxingCacheMu.RLock()
			entry, ok := tokenmaxxingCache[m]
			tokenmaxxingCacheMu.RUnlock()
			if !ok || time.Since(entry.fetchedAt) >= tokenmaxxingTTL {
				allCached = false
				break
			}
		}

		if allCached && len(groupModels) > 0 {
			for _, m := range groupModels {
				tokenmaxxingCacheMu.RLock()
				scores[m] = tokenmaxxingCache[m].score
				tokenmaxxingCacheMu.RUnlock()
			}
			continue
		}

		score := h.resolveProviderTokenmaxxingScore(ctx, canonical, groupModels, now)

		tokenmaxxingCacheMu.Lock()
		for _, m := range groupModels {
			if len(tokenmaxxingCache) >= maxTokenmaxxingCacheSize {
				for k := range tokenmaxxingCache {
					delete(tokenmaxxingCache, k)
					break
				}
			}
			tokenmaxxingCache[m] = tokenmaxxingCacheEntry{
				score:     score,
				fetchedAt: now,
			}
			scores[m] = score
		}
		tokenmaxxingCacheMu.Unlock()
	}

	return OrderByTokenmaxxingScores(modelsList, scores)
}

func (h *ChatHandler) resolveProviderTokenmaxxingScore(ctx context.Context, canonicalProvider string, modelsList []string, now time.Time) *float64 {
	if h.Repo == nil {
		return nil
	}
	conns, err := h.Repo.GetProviderConnections(canonicalProvider, true)
	if err != nil || len(conns) == 0 {
		if conns2, err2 := h.Repo.GetProviderConnections("", true); err2 == nil {
			var filtered []*models.ProviderConnection
			for _, c := range conns2 {
				if c != nil && (strings.EqualFold(c.Provider, canonicalProvider) || strings.EqualFold(resolveProviderAlias(c.Provider), canonicalProvider)) {
					filtered = append(filtered, c)
				}
			}
			conns = filtered
		}
		if len(conns) == 0 {
			return nil
		}
	}

	var bestScore *float64

	for _, c := range conns {
		if c == nil {
			continue
		}

		var quotas []QuotaWindow

		// Check agQuotaCache / agWeeklyCache for antigravity connections
		if strings.EqualFold(canonicalProvider, "antigravity") {
			agQuotaMu.RLock()
			if modelQuotas, ok := agQuotaCache[c.ID]; ok {
				for modelName, mq := range modelQuotas {
					rp := mq.RemainingPercentage
					reset := mq.ResetAt
					quotas = append(quotas, QuotaWindow{
						Name:                modelName,
						RemainingPercentage: &rp,
						ResetAt:             &reset,
					})
				}
			}
			if weeklyQuotas, ok := agWeeklyCache[c.ID]; ok {
				for family, wq := range weeklyQuotas {
					rp := wq.RemainingPercentage
					reset := wq.ResetAt
					quotas = append(quotas, QuotaWindow{
						Name:                "weekly-" + family,
						RemainingPercentage: &rp,
						ResetAt:             &reset,
					})
				}
			}
			agQuotaMu.RUnlock()
		}

		if c.Data != "" {
			var data map[string]any
			if json.Unmarshal([]byte(c.Data), &data) == nil {
				if qMap, ok := data["quotas"].(map[string]any); ok {
					for name, qVal := range qMap {
						qw := QuotaWindow{Name: name}
						if qObj, ok := qVal.(map[string]any); ok {
							if rp, ok := qObj["remainingPercentage"].(float64); ok {
								qw.RemainingPercentage = &rp
							} else if rpInt, ok := qObj["remainingPercentage"].(int); ok {
								f := float64(rpInt)
								qw.RemainingPercentage = &f
							}
							if used, ok := qObj["used"].(float64); ok {
								qw.Used = &used
							} else if usedInt, ok := qObj["used"].(int); ok {
								f := float64(usedInt)
								qw.Used = &f
							}
							if total, ok := qObj["total"].(float64); ok {
								qw.Total = &total
							} else if totalInt, ok := qObj["total"].(int); ok {
								f := float64(totalInt)
								qw.Total = &f
							}
							if unlim, ok := qObj["unlimited"].(bool); ok {
								qw.Unlimited = unlim
							} else if unlimStr, ok := qObj["isUnlimited"].(bool); ok {
								qw.Unlimited = unlimStr
							}
							if resetStr, ok := qObj["resetAt"].(string); ok && resetStr != "" {
								if t, err := time.Parse(time.RFC3339, resetStr); err == nil {
									qw.ResetAt = &t
								}
							}
						}
						quotas = append(quotas, qw)
					}
				}

				if rateLimitedUntil, ok := data["rateLimitedUntil"].(string); ok && rateLimitedUntil != "" {
					if t, err := time.Parse(time.RFC3339, rateLimitedUntil); err == nil && t.After(now) {
						zeroVal := 0.0
						quotas = append(quotas, QuotaWindow{
							Name:                "rateLimited",
							RemainingPercentage: &zeroVal,
							ResetAt:             &t,
						})
					}
				}
			}
		}

		sc := ScoreQuotas(quotas, now)
		if sc != nil {
			if bestScore == nil || *sc > *bestScore {
				bestScore = sc
			}
		}
	}

	return bestScore
}
