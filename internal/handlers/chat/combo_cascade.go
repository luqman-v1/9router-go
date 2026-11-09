package chat

import (
	"sort"
	"strings"

	"9router/proxy/internal/pricing"
)

// OrderModelsByCascade ranks a combo's members cheapest first, so the combo
// escalation chain the operator gets is the one they would have arranged by
// hand: try the cheap model, and only spend on a capable one when the cheap
// one cannot serve the turn.
//
// This is an ordering, not a new failure path. The combo loop already walks
// the chain and escalates on every upstream failure; cascade's only job is to
// make the order it walks start from the least expensive candidate. That
// keeps the strategy a one-line addition to applyComboStrategy rather than a
// second dispatch loop to keep in step with the first.
//
// Ranking is by a single blended rate, input + output per million tokens,
// because a request's mix of prompt and completion is not known before the
// model answers. A model with no known price is ranked after every priced
// model rather than dropped: an unpriced member is usually a new or
// private-endpoint model, and putting it last means the combo still has it as
// a last resort instead of silently losing it.
func OrderModelsByCascade(modelsList []string) []string {
	if len(modelsList) <= 1 {
		return modelsList
	}
	order := make([]string, len(modelsList))
	copy(order, modelsList)
	rate := func(entry string) (float64, bool) {
		provider, model := splitCascadeEntry(entry)
		if model == "" {
			return 0, false
		}
		p, ok := pricing.GetPricingForModel(provider, model)
		if !ok {
			return 0, false
		}
		return p.InputPer1M + p.OutputPer1M, true
	}
	sort.SliceStable(order, func(i, j int) bool {
		ri, oki := rate(order[i])
		rj, okj := rate(order[j])
		if oki != okj {
			// Unpriced goes last.
			return oki
		}
		if !oki {
			// Both unpriced: keep the stored order.
			return false
		}
		return ri < rj
	})
	return order
}

// splitCascadeEntry splits a "provider/model" combo entry into its parts.
//
// A custom node can be spelled "nodeID/model" with no provider prefix at all,
// and pricing has no row for that spelling, so an entry that does not split is
// reported as unpriced rather than guessed at.
func splitCascadeEntry(entry string) (provider, model string) {
	provider, model, ok := strings.Cut(entry, "/")
	if !ok || model == "" {
		return "", ""
	}
	return provider, model
}
