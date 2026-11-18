package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Open Design OpenDesign Cloud (open-design.ai) / AMR usage.
//
// Two planes on two hosts: quota reads come from https://amr-api.open-design.ai
// (control) on the control key (`vela_ctrl_…`), never from the model plane.
// A personal account has exactly one workspace; its id is read once per
// control key and cached ~10 min, keyed by the control key (two connections can
// belong to two accounts).
//
// Preflight payload:
//   { codingPlan: { eligible, windows: [{ usedCredits, limitCredits,
//     durationSeconds, resetsAt }] }, balanceUsd }
// Summary payload: { membershipTier, balanceUsd, ... }
//
// Limit/used credits arrive as decimals that can exceed float64 exactness, so
// all arithmetic is big.Rat.

var openDesignControlHost = "https://amr-api.open-design.ai"

// openDesignWorkspaceCache holds the per-control-key workspace read with a TTL.
// Never keyed by provider alone: two connections can belong to two accounts.
var openDesignWorkspaceMu sync.Mutex
var openDesignWorkspaceCache = make(map[string]openDesignWorkspaceEntry)

const openDesignWorkspaceTTL = 10 * time.Minute

// openDesignWorkspaceMax bounds the cache: a control key is client-supplied, so
// an unbounded map keyed by it would grow for as long as the gateway runs.
const openDesignWorkspaceMax = 500

// storeOpenDesignWorkspace inserts entry under controlKey, keeping the cache at
// or below openDesignWorkspaceMax. Expired entries go first; then the oldest
// insertion (map order is random, so "oldest" means the smallest at timestamp).
// Caller holds openDesignWorkspaceMu.
func storeOpenDesignWorkspace(controlKey string, entry openDesignWorkspaceEntry) {
	if _, exists := openDesignWorkspaceCache[controlKey]; !exists && len(openDesignWorkspaceCache) >= openDesignWorkspaceMax {
		now := time.Now()
		for key, cached := range openDesignWorkspaceCache {
			if !now.Before(cached.expire) {
				delete(openDesignWorkspaceCache, key)
			}
		}
		for len(openDesignWorkspaceCache) >= openDesignWorkspaceMax {
			var oldestKey string
			var oldestAt time.Time
			first := true
			for key, cached := range openDesignWorkspaceCache {
				if first || cached.at.Before(oldestAt) {
					oldestKey, oldestAt, first = key, cached.at, false
				}
			}
			if oldestKey == "" {
				return
			}
			delete(openDesignWorkspaceCache, oldestKey)
		}
	}
	openDesignWorkspaceCache[controlKey] = entry
}

// openDesignWorkspaceEntry is one cached workspace read, kept per control key.
type openDesignWorkspaceEntry struct {
	id     string
	at     time.Time
	tier   string
	expire time.Time
}

// fetchOpenDesignUsage fetches the Design Plan allowance windows + wallet
// balance for an account's control key. Provider-specific failures surface as
// messages inside the result (ok=true), matching the other dashboard fetchers.
func fetchOpenDesignUsage(ctx context.Context, data map[string]any) usageResult {
	_, apiKey, psd := usageCreds(data)
	controlKey := controlKeyOfPsd(psd, apiKey)
	if controlKey == "" {
		return usageResult{
			message: "Signed in with an API key: the Design Plan allowance needs the account sign-in.",
		}
	}
	id, tier, err := openDesignWorkspace(ctx, controlKey)
	if err != nil {
		return usageResult{message: err.Error()}
	}

	signalCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	preflightURL := fmt.Sprintf("%s/api/v1/workspaces/%s/billing/preflight", openDesignControlHost, id)
	status, _, out, err := usageGet(signalCtx, preflightURL, map[string]string{
		"Authorization": "Bearer " + controlKey,
		"Accept":        "application/json",
		"User-Agent":    usageUserAgent,
	})
	if err != nil {
		return usageResult{message: fmt.Sprintf("Open Design usage: %v", err)}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return usageResult{
			plan:    openDesignPlanLabel(tier),
			quotas:  map[string]any{},
			message: "Open Design refused the account's sign-in — sign in again.",
		}
	}
	if status < 200 || status >= 300 {
		return usageResult{
			plan:    openDesignPlanLabel(tier),
			quotas:  map[string]any{},
			message: fmt.Sprintf("the Design Plan read answered %d", status),
		}
	}
	payload := usageJSON(out)
	if payload == nil {
		return usageResult{
			plan:    openDesignPlanLabel(tier),
			quotas:  map[string]any{},
			message: "the Design Plan read answered no reading",
		}
	}

	plan := payload["codingPlan"]
	balanceUsd := usageNum(payload["balanceUsd"], math.NaN())
	var summary map[string]any
	if s, ok := payload["summary"].(map[string]any); ok {
		summary = s
	}
	balance := balanceUsd
	if math.IsNaN(balance) && summary != nil {
		balance = usageNum(summary["balanceUsd"], math.NaN())
	}
	balanceStr := ""
	if !math.IsNaN(balance) {
		balanceStr = fmt.Sprintf("$%.2f", balance)
	}

	planStr := openDesignPlanLabel(tier)
	if summary != nil {
		if t := usageStr(summary["membershipTier"]); t != "" {
			planStr = "Design Plan " + strings.ToUpper(t[:1]) + t[1:]
		}
	}

	planEligible := false
	if p, ok := plan.(map[string]any); ok {
		if e, ok := p["eligible"].(bool); ok {
			planEligible = e
		}
	}

	quotas := map[string]any{}
	if p, ok := plan.(map[string]any); ok {
		if windowsRaw, ok := p["windows"].([]any); ok {
			for _, w := range windowsRaw {
				wm, ok := w.(map[string]any)
				if !ok {
					continue
				}
				used := openDesignDecimal(wm["usedCredits"])
				limit := openDesignDecimal(wm["limitCredits"])
				if limit == nil || limit.Sign() <= 0 {
					continue
				}
				// usedPercent = round(max(0, used/limit*100)*10)/10, big.Rat-exact.
				percent := new(big.Rat).Mul(used, big.NewRat(1000, 1))
				percent.Quo(percent, limit)
				percent = percent.Mul(percent, big.NewRat(1, 10))
				pctF, _ := percent.Float64()
				pct := math.Max(0, math.Round(pctF*10)/10)
				span := openDesignWindowName(wm["durationSeconds"])
				resetAt := usageResetTime(wm["resetsAt"])
				usedF, _ := used.Float64()
				limitF, _ := limit.Float64()
				quotas[span] = map[string]any{
					"used":                usedF,
					"total":               limitF,
					"remaining":           math.Max(0, limitF-usedF),
					"remainingPercentage": pct,
					"resetAt":             resetAt,
					"unlimited":           false,
				}
			}
		}
	}
	if !planEligible {
		return usageResult{
			plan:    planStr,
			message: "This account has no Design Plan allowance: its requests draw on the wallet balance.",
			quotas:  quotas,
		}
	}
	return usageResult{
		plan:   planStr,
		quotas: quotas,
		extra:  map[string]any{"balance": balanceStr},
	}
}

// controlKeyOfPsd returns the control key: the PSD controlKey wins, otherwise a
// pasted credential that starts with `vela_ctrl_`.
func controlKeyOfPsd(psd map[string]any, apiKey string) string {
	if psd != nil {
		if v, ok := psd["controlKey"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if strings.HasPrefix(apiKey, "vela_ctrl_") {
		return apiKey
	}
	return ""
}

// openDesignWorkspace reads the current workspace id for a control key, cached
// ~10 min per key.
func openDesignWorkspace(ctx context.Context, controlKey string) (id string, tier string, err error) {
	openDesignWorkspaceMu.Lock()
	cached, ok := openDesignWorkspaceCache[controlKey]
	openDesignWorkspaceMu.Unlock()
	if ok && time.Now().Before(cached.expire) {
		return cached.id, cached.tier, nil
	}

	url := openDesignControlHost + "/api/v1/workspaces/current"
	status, _, out, err := usageGet(ctx, url, map[string]string{
		"Authorization": "Bearer " + controlKey,
		"Accept":        "application/json",
		"User-Agent":    usageUserAgent,
	})
	if err != nil {
		return "", "", fmt.Errorf("the workspaces read answered: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", "", fmt.Errorf("the workspaces read answered %d", status)
	}
	payload := usageJSON(out)
	if payload == nil {
		return "", "", errors.New("the workspaces read answered no reading")
	}
	id = usageStr(payload["activeWorkspaceId"])
	if id == "" {
		id = usageStr(payload["workspaceId"])
	}
	if id == "" {
		return "", "", errors.New("the account has no active workspace")
	}
	tier = usageStr(payload["personalMembershipTier"])

	now := time.Now()
	openDesignWorkspaceMu.Lock()
	storeOpenDesignWorkspace(controlKey, openDesignWorkspaceEntry{
		id: id, at: now, tier: tier, expire: now.Add(openDesignWorkspaceTTL),
	})
	openDesignWorkspaceMu.Unlock()
	return id, tier, nil
}

// openDesignDecimal parses a decimal credit value with big.Rat so values that
// exceed float64 exactness are not rounded away.
func openDesignDecimal(v any) *big.Rat {
	switch n := v.(type) {
	case nil:
		return nil
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return nil
		}
		r, ok := new(big.Rat).SetString(s)
		if !ok {
			return nil
		}
		return r
	case json.Number:
		r, ok := new(big.Rat).SetString(string(n))
		if !ok {
			return nil
		}
		return r
	case float64:
		return new(big.Rat).SetFloat64(n)
	default:
		return nil
	}
}

// openDesignWindowName names a window span seconds: week → 7 days, day → days,
// hour → hours, else minutes (rounded). Matches the vendor card's ordering.
func openDesignWindowName(seconds any) string {
	s := usageNum(seconds, math.NaN())
	if math.IsNaN(s) || s <= 0 {
		return "Allowance"
	}
	const week = 7 * 24 * 3600
	const day = 24 * 3600
	const hour = 3600
	switch {
	case math.Mod(s, week) == 0:
		return fmt.Sprintf("%d days", int(s/week)*7)
	case math.Mod(s, day) == 0:
		return fmt.Sprintf("%d days", int(s/day))
	case math.Mod(s, hour) == 0:
		return fmt.Sprintf("%d hours", int(s/hour))
	default:
		return fmt.Sprintf("%.0f minutes", math.Round(s/60))
	}
}

func openDesignPlanLabel(tier string) string {
	t := strings.TrimSpace(tier)
	if t == "" {
		return "Open Design"
	}
	return "Design Plan " + strings.ToUpper(t[:1]) + t[1:]
}
