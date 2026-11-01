package dashboard

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"9router/proxy/internal/customjson"
	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
)

// A custom provider node persists under a synthetic id —
// "openai-compatible-chat-<suffix>", "anthropic-compatible-<suffix>" — that is
// its model-id prefix, the provider column of its connections, and the key of
// every custom model, alias and disabled-model list attached to it. Upstream
// always fills <suffix> with a random uuid, which makes the id unreadable in
// logs, in usage rows and in a user's own configuration. A custom suffix lets
// the operator name it instead.
//
// The suffix is therefore an identifier, not a label: everything downstream
// parses it, and the readers are prefix matches and "SplitN(entry, "/", 2)".
// The rules below are what keeps a hand-written suffix from producing an id
// those readers mis-handle.

// nodeIDSuffixPattern is the allowed shape of a custom suffix. It is the same
// character class the dashboard already uses for connection names
// (longTokenPattern in connections.go), extended to allow a leading dot so a
// dotted release label ("v2.1") stays writable.
var nodeIDSuffixPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// maxProviderNodeIDLength bounds the composed id. The suffix itself is capped
// by its own pattern; this is the whole-id ceiling the SQLite primary key and
// the URL path (/api/provider-nodes/{id}) have to carry comfortably.
const maxProviderNodeIDLength = 96

// nodeIDPrefixes are the literals every reader of a custom node id matches on
// (strings.HasPrefix in resolution, live_catalog, connection probes, icon
// lookup). A suffix may not begin with one of them: "openai-compatible-chat-x"
// must not be mistaken for a node id the operator named "chat-x".
var nodeIDPrefixes = []string{"openai-compatible", "anthropic-compatible", "custom-embedding"}

// composeProviderNodeID builds the storage id for a node of nodeType/apiType
// with the given custom suffix. An empty suffix falls back to the random id
// upstream generates, which is what every node created before this field
// existed carries.
func composeProviderNodeID(nodeType, apiType, suffix string) string {
	literal, _ := providerNodeLiterals(nodeType, apiType)
	if suffix == "" {
		suffix = randomNodeSuffix()
	}
	return literal + "-" + suffix
}

// randomNodeSuffix is the fallback tail for a node created without one: a
// uuid, exactly what upstream's generateId() produces.
func randomNodeSuffix() string {
	return uuid.New().String()
}

// providerNodeSuffixOf returns the id tail for a composed id and whether that
// tail is the generated fallback rather than something the user typed. The
// dashboard needs both to show the field: a uuid is not editable text, and
// re-rendering it as a suffix would invite the user to "fix" a value they
// never chose.
func providerNodeSuffixOf(id string) (suffix string, generated bool) {
	for _, literal := range providerNodeLiteralsForID(id) {
		if !strings.HasPrefix(id, literal+"-") {
			continue
		}
		tail := strings.TrimPrefix(id, literal+"-")
		return tail, isGeneratedNodeSuffix(tail)
	}
	// An id that is not in the shape the create handler composes — a node row
	// written by another build, or a hand-edited database — has no suffix to
	// show, and presenting its raw tail as editable text would invite an edit
	// that cannot round-trip.
	return "", true
}

// providerNodeLiteralsForID lists the id literals that can precede a suffix,
// longest first so "openai-compatible-chat-" is preferred over
// "openai-compatible-" and the apiType segment is never mistaken for the tail.
func providerNodeLiteralsForID(id string) []string {
	// "custom-json" carries its own hyphen, so the generic one-segment cut
	// below would read its apiType as "custom".
	if customLiteral := "openai-compatible-" + customjson.APIType; strings.HasPrefix(id, customLiteral+"-") {
		return []string{customLiteral, "anthropic-compatible", "custom-embedding", "openai-compatible"}
	}
	if apiType, _, ok := strings.Cut(strings.TrimPrefix(id, "openai-compatible-"), "-"); ok &&
		(apiType == "chat" || apiType == "responses") {
		return []string{"openai-compatible-" + apiType, "anthropic-compatible", "custom-embedding", "openai-compatible"}
	}
	return []string{"anthropic-compatible", "custom-embedding", "openai-compatible"}
}

// currentSuffixOf reports the suffix a node id already carries, or an empty
// string when its tail is the generated uuid. Callers use it to answer with
// whatever the id holds after a no-op edit, so the response cannot claim a
// suffix the stored id does not actually have.
func currentSuffixOf(id string) string {
	suffix, generated := providerNodeSuffixOf(id)
	if generated {
		return ""
	}
	return suffix
}

// isGeneratedNodeSuffix reports whether a tail is the uuid fallback, using the
// canonical text form rather than a shape guess so a user suffix that happens
// to look like a uuid is still reported as theirs.
func isGeneratedNodeSuffix(suffix string) bool {
	parsed, err := uuid.Parse(suffix)
	return err == nil && parsed.String() == suffix
}

// providerNodeLiterals returns the id literal and the default base URL for a
// node type, mirroring upstream src/app/api/provider-nodes/route.js: the
// OpenAI literal pins the apiType, the other two carry no shape segment.
func providerNodeLiterals(nodeType, apiType string) (literal, defaultBaseURL string) {
	switch nodeType {
	case "anthropic-compatible":
		return "anthropic-compatible", "https://api.anthropic.com/v1"
	case "custom-embedding":
		return "custom-embedding", "https://api.openai.com/v1"
	default:
		// A custom JSON endpoint is a complete URL the operator must name;
		// there is no default to fall back to.
		if apiType == customjson.APIType {
			return "openai-compatible-" + apiType, ""
		}
		if !isOpenAICompatibleAPIType(apiType) {
			apiType = "chat"
		}
		return "openai-compatible-" + apiType, "https://api.openai.com/v1"
	}
}

// isOpenAICompatibleAPIType reports whether apiType is one an
// openai-compatible node may store (upstream provider-nodes routes).
func isOpenAICompatibleAPIType(apiType string) bool {
	return apiType == "chat" || apiType == "responses" || apiType == customjson.APIType
}

// validateNodeIDSuffix checks one user-supplied suffix and reports why it is
// unusable. It returns the trimmed suffix on success and an empty string with
// an error otherwise, so the handler writes one message the dashboard shows
// verbatim instead of a bare 400.
func validateNodeIDSuffix(raw string) (string, error) {
	suffix := strings.TrimSpace(raw)
	if suffix == "" {
		return "", nil
	}
	if len(suffix) > maxProviderNodeIDLength {
		return "", fmt.Errorf("URL suffix is too long (max %d characters)", maxProviderNodeIDLength)
	}
	if !nodeIDSuffixPattern.MatchString(suffix) {
		return "", errors.New("URL suffix may only contain letters, numbers, dots, hyphens and underscores, and must start with a letter or number")
	}
	for _, literal := range nodeIDPrefixes {
		if strings.HasPrefix(strings.ToLower(suffix), literal) {
			return "", fmt.Errorf("URL suffix must not start with %q", literal)
		}
	}
	return suffix, nil
}

// resolveProviderNodeID validates the requested suffix and confirms the id it
// composes is free. currentID is the node being updated: the composed id equals
// it for any suffix that changes nothing, and an empty suffix is read as "keep
// the id I already have" rather than as a request for a new random one — an edit
// that renames nothing must never roll a generated uuid.
//
// Returns the id to store and the suffix that produced it, so the dashboard can
// render the composed id back to the user.
func (h *DashboardHandler) resolveProviderNodeID(w http.ResponseWriter, nodeType, apiType, suffix, currentID string) (id, resolvedSuffix string, ok bool) {
	if strings.TrimSpace(suffix) == "" && currentID != "" {
		existing, _, err := h.Repo.GetProviderNodeByID(currentID)
		if err != nil {
			handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
			return "", "", false
		}
		if existing == nil {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "provider node not found")
			return "", "", false
		}
		return currentID, currentSuffixOf(currentID), true
	}

	resolvedSuffix, err := validateNodeIDSuffix(suffix)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return "", "", false
	}
	id = composeProviderNodeID(nodeType, apiType, resolvedSuffix)
	if len(id) > maxProviderNodeIDLength {
		handlerutil.WriteJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("provider id is too long (max %d characters)", maxProviderNodeIDLength))
		return "", "", false
	}
	if id == currentID {
		return id, resolvedSuffix, true
	}
	existing, _, err := h.Repo.GetProviderNodeByID(id)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return "", "", false
	}
	if existing != nil {
		writeNodeIDConflict(w, id)
		return "", "", false
	}
	return id, resolvedSuffix, true
}

// writeNodeIDConflict emits the typed 409 the name guard established, so a
// client already handling PROVIDER_NAME_CONFLICT handles this shape too.
func writeNodeIDConflict(w http.ResponseWriter, id string) {
	writeNameConflict(w, "PROVIDER_NODE_ID_CONFLICT", id,
		"the provider id "+id+" is already used by another custom endpoint; choose a different URL suffix")
}

// applyProviderNodeRename moves an existing node when the requested suffix
// composes a different id, carrying its connections, custom models, aliases,
// disabled-model list, combos and history with it.
//
// A renamed node keeps a different URL from the one the dashboard is currently
// showing, so the caller receives the new id and reports it back instead of
// leaving the page pointed at an id that no longer resolves.
func (h *DashboardHandler) applyProviderNodeRename(w http.ResponseWriter, currentID, newID string) (string, bool) {
	if newID == currentID {
		return currentID, true
	}
	if _, err := h.Repo.RenameProviderNode(currentID, newID); err != nil {
		if errors.Is(err, db.ErrProviderNodeIDTaken) {
			writeNodeIDConflict(w, newID)
			return "", false
		}
		if errors.Is(err, db.ErrProviderNodeNotFound) {
			handlerutil.WriteJSONError(w, http.StatusNotFound, "provider node not found")
			return "", false
		}
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return "", false
	}
	return newID, true
}
