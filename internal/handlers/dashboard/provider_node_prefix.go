package dashboard

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// maxDerivedPrefixLength caps a prefix this package derives from the URL
// suffix. The suffix pattern allows 64 characters, which is a legal prefix but
// not a usable one: the prefix sits in front of every model id the node
// publishes, so a long suffix would produce ids longer than the node-id
// fallback this fill exists to replace.
const maxDerivedPrefixLength = 24

// prefixTokenAttempts bounds the random-token loop. Each draw is 40 bits and
// is only rejected by a registry alias or another node's prefix — a handful of
// owners out of a huge space — so a handful of draws never realistically runs
// out. The bound exists so a pathological database cannot spin forever.
const prefixTokenAttempts = 5

// randomPrefixToken is the namespace handed to a chat-compatible node whose
// prefix was left blank and whose URL suffix could not serve as one: eight
// base32 characters, 40 bits of entropy. It is what the operator gets instead
// of a 36-character uuid or a 31-to-59-character node id, which is the whole
// point of filling the field at all.
func randomPrefixToken() string {
	var buf [5]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand has no practical failure mode on the platforms this
		// ships to. Returning "" hands the decision back to the caller, which
		// falls through to the node-id namespace — the behaviour before this
		// fill existed, never a weaker token.
		return ""
	}
	// Lowercase base32: the same alphabet the dashboard's other generated
	// values read as, and safe inside a "<prefix>/<model>" id.
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:]))
}

// fillBlankPrefix gives a node that was created or edited without a prefix a
// namespace of its own, and returns the prefix to store.
//
// A non-blank prefix is the operator's and passes through untouched — the
// conflict guard already ran on it in the handler, so re-checking here would
// only double the work. A blank one is filled from the URL suffix, because
// that is the one short, meaningful string the operator already chose for
// this node, and only then from a random token.
//
// The suffix is skipped when it is longer than maxDerivedPrefixLength, and
// whenever claiming it would shadow a real provider: a suffix is unique per
// composed node id, not per prefix, so two node types may legally share one
// (openai-compatible-chat-foo and anthropic-compatible-foo), and a suffix like
// `gh` would otherwise answer every gh/* request from this node instead of
// GitHub Copilot. Both refusals are what providerNodePrefixConflict exists to
// catch, so the token draw reuses it rather than duplicating the rule.
//
// excludeID is the node being edited, so a node whose stored prefix already
// equals its own suffix is not refused for colliding with itself.
//
// The result is stored, never re-derived on read: a prefix that changed under
// a stored combo would orphan every "<prefix>/<model>" that names this node.
// Returns "" only when no candidate could be claimed, which leaves the node
// publishing under its own id — the behaviour before this fill, not a
// regression.
func (h *DashboardHandler) fillBlankPrefix(prefix, suffix, excludeID string) string {
	if prefix != "" {
		return prefix
	}
	if suffix != "" && len(suffix) <= maxDerivedPrefixLength &&
		h.providerNodePrefixConflict(suffix, excludeID) == "" {
		return suffix
	}
	for range prefixTokenAttempts {
		token := randomPrefixToken()
		if token != "" && h.providerNodePrefixConflict(token, excludeID) == "" {
			return token
		}
	}
	return ""
}
