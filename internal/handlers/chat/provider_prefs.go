package chat

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"encoding/json/jsontext"
	json "encoding/json/v2"

	"github.com/samber/lo"
)

// providerPreferences is the request-level provider routing object, the
// contract OpenRouter's API made standard: a client steers which providers
// may serve this turn without the operator changing any combo. The zero
// value is inert — a body without the object, or one that fails to decode,
// leaves every resolution path exactly as it was, so a client that does not
// send one pays nothing for the feature existing.
type providerPreferences struct {
	// Order tries these providers first, in this sequence. Entries outside
	// the list keep their relative order behind the listed ones.
	Order []string `json:"order"`
	// Only restricts candidates to these providers.
	Only []string `json:"only"`
	// Ignore excludes these providers.
	Ignore []string `json:"ignore"`
	// AllowFallbacks, when explicitly false, turns Order into a hard
	// allowlist: nothing outside it may serve the request. Left nil it
	// defaults to true, which keeps Order a preference rather than a
	// restriction.
	AllowFallbacks *bool `json:"allow_fallbacks"`
}

// providerPreferencesKey is the body field the object arrives under. It is
// a 9router-go client-side routing hint, not an upstream request field, so
// it is stripped from the body before forwarding.
const providerPreferencesKey = "provider"

// nodePrefixToIDMap inverts the stored node map (node id → display prefix)
// into the prefix → node id shape the preferences filter matches on. One
// query per request that carries preferences; a caller without custom
// nodes gets nil and the static alias path answers every lookup.
func (h *ChatHandler) nodePrefixToIDMap() map[string]string {
	byID := h.providerNodePrefixMap()
	if len(byID) == 0 {
		return nil
	}
	out := make(map[string]string, len(byID))
	for id, prefix := range byID {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix != "" {
			out[prefix] = strings.ToLower(strings.TrimSpace(id))
		}
	}
	return out
}

// parseProviderPreferences reads the provider object off a request body.
// The bytes pre-check comes first: a body that never mentions the field is
// not re-parsed, which keeps the common path at zero cost. A malformed
// object decodes to the inert default rather than failing the request —
// routing preferences are a hint, and a typo in one must not take a
// working request down.
func parseProviderPreferences(body []byte) providerPreferences {
	if !bytes.Contains(body, []byte(`"`+providerPreferencesKey+`"`)) {
		return providerPreferences{}
	}
	var envelope struct {
		Provider providerPreferences `json:"provider"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return providerPreferences{}
	}
	return envelope.Provider
}

// stripProviderPreferencesField removes the client-side provider object
// from a body that carries one, so the field never reaches an upstream
// provider, where a strict OpenAI-compatible endpoint would reject an
// unknown top-level field. Returns the original slice untouched when there
// is nothing to remove.
//
// The removal is a token-level splice through jsontext, not a map
// round-trip: a body decoded into map[string]any turns every number into
// float64 (9007199254740993 comes back as 9007199254740992), and a
// re-marshal is free to reorder keys, so byte-stable prefix caching fails
// on exactly the bodies this feature touches. Streaming copy keeps every
// other token verbatim, and dropping one member never touches its
// neighbours.
func stripProviderPreferencesField(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"`+providerPreferencesKey+`"`)) {
		return body
	}
	dec := jsontext.NewDecoder(bytes.NewReader(body))
	tok, err := dec.ReadToken()
	if err != nil || tok.Kind() != '{' {
		return body
	}
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out)
	if err := enc.WriteToken(tok); err != nil {
		return body
	}
	for dec.PeekKind() != 0 {
		tok, err := dec.ReadToken()
		if err != nil {
			return body
		}
		if tok.Kind() == '}' {
			if err := enc.WriteToken(tok); err != nil {
				return body
			}
			break
		}
		// At the top level of an object, a string token is a member name.
		// The provider member is dropped whole — name and value — and every
		// other member is copied with its value read as one raw value, so
		// nested content is never token-walked.
		if tok.Kind() == '"' && tok.String() == providerPreferencesKey {
			if _, err := dec.ReadValue(); err != nil {
				return body
			}
			continue
		}
		if err := enc.WriteToken(tok); err != nil {
			return body
		}
		value, err := dec.ReadValue()
		if err != nil {
			return body
		}
		if err := enc.WriteValue(value); err != nil {
			return body
		}
	}
	return out.Bytes()
}

// canonical renders the preferences as deterministic bytes for the cache
// keys: a struct marshal, so the field order is fixed and two clients that
// spelled the same steer differently (key order, whitespace) share one key.
// An inert preference set renders to nil, and nil adds nothing to a key —
// a steer-less request keeps exactly its historical cache key.
func (p providerPreferences) canonical() []byte {
	if !p.active() {
		return nil
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return encoded
}

// active reports whether any preference was set, so a caller that did not
// ask for steering skips every later step.
func (p providerPreferences) active() bool {
	return len(p.Order) > 0 || len(p.Only) > 0 || len(p.Ignore) > 0 || p.AllowFallbacks != nil
}

// filterCandidates applies the preferences to a resolved candidate list —
// a combo's flattened members, or a one-entry single-model list — and
// returns the survivors in decision order. The contract when nothing
// survives is an error, not a silent fall through to the unfiltered set:
// a caller that said "only these providers" must never be served by a
// provider it excluded.
func (p providerPreferences) filterCandidates(candidates []string, prefixToNode map[string]string) ([]string, error) {
	if !p.active() || len(candidates) == 0 {
		return candidates, nil
	}
	only := providerIDSet(p.Only)
	ignore := providerIDSet(p.Ignore)
	orderSet := providerIDSet(p.Order)
	order := providerIDList(p.Order)
	hardOrder := p.AllowFallbacks != nil && !*p.AllowFallbacks && len(order) > 0

	kept := lo.Filter(candidates, func(entry string, _ int) bool {
		if len(only) > 0 && !entryMatchesAny(only, entry, prefixToNode) {
			return false
		}
		if entryMatchesAny(ignore, entry, prefixToNode) {
			return false
		}
		return !hardOrder || entryMatchesAny(orderSet, entry, prefixToNode)
	})

	kept = orderCandidates(kept, order, prefixToNode)
	if len(kept) == 0 {
		return nil, fmt.Errorf("no provider left to route to: the request's provider preferences (%s) exclude every candidate", p.summary())
	}
	return kept, nil
}

// allowsSingle reports the error a single-model request carries when its
// one resolved provider is excluded, and nil when it is permitted. Order
// alone never excludes a single-model request: there is nothing to reorder.
func (p providerPreferences) allowsSingle(providerID string, prefixToNode map[string]string) error {
	if !p.active() {
		return nil
	}
	id := normalizeProviderToken(providerID)
	if len(p.Only) > 0 && !spellingMatches(p.Only, id, prefixToNode) {
		return fmt.Errorf("provider %q is not in the request's provider preferences (only %s)", id, p.summary())
	}
	if spellingMatches(p.Ignore, id, prefixToNode) {
		return fmt.Errorf("provider %q is excluded by the request's provider preferences (%s)", id, p.summary())
	}
	if p.AllowFallbacks != nil && !*p.AllowFallbacks && len(p.Order) > 0 && !spellingMatches(p.Order, id, prefixToNode) {
		return fmt.Errorf("provider %q is outside the request's provider order with fallbacks disabled (%s)", id, p.summary())
	}
	return nil
}

// summary renders the active preferences for an error message, so the
// client sees which steer emptied the candidate set.
func (p providerPreferences) summary() string {
	var parts []string
	if len(p.Only) > 0 {
		parts = append(parts, "only="+strings.Join(p.Only, ","))
	}
	if len(p.Ignore) > 0 {
		parts = append(parts, "ignore="+strings.Join(p.Ignore, ","))
	}
	if len(p.Order) > 0 {
		parts = append(parts, "order="+strings.Join(p.Order, ","))
	}
	if p.AllowFallbacks != nil && !*p.AllowFallbacks {
		parts = append(parts, "allow_fallbacks=false")
	}
	return strings.Join(parts, " ")
}

// normalizeProviderToken folds a provider spelling to its canonical id:
// trimmed, lowercased, and run through the alias table so "oa" and "openai"
// are one name. Unknown spellings pass through unchanged, which is what
// lets a custom provider-node id match itself.
func normalizeProviderToken(token string) string {
	return resolveProviderAlias(strings.ToLower(strings.TrimSpace(token)))
}

func providerIDSet(tokens []string) map[string]bool {
	set := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		if id := normalizeProviderToken(token); id != "" {
			set[id] = true
		}
	}
	return set
}

func providerIDList(tokens []string) []string {
	return lo.Uniq(lo.FilterMap(tokens, func(token string, _ int) (string, bool) {
		id := normalizeProviderToken(token)
		return id, id != ""
	}))
}

// entryProviderID reports the provider id a "provider/model" entry
// dispatches through. prefixToNode maps a custom provider-node prefix to
// that node's id, which is the provider id node-prefixed entries resolve
// to; without a hit the prefix itself is the id (a canonical provider id
// or an alias of one).
func entryProviderID(entry string, prefixToNode map[string]string) string {
	prefix, _ := splitProviderModel(entry)
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if node, ok := prefixToNode[prefix]; ok && node != "" {
		return node
	}
	return normalizeProviderToken(prefix)
}

// entryMatchesAny reports whether a candidate entry is named by the token
// set, under either of its spellings: the provider id it dispatches to, or
// the literal prefix the client wrote. The prefix spelling matters for a
// custom provider node, whose client-facing prefix ("bn/model") is not its
// node id — an exclude that only knew the id would fail open.
func entryMatchesAny(set map[string]bool, entry string, prefixToNode map[string]string) bool {
	if len(set) == 0 {
		return false
	}
	if set[entryProviderID(entry, prefixToNode)] {
		return true
	}
	prefix, _ := splitProviderModel(entry)
	return set[normalizeProviderToken(prefix)]
}

// spellingMatches is entryMatchesAny for the single-model path, where the
// entry's spelling is the resolved provider id and any registered prefix of
// that node.
func spellingMatches(tokens []string, id string, prefixToNode map[string]string) bool {
	set := providerIDSet(tokens)
	if set[id] {
		return true
	}
	for prefix, node := range prefixToNode {
		if node == id && set[normalizeProviderToken(prefix)] {
			return true
		}
	}
	return false
}

// orderCandidates stably moves entries whose provider appears in order to
// the front, in the order's sequence; unlisted entries keep their relative
// order behind them. A stable sort matters: equal-rank entries must not be
// shuffled by Go's unstable sort.
func orderCandidates(entries, order []string, prefixToNode map[string]string) []string {
	if len(order) == 0 || len(entries) < 2 {
		return entries
	}
	rank := func(entry string) int {
		if i := lo.IndexOf(order, entryProviderID(entry, prefixToNode)); i >= 0 {
			return i
		}
		prefix, _ := splitProviderModel(entry)
		if i := lo.IndexOf(order, normalizeProviderToken(prefix)); i >= 0 {
			return i
		}
		return len(order)
	}
	out := make([]string, len(entries))
	copy(out, entries)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// splitProviderModel splits a "provider/model" entry into its two parts.
// An entry without a slash returns it whole as the provider, which is the
// shape resolveModelEntry itself produces for a bare provider.
func splitProviderModel(entry string) (provider, model string) {
	if i := strings.Index(entry, "/"); i >= 0 {
		return entry[:i], entry[i+1:]
	}
	return entry, ""
}
