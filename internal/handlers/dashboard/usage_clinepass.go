package dashboard

// ClinePass quota — GET https://api.cline.bot/api/v1/users/me/plan/usage-limits
// Port of open-sse/services/usage/clinepass.js (getClinepassUsage).
//
// The endpoint answers percent-used windows rather than token counts:
//
//	{ success: true, data: { limits: [{ type, percentUsed, resetsAt }] } }
//	type ∈ five_hour | weekly | monthly
//
// ClinePass accepts two credentials on the same route: a plain API key
// (Bearer clp_…) and the Cline OAuth WorkOS JWT (Bearer workos:<JWT>, sent
// with the Cline client-identity headers). Upstream prefers the API key when
// both are present, and so does this fetcher.

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/updater"
)

// clinePassUsageURL is a var so tests can point it at an httptest server.
var clinePassUsageURL = "https://api.cline.bot/api/v1/users/me/plan/usage-limits"

const clinePassPlan = "ClinePass"

// clinePassWindows fixes the label and order of the three reported windows.
var clinePassWindows = []struct {
	kind  string
	label string
}{
	{"five_hour", "Session (5h)"},
	{"weekly", "Weekly (7d)"},
	{"monthly", "Monthly"},
}

func fetchClinePassUsage(ctx context.Context, accessToken, apiKey string) usageResult {
	headers := clinePassUsageHeaders(accessToken, apiKey)
	if headers == nil {
		return usageResult{message: "ClinePass access token or API key not available."}
	}

	status, _, out, err := usageGet(ctx, clinePassUsageURL, headers)
	if err != nil {
		return usageResult{message: fmt.Sprintf("ClinePass usage error: %v", err)}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return usageResult{plan: clinePassPlan, message: "ClinePass authentication expired. Please re-authorize."}
	}
	if status < 200 || status >= 300 {
		detail := strings.TrimSpace(string(out))
		if len(detail) > 120 {
			detail = detail[:120]
		}
		msg := fmt.Sprintf("ClinePass usage API error (%d)", status)
		if detail != "" {
			msg += ": " + detail
		}
		return usageResult{plan: clinePassPlan, message: msg}
	}

	var payload clinePassUsagePayload
	if err := json.Unmarshal(out, &payload); err != nil {
		return usageResult{message: "ClinePass usage response was not JSON."}
	}

	byType := make(map[string]clinePassLimit, len(payload.Data.Limits))
	for _, limit := range payload.Data.Limits {
		if limit.Type != "" {
			byType[limit.Type] = limit
		}
	}

	quotas := make(map[string]any, len(clinePassWindows))
	for _, w := range clinePassWindows {
		if limit, ok := byType[w.kind]; ok {
			quotas[w.label] = clinePassQuotaRow(limit.PercentUsed, limit.ResetsAt)
		}
	}
	if len(quotas) == 0 {
		return usageResult{
			plan:    clinePassPlan,
			message: "ClinePass connected. No usage limits reported for this plan.",
		}
	}
	return usageResult{plan: clinePassPlan, quotas: quotas}
}

type clinePassUsagePayload struct {
	Data struct {
		Limits []clinePassLimit `json:"limits"`
	} `json:"data"`
}

type clinePassLimit struct {
	Type        string `json:"type"`
	PercentUsed any    `json:"percentUsed"`
	ResetsAt    any    `json:"resetsAt"`
}

// clinePassUsageHeaders mirrors upstream buildAuthHeaders: an API key rides
// plain Bearer with no client identity, an OAuth token goes through the same
// workos: prefix and Cline identity headers the catalogue and chat lane use.
func clinePassUsageHeaders(accessToken, apiKey string) map[string]string {
	if key := strings.TrimSpace(apiKey); key != "" {
		return map[string]string{
			"Accept":        "application/json",
			"Authorization": "Bearer " + key,
		}
	}
	token := strings.TrimSpace(accessToken)
	if token == "" {
		return nil
	}
	headers := providers.ClineIdentityHeaders(updater.CurrentVersion, map[string]string{"Accept": "application/json"})
	headers["Authorization"] = "Bearer " + clineAuthHeader(token)
	return headers
}

// clinePassQuotaRow renders one percent-used window. claudeWindowRow already
// produces exactly that shape (used/total 100/remaining/resetAt), so the
// mapping is shared rather than duplicated.
func clinePassQuotaRow(percentUsed, resetsAt any) map[string]any {
	used := usageNum(percentUsed, 0)
	return claudeWindowRow(&used, resetsAt)
}
