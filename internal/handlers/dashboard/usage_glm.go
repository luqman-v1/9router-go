package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GLM Coding Plan quota / usage fetcher (international + China regions).
// Port of upstream open-sse/services/usage/glm.js.
// Shared by pasted API keys and OAuth-minted coding-plan keys (both hit the
// same monitor endpoint with Bearer auth).

var (
	glmQuotaURLInternational = "https://api.z.ai/api/monitor/usage/quota/limit"
	glmQuotaURLChina         = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"
	// glmResetListURL is the international customer-package-reset/list endpoint.
	// China (glm-cn) has no reset-card path — only international tokens hit it.
	glmResetListURL = "https://api.z.ai/api/biz/customer-package-reset/list"
	// glmResetUseURL is the international customer-package-reset/use endpoint.
	glmResetUseURL = "https://api.z.ai/api/biz/customer-package-reset/use"
)

func glmQuotaURL(provider string) string {
	if provider == "glm-cn" {
		return glmQuotaURLChina
	}
	return glmQuotaURLInternational
}

// GLMResetCard represents one redeemable reset package attached to a GLM
// international connection. Mirrors the upstream GLMResetCard type from
// src/components/settings/profile/GLMResetCard*.
type GLMResetCard struct {
	RecordID  string `json:"recordId"`
	Type      string `json:"type"`      // "FIVE_HOUR" or "WEEK"
	ExpiresAt string `json:"expiresAt"` // RFC3339, UTC
}

// GLMResetCardsResult wraps the list endpoint payload for the dashboard.
type GLMResetCardsResult struct {
	AvailableCount int            `json:"availableCount"`
	Cards          []GLMResetCard `json:"cards"`
}

func fetchGLMUsage(ctx context.Context, provider, token string) usageResult {
	if strings.TrimSpace(token) == "" {
		return usageResult{message: "GLM API key not available."}
	}
	url := glmQuotaURL(provider)
	headers := map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(token),
		"Accept":        "application/json",
	}

	status, _, body, err := usageGet(ctx, url, headers)
	if err != nil {
		return usageResult{message: fmt.Sprintf("GLM error: %v", err)}
	}
	if status == http.StatusUnauthorized {
		return usageResult{message: "GLM API key invalid or expired."}
	}
	if status != http.StatusOK {
		return usageResult{message: fmt.Sprintf("GLM quota API error (%d).", status)}
	}

	data := usageJSON(body)
	res := parseGLMQuotaResponse(data)

	// International GLM accounts carry reset cards on the customer-package
	// endpoint; glm-cn does not expose them. Attach when the token is valid
	// and the read produced a real result so the UI can show the card list.
	if provider == "glm" {
		if cards, err := fetchGLMResetCards(ctx, token); err == nil && cards != nil {
			if res.extra == nil {
				res.extra = make(map[string]any)
			}
			res.extra["resetCards"] = cards
		}
	}

	return res
}

// fetchGLMResetCards calls the international reset-card list endpoint and
// returns a serialisable {availableCount, cards} map. Returns nil (not error)
// when the API is unreachable so the quota read still surfaces.
func fetchGLMResetCards(ctx context.Context, token string) (map[string]any, error) {
	url := glmResetListURL + "?targetType=PERSONAL"
	headers := map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(token),
		"Accept":        "application/json",
	}
	status, _, body, err := usageGet(ctx, url, headers)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		return nil, fmt.Errorf("GLM reset cards: unauthorized")
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GLM reset cards API error (%d)", status)
	}
	return parseGLMResetCardsResponse(usageJSON(body))
}

// parseGLMResetCardsResponse turns the API payload into the dashboard shape.
// Buckets fiveHourResets (type FIVE_HOUR) and weekResets (type WEEK) are merged,
// only available cards with a non-empty recordId are kept, expireTime is parsed
// tolerantly (Beijing UTC+08:00 naive datetimes as well as ISO8601), and the
// final list is sorted by expiresAt ascending.
func parseGLMResetCardsResponse(payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, fmt.Errorf("GLM reset cards: nil response")
	}

	// Z.ai wraps the buckets inside payload["data"].
	dataMap, _ := payload["data"].(map[string]any)
	if dataMap == nil {
		dataMap = payload
	}

	buckets := []bucketPair{
		{dataMap["fiveHourResets"], "FIVE_HOUR"},
		{dataMap["weekResets"], "WEEK"},
	}

	var cards []GLMResetCard
	for _, bp := range buckets {
		items, _ := bp.bucket.([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item == nil {
				continue
			}
			available, _ := item["available"].(bool)
			if !available {
				continue
			}
			// Accept both string and numeric recordId. Filter out missing or null.
			var recordID string
			if rawID, ok := item["recordId"]; ok && rawID != nil {
				switch v := rawID.(type) {
				case string:
					recordID = strings.TrimSpace(v)
				case float64:
					recordID = strconv.FormatFloat(v, 'f', -1, 64)
				case int, int64:
					recordID = fmt.Sprintf("%d", v)
				}
			}
			if recordID == "" || recordID == "<nil>" {
				continue
			}
			expiresAt, expTime := parseGLMResetExpireTime(item["expireTime"])
			if expiresAt == "" || !time.Now().Before(expTime) {
				continue
			}
			cards = append(cards, GLMResetCard{
				RecordID:  recordID,
				Type:      bp.cardType,
				ExpiresAt: expiresAt,
			})
		}
	}

	sort.SliceStable(cards, func(i, j int) bool {
		return cards[i].ExpiresAt < cards[j].ExpiresAt
	})

	result := GLMResetCardsResult{AvailableCount: len(cards), Cards: cards}
	out, _ := json.Marshal(result)
	return usageJSON(out), nil
}

type bucketPair struct {
	bucket   any
	cardType string
}

// parseGLMResetExpireTime handles the two formats the GLM list endpoint
// returns: ISO8601 with explicit timezone and Beijing naive datetimes like
// "2006-01-02 15:04:05" (stored without zone, meaning UTC+08:00).
// Returns the formatted UTC RFC3339 string and the parsed time.Time (or zero time.Time if invalid).
func parseGLMResetExpireTime(v any) (string, time.Time) {
	s, _ := v.(string)
	if s == "" {
		return "", time.Time{}
	}
	s = strings.TrimSpace(s)
	if tm, err := time.Parse(time.RFC3339, s); err == nil {
		return tm.UTC().Format(time.RFC3339), tm
	}
	if tm, err := time.Parse("2006-01-02T15:04:05Z07:00", s); err == nil {
		return tm.UTC().Format(time.RFC3339), tm
	}
	// Naive datetime — interpret as Beijing UTC+08:00 per upstream.
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if tm, err := time.ParseInLocation(layout, s, beijingZone); err == nil {
			return tm.UTC().Format(time.RFC3339), tm
		}
	}
	return "", time.Time{}
}

// beijingZone is fixed UTC+08:00. Used for naive GLM expireTime strings.
var beijingZone = time.FixedZone("CST", 8*60*60)

func parseGLMQuotaResponse(json map[string]any) usageResult {
	data, _ := json["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	limits, _ := data["limits"].([]any)
	quotas := make(map[string]any)

	for _, raw := range limits {
		limit, _ := raw.(map[string]any)
		if limit == nil {
			continue
		}
		limType, _ := limit["type"].(string)
		if limType != "TOKENS_LIMIT" && limType != "CREDIT_LIMIT" {
			continue
		}

		usedPercent := usageNum(limit["percentage"], 0)
		resetMs := usageNum(limit["nextResetTime"], 0)
		remaining := 100.0 - usedPercent
		if remaining < 0 {
			remaining = 0
		}

		unit := int(usageNum(limit["unit"], 0))
		number := int(usageNum(limit["number"], 0))

		var key string
		switch {
		case unit == 3:
			key = fmt.Sprintf("Session (%dh)", number)
		case unit == 6:
			key = "Weekly (7d)"
		case limType == "TOKENS_LIMIT":
			key = "Tokens"
		default:
			key = fmt.Sprintf("Limit (%d)", number)
		}

		var resetAt any = nil
		if resetMs > 0 {
			resetAt = time.UnixMilli(int64(resetMs)).UTC().Format(time.RFC3339Nano)
		}

		quotas[key] = map[string]any{
			"used":                usedPercent,
			"total":               float64(100),
			"remaining":           remaining,
			"remainingPercentage": remaining,
			"resetAt":             resetAt,
			"unlimited":           false,
		}
	}

	levelRaw, _ := data["level"].(string)
	plan := "Unknown"
	if levelRaw != "" {
		plan = strings.ToUpper(levelRaw[:1]) + strings.ToLower(levelRaw[1:])
	}

	return usageResult{
		plan:   plan,
		quotas: quotas,
	}
}
