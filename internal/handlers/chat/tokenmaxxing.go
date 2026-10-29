package chat

import (
	"context"
	json "encoding/json/v2"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
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

	var anchor *QuotaWindow
	var latestReset *time.Time

	for i := range validQuotas {
		qw := &validQuotas[i]
		isMonthly := strings.Contains(strings.ToLower(qw.Name), "month")
		if isMonthly {
			if anchor == nil || (qw.ResetAt != nil && (anchor.ResetAt == nil || qw.ResetAt.After(*anchor.ResetAt))) {
				anchor = qw
			}
		}
	}

	if anchor == nil {
		for i := range validQuotas {
			qw := &validQuotas[i]
			if qw.ResetAt != nil {
				if latestReset == nil || qw.ResetAt.After(*latestReset) {
					latestReset = qw.ResetAt
					anchor = qw
				}
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
func OrderByTokenmaxxingScores(models []string, scores map[string]*float64) []string {
	if len(models) <= 1 {
		return models
	}

	order := make([]string, len(models))
	copy(order, models)

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
func OrderModelsByTokenmaxxing(models []string, scores map[string]*float64) []string {
	return OrderByTokenmaxxingScores(models, scores)
}

type tokenmaxxingCacheEntry struct {
	score     *float64
	fetchedAt time.Time
}

var (
	tokenmaxxingCacheMu sync.RWMutex
	tokenmaxxingCache   = make(map[string]tokenmaxxingCacheEntry)
	tokenmaxxingTTL     = 60 * time.Second
)

// ClearTokenmaxxingCache resets the in-memory cache (for testing).
func ClearTokenmaxxingCache() {
	tokenmaxxingCacheMu.Lock()
	defer tokenmaxxingCacheMu.Unlock()
	tokenmaxxingCache = make(map[string]tokenmaxxingCacheEntry)
}

func (h *ChatHandler) applyTokenmaxxingRouting(ctx context.Context, models []string) []string {
	if len(models) <= 1 {
		return models
	}

	scores := make(map[string]*float64)
	now := time.Now()

	for _, m := range models {
		tokenmaxxingCacheMu.RLock()
		if entry, ok := tokenmaxxingCache[m]; ok && time.Since(entry.fetchedAt) < tokenmaxxingTTL {
			scores[m] = entry.score
			tokenmaxxingCacheMu.RUnlock()
			continue
		}
		tokenmaxxingCacheMu.RUnlock()

		score := h.resolveModelTokenmaxxingScore(ctx, m, now)

		tokenmaxxingCacheMu.Lock()
		tokenmaxxingCache[m] = tokenmaxxingCacheEntry{
			score:     score,
			fetchedAt: now,
		}
		tokenmaxxingCacheMu.Unlock()

		scores[m] = score
	}

	return OrderByTokenmaxxingScores(models, scores)
}

func (h *ChatHandler) resolveModelTokenmaxxingScore(ctx context.Context, modelEntry string, now time.Time) *float64 {
	provider, _, _ := strings.Cut(modelEntry, "/")
	if h.Repo == nil {
		return nil
	}
	conns, err := h.Repo.GetProviderConnections(provider, true)
	if err != nil || len(conns) == 0 {
		return nil
	}

	var bestScore *float64

	for _, c := range conns {
		if c == nil || c.Data == "" {
			continue
		}
		var data map[string]any
		if json.Unmarshal([]byte(c.Data), &data) != nil {
			continue
		}

		var quotas []QuotaWindow

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

		sc := ScoreQuotas(quotas, now)
		if sc != nil {
			if bestScore == nil || *sc > *bestScore {
				bestScore = sc
			}
		}
	}

	return bestScore
}
