package dashboard

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/samber/lo"

	"9router/proxy/internal/combos/smart"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// Smart select combo: the pattern rules that keep a combo's membership in step
// with the models the gateway knows about.
//
// The split of responsibilities is deliberate. internal/combos/smart owns the
// matching and the diff and knows nothing about this process; this file owns
// the two things only the dashboard can answer — which models exist, and which
// of them a rule may put into a combo — plus the HTTP surface. The resolver
// appears in neither, because a combo's stored model list stays the only thing
// routing reads.

// HandleComboSmartPreview handles POST /api/combos/{id}/smart/preview.
//
// It answers "what would this rule list do?" without writing anything, which is
// what the rules editor calls as the operator types. The body carries the rule
// list being edited; when the field is absent the stored one is previewed.
func (h *DashboardHandler) HandleComboSmartPreview(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing combo id")
		return
	}
	combo, err := h.Repo.GetComboById(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if combo == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "combo not found")
		return
	}

	body, err := readJSONBody(r)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var rules []smart.Rule
	if _, ok := body["matchRules"]; ok {
		rules, err = smart.DecodeRules(body["matchRules"])
	} else {
		rules, err = smart.ParseRules(combo.MatchRules)
	}
	if err != nil {
		writeSmartRuleError(w, "INVALID_RULE", err.Error())
		return
	}
	if code, msg := smart.ValidateAll(rules); code != "" {
		writeSmartRuleError(w, code, msg)
		return
	}

	members, err := comboModels(combo.Models)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	candidates, err := h.smartComboCandidates(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, err := smart.Preview(members, rules, candidates)
	if err != nil {
		writeSmartRuleError(w, "INVALID_RULE", err.Error())
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"combo":      combo.Name,
		"candidates": len(candidates),
		"models":     res.Models,
		"added":      res.Added,
		"removed":    res.Removed,
		"rules":      res.Rules,
	})
}

// HandleComboSmartLoad handles POST /api/combos/{id}/smart/load.
//
// It is the write half of the preview: the operator has a rule list in the
// editor, has seen what it would do, and presses the button to make it so.
// The merge semantics are the point — the models the rules claim are added,
// the models that are no longer claimed by any rule are removed, and the
// models the operator pinned by hand are left exactly where they are. A
// replace would silently drop those pins; an append would leave stale
// rule-owned members behind, which is the very thing the rules exist to
// prevent.
//
// The response reports the delta so the client can show what changed without
// a second round trip.
func (h *DashboardHandler) HandleComboSmartLoad(w http.ResponseWriter, r *http.Request) {
	id := getURLParam(r, "id")
	if id == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing combo id")
		return
	}
	combo, err := h.Repo.GetComboById(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if combo == nil {
		handlerutil.WriteJSONError(w, http.StatusNotFound, "combo not found")
		return
	}
	body, err := readJSONBody(r)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var rules []smart.Rule
	if _, ok := body["matchRules"]; ok {
		rules, err = smart.DecodeRules(body["matchRules"])
	} else {
		rules, err = smart.ParseRules(combo.MatchRules)
	}
	if err != nil {
		writeSmartRuleError(w, "INVALID_RULE", err.Error())
		return
	}
	if code, msg := smart.ValidateAll(rules); code != "" {
		writeSmartRuleError(w, code, msg)
		return
	}
	members, err := comboModels(combo.Models)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	candidates, err := h.smartComboCandidates(r.Context())
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, err := smart.Preview(members, rules, candidates)
	if err != nil {
		writeSmartRuleError(w, "INVALID_RULE", err.Error())
		return
	}
	// Nothing to do is not an error: the operator pressed the button and the
	// rules already describe the membership.
	if len(res.Added) == 0 && len(res.Removed) == 0 {
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"combo": combo.Name, "added": []string{}, "removed": []string{},
			"models": res.Models, "changed": false,
		})
		return
	}
	// The write goes through ReconcileCombo so the membership and the rule
	// snapshots land in one transaction, the same way the automatic half does
	// it: a read-modify-write racing an operator edit would otherwise drop the
	// edit. The rules travel with the models because the snapshot each rule
	// carries is what a later reconcile reads to know which members it owns.
	changed, err := h.Repo.ReconcileCombo(r.Context(), id,
		func(models []string, _ []smart.Rule) ([]string, []smart.Rule, bool) {
			return res.Models, rules, true
		})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !changed {
		// The row vanished between the read above and the write: nothing was
		// written, and saying so beats reporting a delta that never landed.
		handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
			"combo": combo.Name, "added": []string{}, "removed": []string{},
			"models": res.Models, "changed": false,
		})
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"combo":   combo.Name,
		"added":   res.Added,
		"removed": res.Removed,
		"models":  res.Models,
		"changed": true,
	})
}

// decodeComboRules reads the matchRules field of a combo write.
//
// hasRules reports whether the field was present at all: an absent field leaves
// the stored list alone, while a present one replaces it — the same distinction
// the models field already makes. The returned code is empty when the list is
// usable, and a non-empty one names the first problem so the client can point
// at it instead of showing a raw body.
func decodeComboRules(v any) (encoded string, hasRules bool, code, msg string) {
	if v == nil {
		return "", false, "", ""
	}
	rules, err := smart.DecodeRules(v)
	if err != nil {
		return "", true, "INVALID_RULE", err.Error()
	}
	if code, msg := smart.ValidateAll(rules); code != "" {
		return "", true, code, msg
	}
	encoded, err = smart.EncodeRules(rules)
	if err != nil {
		return "", true, "INVALID_RULE", err.Error()
	}
	return encoded, true, "", ""
}

// HandleReconcileSmartCombos handles POST /api/combos/reconcile.
//
// The rules are applied to every combo that has any, or to the one named by
// ?id=. It is the manual half of the feature: the automatic half runs on every
// model write, and this exists for the changes no write announces — a catalogue
// sync that published new models, or an operator who wants to see the effect
// right now.
func (h *DashboardHandler) HandleReconcileSmartCombos(w http.ResponseWriter, r *http.Request) {
	only := strings.TrimSpace(r.URL.Query().Get("id"))
	res, err := h.reconcileSmartCombos(r.Context(), only)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"reconciled": res.Reconciled,
		"added":      res.Added,
		"removed":    res.Removed,
	})
}

// smartReconcileResult summarises one pass over the combos that carry rules.
type smartReconcileResult struct {
	Reconciled int      `json:"reconciled"`
	Added      []string `json:"added"`
	Removed    []string `json:"removed"`
}

// reconcileSmartCombos applies the rules of every combo that has any.
//
// The candidate list is built once and shared: it is the expensive half (a
// connection read, the registry, the custom-model scope), and every combo is
// reconciled against the same snapshot of the gateway. ReconcileCombo then
// takes the write lock per combo, so the candidate building never happens while
// one is held.
func (h *DashboardHandler) reconcileSmartCombos(ctx context.Context, onlyID string) (smartReconcileResult, error) {
	ids, err := h.Repo.ComboIDsWithRules(ctx)
	if err != nil {
		return smartReconcileResult{}, err
	}
	if len(ids) == 0 {
		return smartReconcileResult{}, nil
	}
	if onlyID != "" && !slices.Contains(ids, onlyID) {
		// Nothing to do rather than an error: the combo may simply have no
		// rules, and the caller asked for a reconcile, not a validation.
		return smartReconcileResult{}, nil
	}

	candidates, err := h.smartComboCandidates(ctx)
	if err != nil {
		return smartReconcileResult{}, err
	}

	res := smartReconcileResult{}
	for _, id := range ids {
		if onlyID != "" && id != onlyID {
			continue
		}
		var ruleErr error
		changed, err := h.Repo.ReconcileCombo(ctx, id, func(members []string, rules []smart.Rule) ([]string, []smart.Rule, bool) {
			newMembers, newRules, dirty, err := smart.Reconcile(members, rules, candidates)
			if err != nil {
				ruleErr = err
				return members, rules, false
			}
			if dirty {
				added, removed := stringDiff(members, newMembers)
				res.Added = append(res.Added, added...)
				res.Removed = append(res.Removed, removed...)
			}
			return newMembers, newRules, dirty
		})
		if ruleErr != nil {
			return res, fmt.Errorf("combo %s: %w", id, ruleErr)
		}
		if err != nil {
			return res, err
		}
		if changed {
			res.Reconciled++
		}
	}
	return res, nil
}

// reconcileSmartCombosBestEffort runs a pass after a model write.
//
// The write itself has already succeeded and been answered, so a failed
// reconcile is logged and left for the next trigger or the manual button:
// combo membership is a convenience and must never turn a saved model into a
// 500 the operator has to retry.
func (h *DashboardHandler) reconcileSmartCombosBestEffort(ctx context.Context, reason string) {
	res, err := h.reconcileSmartCombos(ctx, "")
	if err != nil {
		log.Warn("smart-combo", "reconcile failed", "reason", reason, "error", err)
		return
	}
	if res.Reconciled == 0 {
		return
	}
	log.Info("smart-combo", "membership updated",
		"reason", reason, "combos", res.Reconciled,
		"added", len(res.Added), "removed", len(res.Removed))
}

// smartComboCandidates builds the model set the rules may consider.
//
// Two flags per candidate, and they answer different questions:
//
//   - Live: the model still exists. A member that is not live is pruned
//     whatever the pattern says — the row was deleted, the node was removed, or
//     the operator turned the model off.
//   - Addable: a rule may put it into a combo. Gated by reachability, so a
//     provider that lost its connection stops contributing new members while
//     keeping the ones it already put there.
//
// The registry is enumerated for every provider with an active connection, and
// the custom-model scope for every alias that still resolves. Both spellings of
// a registry model are carried (alias and canonical id) so a member is never
// pruned for being written the other way round.
func (h *DashboardHandler) smartComboCandidates(ctx context.Context) ([]smart.Candidate, error) {
	conns, err := h.Repo.GetProviderConnections("", true)
	if err != nil {
		return nil, fmt.Errorf("smart candidates: connections: %w", err)
	}
	active := make(map[string]bool, len(conns))
	for _, c := range conns {
		active[c.Provider] = true
	}
	disabled, err := h.disabledModelsByAlias()
	if err != nil {
		return nil, fmt.Errorf("smart candidates: disabled models: %w", err)
	}
	nodePrefixes, err := h.Repo.GetProviderNodePrefixMap()
	if err != nil {
		return nil, fmt.Errorf("smart candidates: provider nodes: %w", err)
	}
	// prefix -> node id, node id -> prefix, plus the set of aliases that
	// resolve to anything. The reverse map exists because the dashboard keys a
	// node's custom-model rows with the node id (ProviderDetailView stores
	// custom models under storageAlias = the node id) while every reader —
	// /v1/models, the resolver, a combo member — addresses them by the display
	// prefix the operator sees.
	prefixToNode := make(map[string]string, len(nodePrefixes))
	nodeIDToPrefix := make(map[string]string, len(nodePrefixes))
	knownAlias := make(map[string]bool, len(nodePrefixes)+len(providers.KnownProviders))
	for nodeID, prefix := range nodePrefixes {
		if prefix == "" {
			continue
		}
		prefixToNode[prefix] = nodeID
		nodeIDToPrefix[nodeID] = prefix
		knownAlias[prefix] = true
	}
	for providerID := range providers.KnownProviders {
		knownAlias[providerID] = true
		if alias := providers.GetProviderAlias(providerID); alias != "" {
			knownAlias[alias] = true
		}
	}

	out := make([]smart.Candidate, 0, 256)
	for providerID := range active {
		alias := providers.GetProviderAlias(providerID)
		if alias == "" {
			alias = providerID
		}
		off := disabled[alias]
		for _, modelID := range providers.GetProviderModels(providerID) {
			live := true
			if _, isOff := off[modelID]; isOff {
				live = false
			}
			out = append(out, smart.Candidate{
				ID:      alias + "/" + modelID,
				Bare:    modelID,
				AltID:   providerID + "/" + modelID,
				Kind:    registryModelKind(providerID, modelID),
				Addable: live,
				Live:    live,
			})
		}
	}

	custom, err := h.Repo.GetKVScope("customModels")
	if err != nil {
		return nil, fmt.Errorf("smart candidates: custom models: %w", err)
	}
	for key, raw := range custom {
		alias, modelID, mtype := splitCustomModelKey(key)
		if modelID == "" {
			continue
		}
		var entry struct {
			ProviderAlias string `json:"providerAlias"`
			ID            string `json:"id"`
		}
		if err := json.Unmarshal([]byte(raw), &entry); err == nil {
			if entry.ID != "" {
				modelID = entry.ID
			}
			if entry.ProviderAlias != "" {
				alias = entry.ProviderAlias
			}
		}
		if alias == "" {
			continue
		}
		// A node's rows are keyed by its id; the addressable spelling is the
		// display prefix. Normalize so the candidate carries the spelling the
		// rules and the resolver agree on, and keep the node-id spelling as the
		// AltID so a member written that way is never pruned for it.
		nodeID := alias
		if prefix, ok := nodeIDToPrefix[alias]; ok {
			alias = prefix
		} else if !knownAlias[alias] {
			// An alias that resolves to nothing: the node was deleted and its
			// custom-model rows were left behind. The model is not routable,
			// so it is not live and a rule must not keep it in a combo.
			continue
		}
		live := true
		// The dashboard writes disabledModels under the same storage alias the
		// custom rows use — node id for a node, alias otherwise — and /v1/models
		// honours both spellings, so the rules do too.
		if _, isOff := disabled[alias][modelID]; isOff {
			live = false
		}
		if _, isOff := disabled[nodeID][modelID]; isOff {
			live = false
		}
		out = append(out, smart.Candidate{
			ID:      alias + "/" + modelID,
			Bare:    modelID,
			AltID:   lo.Ternary(nodeID != alias, nodeID+"/"+modelID, ""),
			Kind:    customModelKind(mtype),
			Addable: live && aliasReachable(alias, active, prefixToNode),
			Live:    live,
		})
	}
	return out, nil
}

// registryModelKind classifies a registry model the way the auto-family
// generator does: the registry's own kind when it declares one, an embedding
// when the id says so, and chat otherwise.
func registryModelKind(providerID, modelID string) string {
	if kind := providers.GetProviderModelKind(providerID, modelID); kind != "" {
		return kind
	}
	if isEmbeddingModel(modelID) {
		return "embedding"
	}
	return smart.KindLLM
}

// customModelKind maps a stored custom-model type onto a candidate kind. "llm"
// and "chat" are the same thing here: both serve a chat turn.
func customModelKind(mtype string) string {
	kind := strings.ToLower(strings.TrimSpace(mtype))
	switch kind {
	case "", "llm", "chat":
		return smart.KindLLM
	default:
		return kind
	}
}

// splitCustomModelKey reads the "alias|model|type" key of a customModels row.
func splitCustomModelKey(key string) (alias, modelID, mtype string) {
	parts := strings.Split(key, "|")
	switch len(parts) {
	case 3:
		return parts[0], parts[1], parts[2]
	case 2:
		return parts[0], parts[1], "llm"
	default:
		return "", key, "llm"
	}
}

// aliasReachable reports whether an alias can serve a request right now: a node
// with an active connection, or a built-in provider with one.
func aliasReachable(alias string, active map[string]bool, prefixToNode map[string]string) bool {
	if nodeID, ok := prefixToNode[alias]; ok {
		return active[nodeID]
	}
	if providerID, ok := providers.ProviderAliasMap[alias]; ok {
		return active[providerID]
	}
	return active[alias]
}

// writeSmartRuleError emits a typed 400 in the shape the connection-name guard
// established (PROVIDER_NAME_CONFLICT), so a client already handling that shape
// handles a malformed rule the same way.
func writeSmartRuleError(w http.ResponseWriter, code, detail string) {
	handlerutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error":   detail,
		"code":    code,
		"details": detail,
	})
}

// readJSONBody decodes a request body into a map. An empty body is an empty
// map, which is how the preview endpoint is called with nothing to preview.
func readJSONBody(r *http.Request) (map[string]any, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Body.Close() }()
	if strings.TrimSpace(string(body)) == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// stringDiff reports what after has that before lacks, and the reverse.
func stringDiff(before, after []string) (added, removed []string) {
	for _, id := range after {
		if !slices.Contains(before, id) {
			added = append(added, id)
		}
	}
	for _, id := range before {
		if !slices.Contains(after, id) {
			removed = append(removed, id)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
