package dashboard

import (
	"errors"
	"fmt"
	"strings"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
)

// Model access in the shape the Node dashboard writes (upstream
// src/shared/utils/keyAccess.js). Go already stores the allowlist as one row
// per pattern; this file maps that row set to and from {restricted, allow} so
// a client that speaks either shape reaches the same policy.
//
// There is deliberately no second store: every write goes through
// SetAllowedModels and every read through GetAllowedModels, so the chat gate
// keeps seeing one source of truth. A separate `restricted` column would be a
// second place for the policy to disagree with itself.
//
// The two shapes are NOT the same language, and the bridge has to say so.
// Upstream's allow list is a set of exact resolved identities — its own header
// states "No wildcards, no deny mode". Go's store predates this bridge and is a
// pattern language (#365): `*` globs, and `!term`/`-term` deny. A set with no
// positive entry is deny-only, which means "allow everything except these".
//
// So the same rows mean different policies on each side, and the mapping is
// where that difference has to be resolved rather than papered over:
//
//   - Writing through `access` accepts exact identities only. A `*` or a deny
//     sigil is refused, because storing it would make `restricted: true` mean
//     "may call nothing but this model" while the gate reads it as "may call
//     everything". The flat `models` endpoint keeps its pattern semantics —
//     this narrows the new door, it does not take an existing one away.
//   - Reading reports `restricted` only for a set the Node shape can describe.
//     Rows written through the flat endpoint in pattern syntax are still shown
//     verbatim in that endpoint's own `models` field, but they are not dressed
//     up as a restriction the Node shape would honour — and the expressible
//     rows beside them are not published as if they were the whole policy,
//     because a set containing a glob allows more than its plain entries say.
//   - The policy the gate enforces is the key's rows UNIONED with its plan's
//     (see internal/handlers/chat/access.go). `access` reports that effective
//     policy, because "what may this key call" is the question the field
//     answers, and the key's own rows alone understate it whenever a plan is
//     attached.

// keyAccessInput is the request-side access object. `Restricted` is a pointer
// so a body carrying only `allow` is refused rather than read as "unrestricted"
// — an omitted restricted flag must never widen a key. `Allow` is []any so a
// non-string entry is a 400 rather than silently dropped.
type keyAccessInput struct {
	Restricted *bool `json:"restricted"`
	Allow      []any `json:"allow"`
}

// keyAccess is the response-side access object, matching upstream's shape.
type keyAccess struct {
	Restricted bool     `json:"restricted"`
	Allow      []string `json:"allow"`
}

// accessFromPatterns maps a stored allowlist to the access object.
//
// An empty list is unrestricted. Anything else is restricted — including the
// deny-all sentinel, which is the one state that must not read as unrestricted.
//
// A set holding even one row the Node shape cannot express is reported as
// RESTRICTED with an EMPTY list, never as restricted-with-a-subset and never as
// unrestricted. That direction is the whole point: upstream's own comment says
// a corrupt or unparsable allow list "can
// only ever shrink what a restricted key may call (to nothing); it can never
// turn a restricted key into an unrestricted one" — the same rule has to hold
// for a shape mismatch, or the dashboard would advertise an open key that the
// gate is in fact constraining. The flat `models` field on the same response
// still carries the stored patterns verbatim, so an operator who needs the
// exact rows has them.
func accessFromPatterns(patterns []string) keyAccess {
	if len(patterns) == 0 {
		return keyAccess{Restricted: false, Allow: []string{}}
	}
	allow := nodeExpressibleIdentity(patterns)
	if len(allow) != len(patterns) {
		// At least one row is outside the Node shape — a glob, a deny entry,
		// the deny-all sentinel. The set as a whole is then not a list of
		// models, so the expressible rows among them must NOT be published on
		// their own: `["gpt-4o", "*"]` would be reported as "this key may call
		// gpt-4o" while the gate allows everything, which is the understatement
		// that made the whole field unsafe. The one answer left is the shape
		// upstream uses for "restricted, allows nothing", and it errs toward
		// saying the key is narrower than it is — the direction upstream's own
		// comment promises. The exact rows stay visible in the flat `models`
		// field on the same response.
		return keyAccess{Restricted: true, Allow: []string{}}
	}
	return keyAccess{Restricted: true, Allow: allow}
}

// nodeExpressibleIdentity keeps the entries upstream's shape can carry and
// drops the ones only Go's pattern language understands. A `*` in any position
// is a glob upstream has no meaning for, and a leading `!` or `-` is a deny
// entry its shape has no slot for. Order is preserved, as upstream normalizes
// it.
func nodeExpressibleIdentity(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		entry := strings.TrimSpace(pattern)
		if entry == "" || strings.Contains(entry, "*") {
			continue
		}
		if _, deny := providers.IsDenyEntry(entry); deny {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// validateKeyAccess checks the untrusted object and returns the patterns to
// store. It rejects rather than repairs: a wrong type, an oversized list, a
// missing restricted flag, or an entry the Node shape cannot express is a 400,
// not a silent guess.
//
// The entry check is the one that stops this door from WIDENING a key. The
// store's language is a pattern language, so `["*"]` would be stored as
// "everything matches" — a client asking to restrict a key to one model
// through this endpoint, but sending a wildcard, would get a key the dashboard
// calls restricted and the gate treats as open. Refusing is the only answer
// that keeps the flag honest: the caller can still write patterns through the
// flat `models` endpoint, where the semantics are the endpoint's own subject.
func validateKeyAccess(in *keyAccessInput) ([]string, error) {
	if in == nil {
		return nil, fmt.Errorf("access must be an object { restricted, allow }")
	}
	if in.Restricted == nil {
		return nil, fmt.Errorf("access.restricted must be a boolean")
	}
	allow, err := decodeAllowList(in.Allow)
	if err != nil {
		return nil, err
	}
	cleaned := dedupePatterns(cleanModelPatterns(allow))
	if _, err := validateModelAllowlist(cleaned); err != nil {
		return nil, err
	}
	if rest := nodeExpressibleIdentity(cleaned); len(rest) != len(cleaned) {
		return nil, fmt.Errorf("access.allow carries only exact model ids; wildcards and deny entries belong to the models endpoint")
	}
	if !*in.Restricted {
		// An unrestricted key carries no patterns. Clearing the list is what
		// makes `{"restricted": false, "allow": ["a"]}` mean "open" rather
		// than leaving a stale restriction behind.
		return nil, nil
	}
	if len(cleaned) == 0 {
		// Restricted with nothing allowed: the sentinel is the stored form, so
		// the gate denies every model instead of reading an empty list as
		// "no allowlist, allow all".
		return []string{db.SentinelDenyAll}, nil
	}
	return cleaned, nil
}

// decodeAllowList keeps the entries as `any` so a non-string element is
// reported as the type error it is, instead of failing the whole body with
// "invalid JSON" — the client could not tell which field was wrong.
func decodeAllowList(raw []any) ([]string, error) {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("access.allow must be an array of strings")
		}
		out = append(out, s)
	}
	return out, nil
}

// effectiveAccessPatterns returns the policy the chat gate enforces for one key:
// its own rows unioned with the rows its plan grants. It reproduces the union in
// internal/handlers/chat/access.go (withPlanPatterns) rather than importing it,
// because the dashboard package sits below the chat handler and cannot reach it.
//
// The read goes through GetPlansForKeys — the joined, one-query form — so this
// costs the same whether the caller holds one key or a page of them, and it
// answers "no plan" and "plans table absent" the same way withPlanPatterns
// does: the key's own rows are all that is left, and that is not a failure.
// A real read error is returned, because the union is then unknown and the
// caller has to be able to fail closed — returning the own rows would drop the
// plan's grant silently, and a key whose only rows came from its plan would
// read as unrestricted.
func (h *DashboardHandler) effectiveAccessPatterns(apiKeyID string, own []string) ([]string, error) {
	if h.Repo == nil || apiKeyID == "" {
		return own, nil
	}
	plans, err := h.Repo.GetPlansForKeys([]string{apiKeyID})
	if err != nil {
		if errors.Is(err, db.ErrNoPlansTable) {
			return own, nil
		}
		return nil, err
	}
	plan := plans[apiKeyID]
	if len(plan.AllowedModels) == 0 {
		return own, nil
	}
	return unionModelPatterns(own, plan.AllowedModels), nil
}

// unionModelPatterns merges two allowlists in stable order, dropping duplicates
// and blanks — the same merge the chat gate performs, so the published policy
// and the enforced one name the same models in the same order.
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

// dedupePatterns drops case-insensitive repeats, keeping the first spelling —
// upstream's normalizeAllowList order rule. The exact same list written twice
// must not store two rows that the gate then matches twice.
func dedupePatterns(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		key := strings.ToLower(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}
