package chat

import (
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"errors"
	"net/http"
	"strings"
)

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
// (providers.MatchModelPattern, the model resolver's own) and ONE decision
// function (modelAllowed). Handlers never re-derive an allowlist.

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

// effectiveAllowedModels is the ONE place a key's allowlist is read. Both
// dispatch (checkModelAccess) and listing (filterModelsByAccess) go through it.
//
// An empty result means "no allowlist configured" and allows every model, which
// is what keeps every key minted before this feature behaving exactly as it did.
// A nil key (no API-key middleware on the route) also returns empty. A storage
// failure degrades the same way rather than turning into a 500: a broken policy
// read must not take down traffic the key has always been allowed to send.
func (h *ChatHandler) effectiveAllowedModels(key *models.APIKey) ([]string, error) {
	if h == nil || key == nil || key.ID == "" || h.Repo == nil {
		return nil, nil
	}
	patterns, err := h.Repo.GetAllowedModels(key.ID)
	if err != nil {
		log.Warn("chat", "model allowlist read failed, allowing all", "error", err, "apiKeyId", key.ID)
		return nil, nil
	}
	return patterns, nil
}

// allowedModelPatterns is effectiveAllowedModels for callers that hold a key id
// rather than the key itself — the capacity-adapter pool filter runs deep in
// dispatch, long after the request context is gone. It returns nil for an
// empty id, which modelAllowed reads as "no allowlist".
func (h *ChatHandler) allowedModelPatterns(keyID string) []string {
	if h == nil || keyID == "" || h.Repo == nil {
		return nil
	}
	patterns, err := h.Repo.GetAllowedModels(keyID)
	if err != nil {
		log.Warn("chat", "model allowlist read failed, allowing all", "error", err, "apiKeyId", keyID)
		return nil
	}
	return patterns
}

// modelAllowed is the single allow/deny decision. An empty pattern set allows
// everything; otherwise some pattern must match some candidate id.
//
// candidates carries both spellings of the same model — the bare id and the
// `provider/model` form — because a client may request either and an operator
// may write the allowlist as either.
func modelAllowed(patterns []string, candidates ...string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && providers.MatchModelPattern(pattern, candidate) {
				return true
			}
		}
	}
	return false
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

// writeModelAccessError maps a gate error onto the wire: a typed denial is 403,
// anything else is a 400.
func writeModelAccessError(w http.ResponseWriter, err error) {
	if IsModelNotAllowed(err) {
		handlerutil.WriteJSONError(w, http.StatusForbidden, err.Error())
		return
	}
	handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
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
		endpoint := kindEndpoint(kind)
		out = append(out, map[string]any{
			"id":       m.ID,
			"object":   "model",
			"kind":     kind,
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