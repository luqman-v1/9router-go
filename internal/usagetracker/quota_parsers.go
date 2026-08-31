package usagetracker

import (
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"
)

// QuotaWindow represents a single usage limit/window (remaining percentage, reset time, limits).
type QuotaWindow struct {
	RemainingPercentage float64   `json:"remainingPercentage"`
	UsedTokens          int64     `json:"usedTokens,omitempty"`
	LimitTokens         int64     `json:"limitTokens,omitempty"`
	UsedCount           int64     `json:"usedCount,omitempty"`
	LimitCount          int64     `json:"limitCount,omitempty"`
	IsUnlimited         bool      `json:"isUnlimited,omitempty"`
	ResetAt             time.Time `json:"resetAt,omitempty"`
	Message             string    `json:"message,omitempty"`
}

// ProviderQuotaInfo represents aggregated quota information for an account.
type ProviderQuotaInfo struct {
	Provider     string                 `json:"provider"`
	Plan         string                 `json:"plan,omitempty"`
	LimitReached bool                   `json:"limitReached,omitempty"`
	Quotas       map[string]QuotaWindow `json:"quotas"`
	Raw          map[string]any         `json:"raw,omitempty"`
}

// ParseCodexUsageQuotas extracts standard, review, and Spark rate limit windows from Codex usage payload.
func ParseCodexUsageQuotas(data []byte) (*ProviderQuotaInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal codex usage: %w", err)
	}

	quotas := make(map[string]QuotaWindow)

	// Helper to extract windows from a rate limit object
	appendWindows := func(prefix string, limitObj any) {
		limitMap, ok := limitObj.(map[string]any)
		if !ok || limitMap == nil {
			return
		}

		// Primary window / session
		if primary, ok := limitMap["primary_window"].(map[string]any); ok {
			key := "session"
			if prefix != "" {
				key = prefix + "_session"
			}
			quotas[key] = extractWindow(primary)
		} else if sess, ok := limitMap["session"].(map[string]any); ok {
			key := "session"
			if prefix != "" {
				key = prefix + "_session"
			}
			quotas[key] = extractWindow(sess)
		}

		// Secondary window / weekly
		if secondary, ok := limitMap["secondary_window"].(map[string]any); ok {
			key := "weekly"
			if prefix != "" {
				key = prefix + "_weekly"
			}
			quotas[key] = extractWindow(secondary)
		} else if weekly, ok := limitMap["weekly"].(map[string]any); ok {
			key := "weekly"
			if prefix != "" {
				key = prefix + "_weekly"
			}
			quotas[key] = extractWindow(weekly)
		}
	}

	// 1. Normal rate limits
	normalLimit := raw["rate_limit"]
	if normalLimit == nil {
		normalLimit = raw["rate_limits"]
	}
	if byLimit, ok := raw["rate_limits_by_limit_id"].(map[string]any); ok && normalLimit == nil {
		normalLimit = byLimit["codex"]
	}
	appendWindows("", normalLimit)

	// 2. Review rate limits
	var reviewLimit any
	if r, ok := raw["review_rate_limit"]; ok {
		reviewLimit = r
	} else if byLimit, ok := raw["rate_limits_by_limit_id"].(map[string]any); ok {
		reviewLimit = byLimit["review"]
	}
	appendWindows("review", reviewLimit)

	// 3. Spark rate limits (GPT-5.3-Codex-Spark)
	var sparkLimit any
	if s, ok := raw["spark_rate_limit"]; ok {
		sparkLimit = s
	} else if s, ok := raw["gpt_5_3_codex_spark_rate_limit"]; ok {
		sparkLimit = s
	} else if byLimit, ok := raw["rate_limits_by_limit_id"].(map[string]any); ok {
		if s, ok := byLimit["gpt-5.3-codex-spark"]; ok {
			sparkLimit = s
		} else if s, ok := byLimit["gpt_5_3_codex_spark"]; ok {
			sparkLimit = s
		} else if s, ok := byLimit["spark"]; ok {
			sparkLimit = s
		}
	} else if addl, ok := raw["additional_rate_limits"].([]any); ok {
		for _, item := range addl {
			if im, ok := item.(map[string]any); ok {
				name := fmt.Sprintf("%v", im["limit_name"])
				if strings.Contains(strings.ToLower(name), "spark") {
					sparkLimit = im
					break
				}
			}
		}
	}
	appendWindows("spark", sparkLimit)

	plan := "unknown"
	if p, ok := raw["plan_type"].(string); ok {
		plan = p
	}

	return &ProviderQuotaInfo{
		Provider: "codex",
		Plan:     plan,
		Quotas:   quotas,
		Raw:      raw,
	}, nil
}

func extractWindow(m map[string]any) QuotaWindow {
	var qw QuotaWindow
	if rem, ok := m["remaining_fraction"].(float64); ok {
		qw.RemainingPercentage = rem * 100
	} else if rem, ok := m["remaining_percentage"].(float64); ok {
		qw.RemainingPercentage = rem
	} else if used, ok := m["used_tokens"].(float64); ok {
		qw.UsedTokens = int64(used)
		if limit, ok := m["limit_tokens"].(float64); ok && limit > 0 {
			qw.LimitTokens = int64(limit)
			qw.RemainingPercentage = (1.0 - (used / limit)) * 100
		}
	}
	// Clamp to a sane [0, 100] range so downstream dashboards and quota-block
	// logic never see negative or >100% values from miscalibrated upstreams.
	qw.RemainingPercentage = clampPercentage(qw.RemainingPercentage)

	if resetStr, ok := m["reset_time"].(string); ok && resetStr != "" {
		if t, err := time.Parse(time.RFC3339, resetStr); err == nil {
			qw.ResetAt = t.UTC()
		}
	} else if resetSec, ok := m["reset_after_seconds"].(float64); ok && resetSec > 0 {
		qw.ResetAt = time.Now().Add(time.Duration(resetSec) * time.Second).UTC()
	}

	return qw
}

// ParseGLMUsageQuotas parses multi-interval and credit-based GLM quota payloads.
func ParseGLMUsageQuotas(data []byte) (*ProviderQuotaInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal glm usage: %w", err)
	}

	quotas := make(map[string]QuotaWindow)

	// Accept limits array (tokens or credits)
	if limits, ok := raw["limits"].([]any); ok {
		for _, item := range limits {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			limitType, _ := m["type"].(string) // "TOKENS_LIMIT", "CREDIT_LIMIT", etc.
			unit, _ := m["unit"].(string)      // "5h", "7d", "session", "weekly"

			key := "session"
			if strings.Contains(unit, "7d") || strings.Contains(unit, "week") {
				key = "weekly"
			} else if limitType == "CREDIT_LIMIT" {
				key = "credits"
			}

			qw := extractWindow(m)
			if limitType == "CREDIT_LIMIT" {
				if totalCredits, ok := m["total_credits"].(float64); ok {
					qw.LimitCount = int64(totalCredits)
				}
				if usedCredits, ok := m["used_credits"].(float64); ok {
					qw.UsedCount = int64(usedCredits)
				}
			}
			quotas[key] = qw
		}
	}

	plan := "Free"
	if p, ok := raw["plan"].(string); ok {
		plan = p
	}

	return &ProviderQuotaInfo{
		Provider: "glm",
		Plan:     plan,
		Quotas:   quotas,
		Raw:      raw,
	}, nil
}

// ParseZedUsageQuotas parses Zed account usage (GET /client/users/me).
func ParseZedUsageQuotas(data []byte) (*ProviderQuotaInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal zed usage: %w", err)
	}

	quotas := make(map[string]QuotaWindow)

	plan := "Free"
	if p, ok := raw["plan"].(string); ok && p != "" {
		plan = p
	}

	// Edit predictions quota
	if editPred, ok := raw["edit_predictions"].(map[string]any); ok {
		var qw QuotaWindow
		used, _ := editPred["used"].(float64)
		qw.UsedCount = int64(used)
		if limit, ok := editPred["limit"].(float64); ok && limit > 0 {
			qw.LimitCount = int64(limit)
			qw.RemainingPercentage = (1.0 - (used / limit)) * 100
		} else {
			qw.IsUnlimited = true
			qw.RemainingPercentage = 100
		}
		if resetStr, ok := editPred["reset_at"].(string); ok && resetStr != "" {
			if t, err := time.Parse(time.RFC3339, resetStr); err == nil {
				qw.ResetAt = t.UTC()
			}
		}
		quotas["edit_predictions"] = qw
	}

	// Hosted model requests quota
	if hosted, ok := raw["hosted_model_requests"].(map[string]any); ok {
		var qw QuotaWindow
		used, _ := hosted["used"].(float64)
		qw.UsedCount = int64(used)
		if limit, ok := hosted["limit"].(float64); ok && limit > 0 {
			qw.LimitCount = int64(limit)
			qw.RemainingPercentage = (1.0 - (used / limit)) * 100
		} else {
			qw.IsUnlimited = true
			qw.RemainingPercentage = 100
		}
		if resetStr, ok := hosted["reset_at"].(string); ok && resetStr != "" {
			if t, err := time.Parse(time.RFC3339, resetStr); err == nil {
				qw.ResetAt = t.UTC()
			}
		}
		quotas["hosted_models"] = qw
	}

	return &ProviderQuotaInfo{
		Provider: "zed",
		Plan:     plan,
		Quotas:   quotas,
		Raw:      raw,
	}, nil
}

// clampPercentage bounds a float64 percentage value to [0, 100].
func clampPercentage(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}
