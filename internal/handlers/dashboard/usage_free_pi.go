package dashboard

// free-pi quota — GET {origin}/me/stats
//
// Read-only port of dennisonbertram/free-pi-cli src/usage-tool.ts (the
// free_pi_usage tool and /usage command both format the same response).
//
// Unlike most providers here, free-pi reports *credits* rather than a dollar
// budget. The upstream deliberately stopped showing free usage as money (the
// allowance's dollar value is not disclosed to users), so this maps credits and
// tokens and leaves cost fields out rather than inventing a figure.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
)

// freePiStatsURL is the quota endpoint, derived per connection so a relayed or
// stubbed base URL reports from its own route. Vars only so tests can point them
// at a local server.
var freePiStatsURLFor = freePiStatsURLFromBase

// freePiStatsURLFromBase maps ".../v1/chat/completions" to ".../me/stats".
func freePiStatsURLFromBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/v1/chat/completions", "/chat/completions", "/v1"} {
		if trimmed := strings.TrimSuffix(base, suffix); trimmed != base {
			return trimmed + "/me/stats"
		}
	}
	if base == "" {
		return providers.FreePiOrigin + "/me/stats"
	}
	return base + "/me/stats"
}

// freePiStats is the subset of GET /me/stats the dashboard renders. Several
// fields were added over time, so they are pointers: a missing field must render
// as absent, not as zero.
type freePiStats struct {
	Tier                  string   `json:"tier"`
	SpentUSDToday         *float64 `json:"spent_usd_today"`
	CapUSDToday           *float64 `json:"cap_usd_today"`
	PromptTokensToday     *int64   `json:"prompt_tokens_today"`
	CompletionTokensToday *int64   `json:"completion_tokens_today"`
	CreditCredits         *int64   `json:"credit_credits"`
	RemainingCreditsToday *int64   `json:"remaining_credits_today"`
	AllowanceCredits      *int64   `json:"allowance_credits"`
	RefillAt              string   `json:"refill_at"`
	Lane                  string   `json:"lane"`
	Lifetime              struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
	} `json:"lifetime"`
}

// fetchFreePiUsage reads the account's remaining allowance.
func fetchFreePiUsage(ctx context.Context, apiKey, baseURL string) usageResult {
	if apiKey == "" {
		return usageResult{message: "free-pi: no credential on this connection"}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, freePiStatsURLFor(baseURL), nil)
	if err != nil {
		return usageResult{message: fmt.Sprintf("free-pi: %v", err)}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := usageHTTPClient.Do(req)
	if err != nil {
		return usageResult{message: fmt.Sprintf("free-pi: %v", err)}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return usageResult{message: fmt.Sprintf("free-pi: read response: %v", err)}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// The credential is a long-lived JWT with no refresh path, so a 401 here
		// is the same terminal state the executor reports: sign in again.
		return usageResult{message: "free-pi: credential expired — sign in again"}
	}
	if resp.StatusCode != http.StatusOK {
		return usageResult{message: fmt.Sprintf("free-pi: HTTP %d", resp.StatusCode)}
	}

	var stats freePiStats
	if err := json.Unmarshal(raw, &stats); err != nil {
		return usageResult{message: fmt.Sprintf("free-pi: parse response: %v", err)}
	}

	return usageResult{
		plan:   planOrFallback(stats.Tier, "free"),
		quotas: freePiQuotas(stats),
		extra:  freePiExtra(stats),
	}
}

// freePiQuotas builds the used/total rows the dashboard draws. Only the credit
// pair is a real quota; tokens are reported as extra detail because upstream
// does not publish a token ceiling.
func freePiQuotas(stats freePiStats) map[string]any {
	quotas := map[string]any{}

	if stats.RemainingCreditsToday != nil && stats.AllowanceCredits != nil {
		total := *stats.AllowanceCredits
		used := total - *stats.RemainingCreditsToday
		if used < 0 {
			used = 0
		}
		quotas["daily"] = map[string]any{
			"used":  used,
			"total": total,
			"unit":  "credits",
		}
		if stats.RefillAt != "" {
			quotas["daily"].(map[string]any)["resetsAt"] = stats.RefillAt
		}
	}

	if stats.CreditCredits != nil {
		quotas["purchased"] = map[string]any{
			"remaining": *stats.CreditCredits,
			"unit":      "credits",
		}
	}

	// A "slow lane" means upstream is serving this account outside the fast path.
	// Surfacing it explains a latency change the user would otherwise blame on us.
	if stats.Lane == "fallback" {
		quotas["lane"] = map[string]any{
			"current": "fallback",
			"note":    "upstream is serving this account on the slow lane",
		}
	}
	return quotas
}

// freePiExtra carries the token counters and any dollar fields upstream still
// sends. Kept out of quotas so they render as text, not as a progress bar.
func freePiExtra(stats freePiStats) map[string]any {
	extra := map[string]any{}
	if stats.PromptTokensToday != nil && stats.CompletionTokensToday != nil {
		extra["tokensToday"] = map[string]any{
			"prompt":     *stats.PromptTokensToday,
			"completion": *stats.CompletionTokensToday,
		}
	}
	if stats.Lifetime.PromptTokens != nil && stats.Lifetime.CompletionTokens != nil {
		extra["lifetimeTokens"] = map[string]any{
			"prompt":     *stats.Lifetime.PromptTokens,
			"completion": *stats.Lifetime.CompletionTokens,
		}
	}
	if stats.RefillAt != "" {
		extra["refillAt"] = stats.RefillAt
	}
	return extra
}

// planOrFallback keeps an empty upstream tier from rendering as a blank badge.
func planOrFallback(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// freePiFetchModelsFromClientVersion reads the live model catalog from GET
// /client-version and returns it in the modelItem shape writeConnectionModels
// uses. The endpoint is unauthenticated and account-independent.
func freePiFetchModelsFromClientVersion(ctx context.Context, apiKey string, psd map[string]any) ([]map[string]any, error) {
	baseURL := ""
	if psd != nil {
		baseURL, _ = psd["baseUrl"].(string)
	}

	endpoint := freePiStatsURLFor(baseURL)
	// freePiStatsURLFor maps to /me/stats; we need /client-version.
	endpoint = strings.TrimSuffix(endpoint, "/me/stats") + "/client-version"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("free-pi models: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := usageHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("free-pi models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("free-pi models: HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("free-pi models: read: %w", err)
	}

	var cv struct {
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &cv); err != nil {
		return nil, fmt.Errorf("free-pi models: parse: %w", err)
	}

	items := make([]map[string]any, 0, len(cv.Models))
	for _, m := range cv.Models {
		if m.ID == "" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		items = append(items, map[string]any{
			"id":   m.ID,
			"name": name,
		})
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("free-pi models: catalog is empty")
	}

	return items, nil
}
