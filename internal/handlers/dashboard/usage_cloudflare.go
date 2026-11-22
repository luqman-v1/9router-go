package dashboard

// Cloudflare Workers AI has no quota API. Today's neuron consumption is read
// from the GraphQL Analytics dataset `aiInferenceAdaptiveGroups` and shown
// against the documented 10,000 neurons/day free allocation (reset 00:00 UTC).
// Port of upstream OmniRoute open-sse/services/usage/cloudflareAi.ts.
//
// The account id and date travel as GraphQL variables, never in the query
// text. The API token needs the `Account Analytics: Read` permission on top of
// Workers AI access; without it the card degrades to an explanatory message.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// cloudflareAIUsageURL is a var so tests can point it at an httptest server.
var cloudflareAIUsageURL = "https://api.cloudflare.com/client/v4/graphql"

const cloudflareAIFreeDailyNeurons = 10000

const cloudflareAIUsageQuery = `query WorkersAiDailyUsage($accountTag: string!, $since: Date!) {
  viewer {
    accounts(filter: { accountTag: $accountTag }) {
      aiInferenceAdaptiveGroups(filter: { date_geq: $since }, limit: 100) {
        count
        sum {
          totalNeurons
        }
      }
    }
  }
}`

const cloudflareAIAnalyticsPermissionMessage = "Cloudflare API token lacks the Account Analytics: Read permission needed for neuron usage."

func fetchCloudflareAiUsage(ctx context.Context, apiKey string, psd map[string]any) usageResult {
	accountID := strings.TrimSpace(usageStr(psd["accountId"]))
	if accountID == "" {
		return usageResult{message: "Cloudflare Account ID missing. Add it to the connection to view usage."}
	}
	token := strings.TrimSpace(apiKey)
	if token == "" {
		return usageResult{message: "Cloudflare API token not available. Add a token to view usage."}
	}

	limit := float64(cloudflareAIFreeDailyNeurons)
	override := usageNum(psd["dailyNeuronLimit"], 0)
	if override > 0 {
		limit = override
	}

	status, payload, err := usagePost(ctx, cloudflareAIUsageURL, map[string]any{
		"query": cloudflareAIUsageQuery,
		"variables": map[string]any{
			"accountTag": accountID,
			"since":      time.Now().UTC().Format("2006-01-02"),
		},
	}, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if err != nil {
		return usageResult{message: fmt.Sprintf("Cloudflare usage error: %v", err)}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return usageResult{message: cloudflareAIAnalyticsPermissionMessage}
	}
	if status < 200 || status >= 300 {
		return usageResult{message: fmt.Sprintf("Cloudflare analytics API error (%d)", status)}
	}
	if payload == nil {
		return usageResult{message: "Cloudflare analytics response was not JSON."}
	}
	if errs, ok := payload["errors"].([]any); ok && len(errs) > 0 {
		first := ""
		if e, ok := errs[0].(map[string]any); ok {
			first = usageStr(e["message"])
		}
		if cloudflareAIIsAuthError(first) {
			return usageResult{message: cloudflareAIAnalyticsPermissionMessage}
		}
		if first == "" {
			first = "unknown error"
		}
		return usageResult{message: "Cloudflare analytics query error: " + first}
	}

	data, _ := payload["data"].(map[string]any)
	viewer, _ := data["viewer"].(map[string]any)
	accounts, _ := viewer["accounts"].([]any)
	var account map[string]any
	if len(accounts) > 0 {
		account, _ = accounts[0].(map[string]any)
	}
	groups, _ := account["aiInferenceAdaptiveGroups"].([]any)

	var neurons, requests float64
	for _, g := range groups {
		row, _ := g.(map[string]any)
		if sum, ok := row["sum"].(map[string]any); ok {
			if n := usageNum(sum["totalNeurons"], 0); n > 0 {
				neurons += n
			}
		}
		if c := usageNum(row["count"], 0); c > 0 {
			requests += c
		}
	}

	now := time.Now().UTC()
	resetAt := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	used := math.Round(neurons)
	remaining := math.Max(limit-used, 0)
	remainingPct := 100.0
	if limit > 0 {
		remainingPct = math.Round(math.Max(0, math.Min(100, remaining/limit*100))*100) / 100
	}

	quotas := map[string]any{
		"neurons": map[string]any{
			"name":        "Neurons (daily)",
			"displayName": "Neurons (daily)",
			"used":        used,
			"total":       limit,
			// No absolute "remaining": the dashboard reads that field as a
			// percentage, so only remainingPercentage carries that signal.
			"remainingPercentage": remainingPct,
			"resetAt":             resetAt,
			"unlimited":           false,
		},
		"requests": map[string]any{
			"name":        "Requests (today)",
			"displayName": "Requests (today)",
			"used":        math.Round(requests),
			"total":       float64(0),
			"resetAt":     resetAt,
			"unlimited":   true,
		},
	}

	plan := "Workers AI · best-effort (free tier 10k neurons/day)"
	if override > 0 {
		plan = fmt.Sprintf("Workers AI (%s neurons/day)", formatCloudflareNeuronLimit(limit))
	}
	return usageResult{plan: plan, quotas: quotas, extra: map[string]any{"resetDate": resetAt}}
}

func cloudflareAIIsAuthError(msg string) bool {
	lower := strings.ToLower(msg)
	for _, needle := range []string{
		"not authorized", "not authorised", "unauthorized", "unauthorised",
		"permission", "forbidden", "authentication",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func formatCloudflareNeuronLimit(limit float64) string {
	s := strconv.FormatInt(int64(math.Round(limit)), 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
