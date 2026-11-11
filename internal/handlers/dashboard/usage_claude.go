package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Claude usage fetcher — port of open-sse/services/usage/claude.js
// (getClaudeUsage). Claude OAuth connections report their 5h / weekly windows
// through the OAuth usage endpoint, not through response headers. `claude` was
// already in usageSupportedProviders, so those connections appeared in the
// quota tracker, but fetchProviderUsage had no case for them and every one
// answered {plan:"claude", quotas:{}} — "Account active. No quota limits
// tracked." — without ever calling Anthropic.

// claudeUsageURL is the OAuth usage endpoint. A var so tests can point it at an
// httptest server.
var claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

const (
	// The OAuth usage endpoint is only honoured for Claude Code's own client
	// identity; the beta flag and UA are what the CLI sends.
	claudeUsageBeta      = "oauth-2025-04-20"
	claudeUsageVersion   = "2023-06-01"
	claudeUsageUserAgent = "claude-cli/2.1.280 (external, cli)"

	// A successful read is reused for this long. The tracker polls every visible
	// connection in one tick and several tabs can be open; the endpoint answers
	// 429 quickly when hammered, while chat traffic on the same token is fine.
	claudeUsageTTL = 60 * time.Second
	// After a 429 the token is not asked again for this long.
	claudeUsage429Cooldown = 3 * time.Minute
	// Bound for the per-token state; the process holds one entry per account.
	claudeUsageMaxEntries = 128
)

type claudeUsageEntry struct {
	result      usageResult // last good read; zero value when none
	hasResult   bool
	expiresAt   time.Time
	cooldownTil time.Time
}

var (
	claudeUsageMu    sync.Mutex
	claudeUsageState = map[string]*claudeUsageEntry{}
)

// claudeUsageKey hashes the token so the credential itself never sits in a map
// key.
func claudeUsageKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func claudeUsageLookup(key string, now time.Time) (entry claudeUsageEntry, fresh bool) {
	claudeUsageMu.Lock()
	defer claudeUsageMu.Unlock()
	e := claudeUsageState[key]
	if e == nil {
		return claudeUsageEntry{}, false
	}
	return *e, e.hasResult && now.Before(e.expiresAt)
}

func claudeUsageUpdate(key string, fn func(e *claudeUsageEntry)) {
	claudeUsageMu.Lock()
	defer claudeUsageMu.Unlock()
	e := claudeUsageState[key]
	if e == nil {
		if len(claudeUsageState) >= claudeUsageMaxEntries {
			// Drop an arbitrary entry; the next read for it just refetches.
			for k := range claudeUsageState {
				delete(claudeUsageState, k)
				break
			}
		}
		e = &claudeUsageEntry{}
		claudeUsageState[key] = e
	}
	fn(e)
}

func fetchClaudeUsage(ctx context.Context, accessToken string) usageResult {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return usageResult{message: "Claude access token not available. Please re-authorize the connection."}
	}

	key := claudeUsageKey(accessToken)
	now := time.Now()
	entry, fresh := claudeUsageLookup(key, now)
	if fresh {
		return entry.result
	}
	if now.Before(entry.cooldownTil) {
		return claudeUsageFallback(entry, "Claude connected. Usage API is rate-limited, try again shortly.")
	}

	fetchURL := claudeUsageURL
	if strings.Contains(fetchURL, "?") {
		fetchURL += "&cedar_ember=1&skip_spend=1"
	} else {
		fetchURL += "?cedar_ember=1&skip_spend=1"
	}

	status, hdr, body, err := usageDo(ctx, http.MethodGet, fetchURL, map[string]string{
		"Authorization":     "Bearer " + accessToken,
		"Accept":            "application/json",
		"anthropic-beta":    claudeUsageBeta,
		"anthropic-version": claudeUsageVersion,
		"User-Agent":        claudeUsageUserAgent,
	}, nil)
	if err != nil {
		return claudeUsageFallback(entry, fmt.Sprintf("Claude connected. Unable to fetch usage: %v", err))
	}

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		// "expired" / "authentication" make the handler refresh the credential
		// and read once more (usageAuthExpiredPatterns).
		return usageResult{message: fmt.Sprintf("Claude usage API authentication expired (%d). Please re-authorize the connection.", status), bare: true}
	case status == http.StatusTooManyRequests:
		wait := claudeUsage429Cooldown
		if ra, convErr := strconv.Atoi(strings.TrimSpace(hdr.Get("Retry-After"))); convErr == nil && ra > 0 && ra <= 900 {
			wait = time.Duration(ra) * time.Second
		}
		claudeUsageUpdate(key, func(e *claudeUsageEntry) { e.cooldownTil = time.Now().Add(wait) })
		return claudeUsageFallback(entry, "Claude connected. Usage API is rate-limited, try again shortly.")
	case status < 200 || status >= 300:
		return claudeUsageFallback(entry, fmt.Sprintf("Claude connected. Usage API temporarily unavailable (%d).", status))
	}

	res := parseClaudeUsage(usageJSON(body))
	if len(res.quotas) > 0 {
		claudeUsageUpdate(key, func(e *claudeUsageEntry) {
			e.result, e.hasResult = res, true
			e.expiresAt = time.Now().Add(claudeUsageTTL)
			e.cooldownTil = time.Time{}
		})
	}
	return res
}

// claudeUsageFallback prefers the last good reading over a transient failure,
// as upstream does: a rate-limited poll should not blank a row that had data.
func claudeUsageFallback(entry claudeUsageEntry, message string) usageResult {
	if entry.hasResult {
		return entry.result
	}
	return usageResult{message: message, bare: true}
}

func parseClaudeUsage(data map[string]any) usageResult {
	if data == nil {
		return usageResult{message: "Claude connected, but the usage response could not be read.", bare: true}
	}
	quotas := map[string]any{}

	if w, ok := data["five_hour"].(map[string]any); ok {
		if row := claudeWindowRow(claudeUtilization(w["utilization"]), w["resets_at"]); row != nil {
			quotas["session (5h)"] = row
		}
	}
	if w, ok := data["seven_day"].(map[string]any); ok {
		if row := claudeWindowRow(claudeUtilization(w["utilization"]), w["resets_at"]); row != nil {
			quotas["weekly (7d)"] = row
		}
	}

	// Model-scoped weekly windows arrive either as seven_day_<model> keys...
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, "seven_day_") || k == "seven_day" {
			continue
		}
		w, ok := data[k].(map[string]any)
		if !ok {
			continue
		}
		if row := claudeWindowRow(claudeUtilization(w["utilization"]), w["resets_at"]); row != nil {
			quotas["weekly "+strings.TrimPrefix(k, "seven_day_")+" (7d)"] = row
		}
	}

	// ...or in limits[] as {kind:"weekly_scoped", percent, resets_at,
	// scope:{model:{display_name}}}. No entry means the account has no such
	// window: the row is omitted, never invented.
	if limits, ok := data["limits"].([]any); ok {
		for _, raw := range limits {
			l, ok := raw.(map[string]any)
			if !ok || usageStr(l["kind"]) != "weekly_scoped" {
				continue
			}
			scope, _ := l["scope"].(map[string]any)
			model, _ := scope["model"].(map[string]any)
			name := strings.ToLower(strings.TrimSpace(usageStr(model["display_name"])))
			if name == "" {
				continue
			}
			if row := claudeWindowRow(claudeUtilization(l["percent"]), l["resets_at"]); row != nil {
				quotas["weekly "+name+" (7d)"] = row
			}
		}
	}

	var extra map[string]any
	if ce, ok := data["cedar_ember"].(map[string]any); ok {
		availableCount := parseClaudeAvailableResetCredits(ce)
		extra = map[string]any{
			"resetCredits": map[string]any{
				"availableCount": availableCount,
			},
		}
	}

	if len(quotas) == 0 {
		return usageResult{plan: "Claude Code", message: "Claude connected, but no usage windows were returned.", extra: extra}
	}
	return usageResult{plan: "Claude Code", quotas: quotas, extra: extra}
}

func parseClaudeAvailableResetCredits(ce map[string]any) int {
	if ce == nil {
		return 0
	}
	grants, ok := ce["grants"].([]any)
	if !ok || len(grants) == 0 {
		return 0
	}
	total := 0
	for _, g := range grants {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		if usable, ok := gm["usable_now"].(bool); ok && !usable {
			continue
		}
		if paused, ok := gm["paused"].(bool); ok && paused {
			continue
		}
		if left, ok := gm["resets_left"].(float64); ok && left > 0 {
			total += int(left)
		} else if leftInt, ok := gm["resets_left"].(int); ok && leftInt > 0 {
			total += leftInt
		}
	}
	return total
}

func claudeUtilization(v any) *float64 {
	if f, ok := usageFiniteNum(v); ok {
		return &f
	}
	return nil
}

// claudeWindowRow renders one window. The endpoint reports utilization as the
// percent USED, so remaining is derived; total is 100 like the Codex rows.
func claudeWindowRow(utilization *float64, resetsAt any) map[string]any {
	if utilization == nil {
		return nil
	}
	used := *utilization
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	remaining := 100 - used
	return map[string]any{
		"used":                used,
		"total":               float64(100),
		"remaining":           remaining,
		"remainingPercentage": remaining,
		"resetAt":             usageResetTimeToNil(resetsAt),
		"unlimited":           false,
	}
}
