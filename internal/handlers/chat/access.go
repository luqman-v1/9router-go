package chat

import (
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// SentinelDenyAll aliases db.SentinelDenyAll.
const SentinelDenyAll = db.SentinelDenyAll

// ErrPolicyUnavailable is the typed 503 indicating policy storage or repo failure.
type ErrPolicyUnavailable struct {
	Message string
}

func (e *ErrPolicyUnavailable) Error() string {
	return e.Message
}

// IsPolicyUnavailable reports whether err is ErrPolicyUnavailable.
func IsPolicyUnavailable(err error) bool {
	var target *ErrPolicyUnavailable
	return errors.As(err, &target)
}

// Per-API-key model access control (F-7).
//
// This is per-key POLICY and nothing else: it never changes which provider
// serves a model, never aliases one provider onto another, and never applies to
// a key without an allowlist (AGENTS.md §3).
//
// The invariant that makes the feature safe is that dispatch and listing ask
// the same question through the same code. A client that can list a model but
// not call it (or worse, call one it cannot see) is the bug this file exists to
// prevent, so there is exactly ONE loader (effectiveAllowedModels), ONE matcher
// (providers.MatchAllowPattern) and ONE decision function (modelAllowed).
// Handlers never re-derive an allowlist.
//
// The matcher is deliberately the allowlist one and not the resolver's own
// segment-local providers.MatchModelPattern: `*claude*` has to reach
// `anthropic/claude-sonnet` (#365). Both live in the providers package, side by
// side and documented, so the two globs stay visibly distinct instead of
// quietly drifting apart.

// ErrModelNotAllowed is the typed 403 the handlers translate. Callers match it
// with errors.As; it is deliberately distinct from a resolution failure, which
// is a 400 and means "no such model" rather than "not yours".
type ErrModelNotAllowed struct {
	Model string
}

func (e *ErrModelNotAllowed) Error() string {
	return "model not permitted for this API key: " + e.Model
}

// IsModelNotAllowed reports whether err is the typed access denial.
func IsModelNotAllowed(err error) bool {
	var target *ErrModelNotAllowed
	return errors.As(err, &target)
}

// effectiveAllowedModels is the ONE loader a key's allowlist passes through.
// Both dispatch (checkModelAccess) and listing (filterModelsByAccess) go
// through it, so a client can never list a model it may not call.
//
// The result is the key's own patterns unioned with the ones its plan grants
// (F-3). An empty result means "no allowlist configured" and allows every
// model, which is what keeps every key minted before this feature behaving
// exactly as it did. A nil key (no API-key middleware on the route) also
// returns empty. A storage failure degrades fail-closed with ErrPolicyUnavailable
// (503) rather than failing open.
func (h *ChatHandler) effectiveAllowedModels(key *models.APIKey) ([]string, error) {
	if h == nil || key == nil || key.ID == "" {
		return nil, nil
	}
	if h.Repo == nil {
		log.Error("chat", "nil repo, fail-closed", "apiKeyId", key.ID)
		return nil, &ErrPolicyUnavailable{Message: "Policy service temporarily unavailable"}
	}
	patterns, err := h.Repo.GetAllowedModels(key.ID)
	if err != nil {
		log.Error("chat", "allowlist read failed, fail-closed", "error", err, "apiKeyId", key.ID)
		return nil, &ErrPolicyUnavailable{Message: "Policy service temporarily unavailable"}
	}
	merged, err := h.withPlanPatterns(key.ID, key.PlanID, patterns)
	if err != nil {
		// The plan half of the policy is unknown, so the union is unknown.
		// Dropping the plan silently would leave the key's own rows alone,
		// and a key whose only rows came from its plan would become
		// unrestricted — the same fail-open the own-rows read above avoids.
		log.Error("chat", "plan read failed, fail-closed", "error", err, "apiKeyId", key.ID)
		return nil, &ErrPolicyUnavailable{Message: "Policy service temporarily unavailable"}
	}
	return merged, nil
}

// allowedModelPatterns is the same loader for callers that hold a key id
// rather than the key itself — the capacity-adapter pool filter runs deep in
// dispatch, long after the request context is gone. It returns nil for an
// empty id, which modelAllowed reads as "no allowlist". When keyID is non-empty,
// any repo or storage failure fails closed by returning SentinelDenyAll ("!*").
func (h *ChatHandler) allowedModelPatterns(keyID string) []string {
	if h == nil || keyID == "" {
		return nil
	}
	if h.Repo == nil {
		log.Error("chat", "nil repo, fail-closed", "apiKeyId", keyID)
		return []string{SentinelDenyAll}
	}
	patterns, err := h.Repo.GetAllowedModels(keyID)
	if err != nil {
		log.Error("chat", "allowlist read failed, fail-closed", "error", err, "apiKeyId", keyID)
		return []string{SentinelDenyAll}
	}
	merged, err := h.withPlanPatterns(keyID, nil, patterns)
	if err != nil {
		// Same reason as the own-rows read above: an unknown plan half means
		// the union is unknown, and a key whose only rows came from its plan
		// would otherwise be treated as unrestricted.
		log.Error("chat", "plan read failed, fail-closed", "error", err, "apiKeyId", keyID)
		return []string{SentinelDenyAll}
	}
	return merged
}

// withPlanPatterns widens a key's allowlist with the patterns its plan grants.
//
// The two sides are UNIONED, so the result can only ever add a model, never
// take one away: a plan names what every key on it may call, and the key's own
// rows are how an operator widens that for this one key. An empty list on
// either side contributes nothing, which keeps "no plan" and "no allowlist"
// both reading as "no constraint".
//
// planID may be nil when the caller holds only a key id, in which case the
// plan is fetched with one joined query. A caller that already holds the key
// passes its plan id: an empty one — every key minted before F-3 — skips that
// query entirely, so the common case pays nothing extra.
//
// A failed plan read returns an error rather than an empty plan: the union is
// then unknown, and returning the own rows alone would drop the plan's grant
// without saying so. A key whose only rows came from its plan would read as
// unrestricted, which is the fail-open the caller has to be able to refuse.
//
// A plan that simply does not exist is not an error: a key can name a plan
// that was deleted after the key was pointed at it, and such a key grants
// nothing beyond its own rows.
func (h *ChatHandler) withPlanPatterns(keyID string, planID *string, own []string) ([]string, error) {
	if h.Repo == nil {
		return own, nil
	}
	var planPatterns []string
	if planID != nil && strings.TrimSpace(*planID) != "" {
		plan, err := h.Repo.GetPlan(strings.TrimSpace(*planID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// The plan row is gone, or the database predates the
				// plans table: the key's own rows are all that is
				// left, and that is not a failure.
				return own, nil
			}
			if errors.Is(err, db.ErrNoPlansTable) {
				// A database without the plans table cannot have a
				// key on a plan.
				return own, nil
			}
			return nil, err
		}
		planPatterns = plan.AllowedModels
	} else if planID == nil {
		// The caller holds only a key id, so the plan id is unknown: one joined
		// read answers it. A caller that passed a known-empty plan id is not
		// here, so a key with no plan skips this query entirely.
		plans, err := h.Repo.GetPlansForKeys([]string{keyID})
		if err != nil {
			if errors.Is(err, db.ErrNoPlansTable) {
				// A database created before F-3 has no plans table, and
				// a key in it cannot be on a plan.
				return own, nil
			}
			return nil, err
		}
		planPatterns = plans[keyID].AllowedModels
	}
	if len(planPatterns) == 0 {
		return own, nil
	}
	return unionModelPatterns(own, planPatterns), nil
}

// unionModelPatterns merges two allowlists in stable order, dropping
// duplicates and blanks. Either side may be nil, which means "no constraint"
// and therefore contributes nothing.
func unionModelPatterns(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, m := range list {
			m = strings.TrimSpace(m)
			if m == "" || seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// modelAllowed is the single allow/deny decision.
//
// An empty pattern set — or one holding nothing but blanks — means "no
// allowlist configured" and allows everything, which is what keeps every key
// minted before this feature behaving exactly as it did.
//
// Otherwise the set is parsed into an allowlist and a deny list (#365):
//
//   - `!term` / `-term` entries deny. A deny always wins, even over a positive
//     rule that would have allowed the same model.
//   - A set with no positive entry at all is deny-only: everything not named is
//     allowed, which is how an operator excludes one model without having to
//     enumerate the rest of the catalogue.
//   - Positive entries match as allowlist globs (`*claude*`, `oc/*`), see
//     providers.MatchAllowPattern.
//
// candidates carries both spellings of the same model — the bare id and the
// `provider/model` form — because a client may request either and an operator
// may write the allowlist as either. Deny terms are checked against every
// spelling, so `-haiku` cannot be dodged by requesting the provider-qualified
// id.
func modelAllowed(patterns []string, candidates ...string) bool {
	allows, denies, entries := parseAllowRules(patterns)
	if entries == 0 {
		return true
	}
	for _, deny := range denies {
		for _, candidate := range candidates {
			if providers.MatchDenyEntry(deny, candidate) {
				return false
			}
		}
	}
	if len(allows) == 0 {
		// Deny-only: open except what was denied above.
		return true
	}
	for _, pattern := range allows {
		for _, candidate := range candidates {
			if providers.MatchAllowPattern(pattern, candidate) {
				return true
			}
		}
	}
	return false
}

// parseAllowRules splits an allowlist into its positive patterns and its deny
// terms, and reports how many non-blank entries the list held.
//
// entries counts positives and denies together: it is what distinguishes "no
// allowlist" from "a deny-only allowlist", and only the caller can make that
// call because only the caller knows the list came from a key's policy.
func parseAllowRules(patterns []string) (allows, denies []string, entries int) {
	allows = make([]string, 0, len(patterns))
	denies = make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		if term, ok := providers.IsDenyEntry(pattern); ok {
			denies = append(denies, term)
			entries++
			continue
		}
		allows = append(allows, pattern)
		entries++
	}
	return allows, denies, entries
}

// accessCandidates returns the id spellings a request for modelID can arrive
// as, plus the provider-qualified form resolveModel arrives at, so an
// allowlist written as `gpt-4o` covers an `openai/gpt-4o` call and vice versa.
// It only READS the resolved provider, for matching; it never substitutes one.
func (h *ChatHandler) accessCandidates(modelID string) []string {
	bare := stripModelContextMarker(strings.TrimSpace(modelID))
	out := make([]string, 0, 3)
	add := func(v string) {
		if v == "" {
			return
		}
		for _, existing := range out {
			if existing == v {
				return
			}
		}
		out = append(out, v)
	}

	add(bare)
	add(modelID)
	if info, err := h.resolveModel(bare); err == nil && info != nil && info.Provider != "" {
		add(info.Provider + "/" + info.Model)
	}
	return out
}

// checkModelAccess is the dispatch gate. Callers run it after the bypass path
// (synthetic warmup/keepalive traffic never reaches a provider and so is never
// a real access request) and before any connection selection.
func (h *ChatHandler) checkModelAccess(key *models.APIKey, modelID string) error {
	patterns, err := h.effectiveAllowedModels(key)
	if err != nil {
		return err
	}
	if modelAllowed(patterns, h.accessCandidates(modelID)...) {
		return nil
	}
	return &ErrModelNotAllowed{Model: modelID}
}

// writeModelAccessError maps a gate error onto the wire: a policy failure is 503,
// a typed denial is 403, anything else is a 400.
//
// The 403 envelope comes from the shared writer (handlerutil.WriteJSONError),
// so its type and code are permission_error / insufficient_quota — the same
// pair upstream's ERROR_TYPES[403] produces (open-sse/config/errorConfig.js).
// The message is upstream's exact client-facing sentence, so it names the
// model the client asked for and nothing else — never the key name and never
// the key's allow list.
//
// The 503 policy failure keeps its own shape: it is not an upstream status,
// so it carries a code of its own rather than the status-derived one.
func writeModelAccessError(w http.ResponseWriter, err error) {
	if IsPolicyUnavailable(err) {
		handlerutil.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{
				"message": "Policy service temporarily unavailable",
				"type":    "api_error",
				"code":    "policy_unavailable",
			},
		})
		return
	}
	// errors.As and IsModelNotAllowed ask the same question of the same
	// error type, so this is the same verdict the 403 branch used to get —
	// but the typed error is unwrapped HERE, and the unwrap result is
	// checked, so the branch cannot dereference a nil pointer.
	var denied *ErrModelNotAllowed
	if errors.As(err, &denied) {
		handlerutil.WriteJSONError(w, http.StatusForbidden, modelAccessDeniedMessage(denied.Model))
		return
	}
	handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
}

// modelAccessDeniedMessage is the client-facing sentence for a model-access
// denial. It reproduces upstream decolua/9router's
// `keyAccessDeniedMessage(requested)` from
// src/shared/constants/keyAccess.js verbatim:
//
//	export function keyAccessDeniedMessage(requested) {
//	  return `This API key is not allowed to use model "${requested}"`;
//	}
//
// Upstream calls it at src/sse/services/keyAccess.js:115 and sends it through
// errorResponse(FORBIDDEN, ...) -> buildErrorBody -> ERROR_TYPES[403]
// (open-sse/config/errorConfig.js) = {type: permission_error, code:
// insufficient_quota}. The model is interpolated raw, so it is quoted with
// double quotes and never escaped.
func modelAccessDeniedMessage(model string) string {
	return `This API key is not allowed to use model "` + model + `"`
}

// enforceModelAccess runs the gate for the calling key and writes the response
// itself when the request must not proceed. It reports whether dispatch may
// continue. Every model-taking entrypoint calls this as its first action after
// the bypass check.
func (h *ChatHandler) enforceModelAccess(w http.ResponseWriter, r *http.Request, modelID string) bool {
	if modelID == "" {
		return true
	}
	err := h.checkModelAccess(requestKey(r), modelID)
	if err == nil {
		return true
	}
	if IsModelNotAllowed(err) {
		log.Info("chat", "model denied by api key allowlist", "model", modelID)
	}
	writeModelAccessError(w, err)
	return false
}

// EnforceModelAccess is the exported entry to the same gate
// enforceModelAccess applies inside the chat lane. The media endpoints call
// it: they sit behind the same RequireApiKey middleware, so a restricted key
// that reached them unauthenticated-in-policy was being served anyway. Sharing
// the one decision function is what keeps a model an operator allowed to be
// the same model a client can actually reach, whichever endpoint it uses.
func (h *ChatHandler) EnforceModelAccess(w http.ResponseWriter, r *http.Request, modelID string) bool {
	return h.enforceModelAccess(w, r, modelID)
}

// filterModelsByAccess narrows a published model list to what the calling key
// may dispatch. It reads the allowlist through effectiveAllowedModels — the
// same loader checkModelAccess uses — so the two cannot disagree.
func (h *ChatHandler) filterModelsByAccess(key *models.APIKey, data []ModelInfoObject) ([]ModelInfoObject, error) {
	patterns, err := h.effectiveAllowedModels(key)
	if err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return data, nil
	}
	out := make([]ModelInfoObject, 0, len(data))
	for _, m := range data {
		if modelAllowed(patterns, modelEntryCandidates(m)...) {
			out = append(out, m)
		}
	}
	return out, nil
}

// modelEntryCandidates returns the id spellings a listing entry can be
// requested by, so listing applies the same match dispatch would. A combo
// publishes its name as the id; it is a model id in its own right and is
// matched on that name, never re-derived from its seats — expanding a combo
// here would be policy deciding routing.
func modelEntryCandidates(m ModelInfoObject) []string {
	owner := strings.TrimSpace(m.OwnedBy)
	if owner == "" || owner == "combo" {
		return []string{m.ID}
	}
	return []string{m.ID, owner + "/" + m.ID}
}

// kindEntriesAsModelInfo projects the kind listing's own entry shape onto the
// shared ModelInfoObject the access filter operates on. The provider-kind
// entries are keyed by their provider id, which is both the id and the owner.
func kindEntriesAsModelInfo(data []map[string]any) []ModelInfoObject {
	out := make([]ModelInfoObject, 0, len(data))
	for _, entry := range data {
		id, _ := entry["id"].(string)
		owner, _ := entry["owned_by"].(string)
		if owner == "" {
			owner = id
		}
		out = append(out, ModelInfoObject{ID: id, Object: "model", OwnedBy: owner})
	}
	return out
}

// kindEntriesFromModelInfo is the inverse projection, keeping only what the
// kind endpoint publishes (its own id, kind, owner, endpoint and created
// stamp). Entries the filter dropped never come back.
func kindEntriesFromModelInfo(data []ModelInfoObject, kind string, created int64) []map[string]any {
	out := make([]map[string]any, 0, len(data))
	for _, m := range data {
		entryKind, endpoint := kind, kindEndpoint(kind)
		// The web listing is the one kind that publishes two lanes under one
		// request, so its per-entry kind and endpoint come from the entry
		// itself (`<alias>/search`, `<alias>/fetch`) rather than from the
		// requested kind.
		if kind == "web" {
			switch {
			case strings.HasSuffix(m.ID, "/search"):
				entryKind, endpoint = "webSearch", "/v1/search"
			case strings.HasSuffix(m.ID, "/fetch"):
				entryKind, endpoint = "webFetch", "/v1/web/fetch"
			}
		}
		out = append(out, map[string]any{
			"id":       m.ID,
			"object":   "model",
			"kind":     entryKind,
			"owned_by": m.OwnedBy,
			"endpoint": endpoint,
			"created":  created,
		})
	}
	return out
}

// kindEndpoint is the route the kind listing advertises for a kind. It is the
// same table HandleModelsByKind used inline, hoisted so the filtered rebuild
// cannot drift from the unfiltered one.
func kindEndpoint(kind string) string {
	switch kind {
	case "web":
		return "/v1/search"
	case "image":
		return "/v1/images/generations"
	case "tts":
		return "/v1/audio/speech"
	case "stt":
		return "/v1/audio/transcriptions"
	case "embedding":
		return "/v1/embeddings"
	case "systemone":
		return "/v1/systemone"
	default:
		return "/v1/chat/completions"
	}
}

// requestKey returns the authenticated key for a request, or nil on a route
// with no API-key middleware.
func requestKey(r *http.Request) *models.APIKey {
	if r == nil {
		return nil
	}
	return middleware.GetAuthenticatedApiKey(r)
}
