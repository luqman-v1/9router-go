package dashboard

// OpenCode Zen quota — GET https://opencode.ai/zen/v1/usage
// Port of open-sse/services/usage/opencode-zen.js (getOpenCodeZenUsage).
//
// The endpoint answers with a percentage consumed per billing period rather
// than a token count, so every quota is normalized to a 0..100 used/total pair
// the dashboard already knows how to draw.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
)

// openCodeZenUsageURL is the provider default (registry). openCodeZenUsageURLFor
// derives the same endpoint from a connection's baseUrl, so a self-hosted or
// relayed Zen endpoint reports quotas from its own route. Both are variables
// only so unit tests can point them at a local server.
var (
	openCodeZenUsageURL    = providers.KnownProviders["opencode-zen"].UsageURL
	openCodeZenUsageURLFor = zenUsageURLFromBase
)

// zenUsageURLFromBase maps ".../zen/v1/chat/completions" to ".../zen/v1/usage",
// where the same key reports its quotas.
func zenUsageURLFromBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		if trimmed := strings.TrimSuffix(base, suffix); trimmed != base {
			return trimmed + "/usage"
		}
	}
	return ""
}

var openCodeZenQuotaNames = []struct {
	period string
	name   string
}{
	{"rolling", "Rolling"},
	{"weekly", "Weekly"},
	{"monthly", "Monthly"},
}

func fetchOpenCodeZenUsage(ctx context.Context, apiKey, baseURL string) usageResult {
	const plan = "OpenCode Zen"
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return usageResult{message: "OpenCode Zen API key not available. Add a key to view usage."}
	}

	usageURL := openCodeZenUsageURL
	if derived := openCodeZenUsageURLFor(baseURL); derived != "" {
		usageURL = derived
	}
	headers := map[string]string{
		"Authorization": "Bearer " + apiKey,
		"Accept":        "application/json",
		"User-Agent":    usageUserAgent,
	}
	status, _, out, err := usageGet(ctx, usageURL, headers)
	if err != nil {
		return usageResult{message: fmt.Sprintf("OpenCode Zen error: %v", err)}
	}

	switch status {
	case http.StatusUnauthorized:
		return usageResult{plan: plan, message: "OpenCode Zen authentication failed. Check the API key."}
	case http.StatusForbidden:
		if openCodeZenIsEntitlementError(out) {
			return usageResult{plan: plan, message: "OpenCode Zen billing required for this API key."}
		}
		return usageResult{plan: plan, message: "OpenCode Zen access forbidden for this API key."}
	}
	if status < 200 || status >= 300 {
		return usageResult{plan: plan, message: fmt.Sprintf("OpenCode Zen usage API error (%d).", status)}
	}

	usage, ok := usageJSON(out)["usage"].(map[string]any)
	if !ok {
		return usageResult{plan: plan, message: "OpenCode Zen usage response did not contain quota data."}
	}

	quotas := map[string]any{}
	for _, q := range openCodeZenQuotaNames {
		quota, ok := usage[q.period].(map[string]any)
		if !ok {
			continue
		}
		percent, ok := usageFiniteNum(quota["percent"])
		if !ok {
			continue
		}
		used := math.Max(0, math.Min(100, percent))
		quotas[q.name] = map[string]any{
			"used":                used,
			"total":               100,
			"remaining":           100 - used,
			"remainingPercentage": 100 - used,
			"resetAt":             usageResetTime(quota["resetsAt"]),
			"unlimited":           false,
		}
	}
	if len(quotas) == 0 {
		return usageResult{plan: plan, message: "OpenCode Zen usage response did not contain valid quota data."}
	}
	return usageResult{plan: plan, quotas: quotas}
}

// openCodeZenIsEntitlementError reports whether a 403 body is the "this key has
// no paid plan" case, which the dashboard shows differently from a plain
// access denial.
func openCodeZenIsEntitlementError(body []byte) bool {
	errObj, _ := usageJSON(body)["error"].(map[string]any)
	return errObj["type"] == "EntitlementError"
}
