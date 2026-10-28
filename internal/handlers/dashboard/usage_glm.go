package dashboard

import (
	"context"
	"fmt"
	"net/http"
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
)

func glmQuotaURL(provider string) string {
	if provider == "glm-cn" {
		return glmQuotaURLChina
	}
	return glmQuotaURLInternational
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
	return parseGLMQuotaResponse(data)
}

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
