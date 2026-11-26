package dashboard

import (
	"context"
	json "encoding/json/v2"
	"strings"

	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
)

// Codex plan auto-sync, ported from diegosouzapw/OmniRoute#20178 ("Auto-Sync
// Changed ChatGPT Subscription Tier from Live Usage to Database").
//
// chatgptPlanType is written once at OAuth/import time (pkce.go) and never
// refreshed, so an account that upgraded from free to plus/pro kept its stored
// "free" until it was re-connected by hand. Live Codex usage reports the tier
// the account holds right now, so the read is also the cheapest place to notice
// the change: no extra request, and it rides on a refresh the dashboard already
// makes every tick.
//
// What this deliberately does not do: it does not change routing. The Go plan
// resolution (media.codexPlanType) prefers the token's chatgpt_plan_type claim
// and only falls back to this stored field, so the write keeps the stored row
// from disagreeing with the account instead of gating any request.

const codexPlanTypeKey = "chatgptPlanType"

// validCodexPlans is the set of tiers a live Codex usage read may write onto a
// connection row. A value outside it ("unknown", "", a provider-specific alias)
// must never clobber a stored tier, since an upgraded account whose read glitched
// would otherwise be downgraded to a placeholder.
var validCodexPlans = map[string]bool{
	"free":       true,
	"plus":       true,
	"pro":        true,
	"team":       true,
	"enterprise": true,
	"edu":        true,
}

// syncCodexPlanFromUsage writes the live plan Codex usage reported onto the
// connection row when it differs from what is stored. It is a no-op when the
// connection is not a Codex connection, when the read returned no plan or a plan
// outside validCodexPlans (a quota failure or an unrecognized tier must not blank
// or corrupt a good value), or when the row already carries the reported tier —
// the common case, since most refreshes see no change.
//
// The payload is re-marshaled from the map the caller already read the
// connection from, so a concurrent writer's change is lost only in the narrow
// window between that read and this write — the same window every other
// background writer in this package (persistUsageTokens, the node sync) carries.
func (h *DashboardHandler) syncCodexPlanFromUsage(ctx context.Context, conn *models.ProviderConnection, data map[string]any, plan string) {
	if h == nil || h.Repo == nil || conn == nil || data == nil {
		return
	}
	// Codex-only: another provider's row may carry the same field but is never
	// written by this sync.
	if conn.Provider != "codex" {
		return
	}
	plan = strings.ToLower(strings.TrimSpace(plan))
	if !validCodexPlans[plan] {
		return
	}

	psd, _ := data["providerSpecificData"].(map[string]any)
	if psd == nil {
		psd = map[string]any{}
	}
	if !codexPlanDiffers(psd, plan) {
		return
	}

	psd[codexPlanTypeKey] = plan
	data["providerSpecificData"] = psd
	encoded, err := json.Marshal(data)
	if err != nil {
		log.Warn("usage", "encode connection data for plan sync failed", "provider", conn.Provider, "conn", conn.ID, "error", err)
		return
	}
	if err := h.Repo.UpdateConnectionData(ctx, conn.ID, string(encoded)); err != nil {
		log.Warn("usage", "sync codex plan type failed", "conn", conn.ID, "plan", plan, "error", err)
	}
}

// codexPlanDiffers reports whether writing plan would change the stored payload.
// A non-string stored value counts as different: the field is a string by
// contract, so overwriting it is the repair.
func codexPlanDiffers(psd map[string]any, plan string) bool {
	current, ok := psd[codexPlanTypeKey].(string)
	return !ok || current != plan
}
