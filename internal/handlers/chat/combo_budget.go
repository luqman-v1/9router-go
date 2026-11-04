package chat

import (
	"time"

	"9router/proxy/internal/db"
)

// ComboBudgetCalls is the ceiling on how many requests a combo may serve
// inside one budget window before it stops being dispatched.
//
// A budget is an admission gate, not a rotation strategy: it does not reorder
// candidates, it withholds the combo entirely once the ceiling is reached, so
// the caller falls through to whatever it would have used without the combo.
// That is the whole point — an operator caps a combo because they want the
// spend behind it to stop, not because they want a different model to be
// tried next.
//
// The counter is in memory and process-local, on purpose. A restart is a fresh
// window: a durable counter would make a gateway that was down for a day come
// back with its combo still capped from before the outage, which is a budget
// the operator cannot account for. See timeoutBench for the same reasoning.
const (
	// DefaultBudgetWindowMs is the window a call ceiling spans when the
	// combo does not set its own.
	DefaultBudgetWindowMs = 60 * 60 * 1000
)

// budgetWindow returns the configured window in milliseconds.
func budgetWindow(cfg db.ComboStrategy) int {
	if cfg.BudgetWindowMs > 0 {
		return cfg.BudgetWindowMs
	}
	return DefaultBudgetWindowMs
}

// comboBudgetKey separates one combo's counter from another's.
func comboBudgetKey(comboName string) string {
	if comboName == "" {
		return "__default__"
	}
	return comboName
}

// comboBudgetExhausted reports whether the combo has already spent its call
// ceiling inside the current window, and prunes expired windows as a side
// effect of the same lock.
func (h *ChatHandler) comboBudgetExhausted(comboName string) bool {
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return false
	}
	cfg, ok := settings.ComboStrategies[comboName]
	if !ok || cfg.BudgetCalls <= 0 {
		return false
	}
	key := comboBudgetKey(comboName)
	window := time.Duration(budgetWindow(cfg)) * time.Millisecond
	now := time.Now()

	h.comboBudgetMu.Lock()
	defer h.comboBudgetMu.Unlock()
	if h.comboBudget == nil {
		h.comboBudget = make(map[string]*comboBudgetCounter)
	}
	entry, ok := h.comboBudget[key]
	if !ok || now.Sub(entry.WindowStart) >= window {
		// A fresh (or recycled) window always admits the combo: the counter
		// counts what was spent inside this window, and nothing was spent in
		// one that just started.
		h.comboBudget[key] = &comboBudgetCounter{WindowStart: now, WindowMs: budgetWindow(cfg)}
		return false
	}
	return entry.Count >= cfg.BudgetCalls
}

// recordComboBudgetCall charges one request against the combo's window.
//
// Called after the combo is admitted, so the count reflects dispatched
// requests rather than offered ones. A request that every member refused
// still consumed the operator's budget intent, so it is charged here too —
// the caller records on the path that already knows a dispatch happened.
func (h *ChatHandler) recordComboBudgetCall(comboName string) {
	settings, err := h.Repo.GetSettings()
	if err != nil || settings == nil {
		return
	}
	cfg, ok := settings.ComboStrategies[comboName]
	if !ok || cfg.BudgetCalls <= 0 {
		return
	}
	key := comboBudgetKey(comboName)
	window := time.Duration(budgetWindow(cfg)) * time.Millisecond
	now := time.Now()

	h.comboBudgetMu.Lock()
	defer h.comboBudgetMu.Unlock()
	if h.comboBudget == nil {
		h.comboBudget = make(map[string]*comboBudgetCounter)
	}
	entry, ok := h.comboBudget[key]
	if !ok || now.Sub(entry.WindowStart) >= window {
		entry = &comboBudgetCounter{WindowStart: now, WindowMs: budgetWindow(cfg)}
		h.comboBudget[key] = entry
	}
	entry.Count++
	// Prune other combos' expired windows so a long-lived gateway with many
	// capped combos does not accumulate one dead entry per window per combo.
	//
	// Each entry is expired against the window *it* was created under, not this
	// combo's. Comparing against this combo's window deleted a neighbour's live
	// counter whenever this combo's window was the shorter of the two: a
	// per-hour budget behind a per-minute capped combo lost its whole spend
	// every time the short combo recorded, so the operator's cap quietly
	// reopened. An entry that never recorded a window (zero) is judged by this
	// combo's, which is what it would have been created with.
	for k, v := range h.comboBudget {
		if k == key {
			continue
		}
		w := v.WindowMs
		if w <= 0 {
			w = budgetWindow(cfg)
		}
		if now.Sub(v.WindowStart) >= time.Duration(w)*time.Millisecond {
			delete(h.comboBudget, k)
		}
	}
}

// comboBudgetCounter is one combo's spend inside one window.
//
// WindowMs is the window this entry was created under, carried on the entry
// rather than re-read from settings: another combo's prune pass compares
// against *its* window, so an entry that recorded its own window is expired on
// its own terms instead of a neighbour's.
type comboBudgetCounter struct {
	WindowStart time.Time
	WindowMs    int
	Count       int
}

// singleModelFromCombo rewrites a combo ModelInfo into the single-model shape
// for the first member of the chain, so a withheld combo degrades into the
// same request the client would have made without the combo.
//
// The member is resolved through resolveModelEntry, the same function the
// non-combo path uses, rather than through resolvePrefixProvider or a
// hand-written split:
//
//   - resolvePrefixProvider only resolves provider *node* prefixes. It returns
//     nil for an ordinary registry provider ("openai/gpt-4o"), and the
//     fallback used to store the whole "provider/model" string in both
//     Provider and Model, so the request went upstream with the prefixed id
//     and was answered 400. A combo whose first member is a registry provider
//     is the common case, not an edge one.
//   - resolveModelEntry handles the alias, the connection-map spelling and the
//     provider/model split, and sets Model to the unprefixed upstream id, so a
//     degraded request is byte-identical to the one the client would have made
//     without the combo.
//
// The single-name branch (a combo nested in another combo's list) is left to
// resolveModelEntry as well: it expands the nested combo and returns its first
// concrete model, which is what the client's own request for that name would
// have produced.
func (h *ChatHandler) singleModelFromCombo(info *ModelInfo, augmented []string) *ModelInfo {
	out := &ModelInfo{ComboModels: nil, Strategy: "", StickyLimit: 0, JudgeModel: ""}
	if len(augmented) > 0 {
		if resolved := h.resolveModelEntry(augmented[0]); resolved != nil {
			out.Provider = resolved.Provider
			out.Model = resolved.Model
			out.ConnectionID = resolved.ConnectionID
			return out
		}
		// Resolution refused the entry outright. Keep it as the target so the
		// failure the client sees names the member that could not be resolved,
		// rather than silently dispatching to some other model.
		out.Provider = augmented[0]
		out.Model = augmented[0]
		return out
	}
	if info != nil {
		out.Provider = info.Provider
		out.Model = info.Model
		out.ConnectionID = info.ConnectionID
	}
	return out
}
