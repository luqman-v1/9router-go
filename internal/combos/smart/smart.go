// Package smart holds the pattern rules that keep a combo's membership in step
// with the models the gateway actually knows about.
//
// A rule is a stored pattern plus a snapshot of the model ids it currently
// contributes to its combo ("resolved"). Everything here is pure: no database,
// no HTTP, no clock. The dashboard layer supplies the candidates and persists
// the result, which is what keeps the resolver and /v1/models untouched — a
// combo's stored model list stays the single truth routing reads, and a rule is
// only ever a writer of that list.
//
// Two independent notions decide membership, and conflating them is the mistake
// this package exists to prevent:
//
//   - Addable: a model a rule may put INTO the combo. Gated by reachability (a
//     provider with a connection, a node that still exists) and by kind.
//   - Live: a model that still EXISTS. A member that is no longer live is
//     pruned whatever the pattern says.
//
// A provider that loses its connection keeps its members — they are live, just
// unreachable — but stops contributing new ones. That asymmetry is deliberate:
// absence from a catalogue is weak evidence of death, the same reading
// internal/handlers/dashboard/models_sync.go already applies to deprecations.
//
// Known limitation: a model id the operator pinned by hand AND a rule claims is
// owned by the rule, so editing the pattern removes it. The dashboard prevents
// the common path (the picker never offers a model that is already a member),
// and the rules editor labels managed members, but the API cannot tell a pin
// from a claim when both spell the same id.
package smart

import (
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Matching modes. A rule names one; an empty mode reads as glob so a client
// that omits it gets the behaviour the pattern looks like.
const (
	ModeGlob    = "glob"
	ModeKeyword = "keyword"
	ModeRegex   = "regex"
)

// Rule effects. An exclude rule always wins over an include rule, the same
// precedence the per-key allowlist gives a deny term.
const (
	EffectInclude = "include"
	EffectExclude = "exclude"
)

// RegexPrefix marks a regex pattern. It is required rather than inferred from
// the characters: a model id may legitimately contain regex metacharacters, and
// a pattern silently read as a regex stops matching the model it names.
const RegexPrefix = "re:"

// DefaultMaxMembers bounds how many models one rule may contribute. A pattern
// like "*" selects most of a catalogue, and a combo is a fallback chain: every
// extra member is another slot the gateway may have to try, on a box that may
// have one cpu. 0 means unlimited and is an explicit operator choice.
const DefaultMaxMembers = 20

// KindLLM is the only kind a rule matches unless it sets AllKinds. Embedding,
// image, tts and stt models cannot serve a chat turn, so they are not
// candidates for a fallback chain by default.
const KindLLM = "llm"

// Rule is one stored pattern and the membership it currently owns.
type Rule struct {
	ID         string `json:"id"`
	Pattern    string `json:"pattern"`
	Mode       string `json:"mode"`
	Effect     string `json:"effect"`
	Enabled    bool   `json:"enabled"`
	MaxMembers int    `json:"maxMembers"`
	AllKinds   bool   `json:"allKinds"`
	// Resolved is the snapshot of what this rule put into the combo. It is the
	// rule's claim, and the only thing a rule is allowed to take back out.
	Resolved []string `json:"resolved,omitempty"`
	// Excluded holds ids the operator removed from the combo by hand. A rule
	// never re-adds them, so a manual removal survives every later reconcile.
	Excluded []string `json:"excluded,omitempty"`
}

// Candidate is one addressable model the rules may consider.
type Candidate struct {
	// ID is the spelling a combo stores: "alias/model".
	ID string
	// Bare is the other spelling a client may write: "model". Rules match
	// against both, the same way the per-key allowlist does.
	Bare string
	// AltID is a second qualified spelling of the same model that a stored
	// member may use — the canonical provider id where it differs from the
	// alias ("deepseek/gpt-x" beside "ds/gpt-x"), or a provider node's id
	// beside its display prefix. It exists so a member is never pruned for
	// being spelled the other way round; a rule pattern written with that
	// spelling selects the candidate too, while the membership it adds still
	// lands under ID, so it cannot create a second slot for one model.
	AltID string
	// Kind is KindLLM for chat-capable models and the provider's own kind
	// otherwise.
	Kind string
	// Addable reports whether a rule may put this model into a combo.
	Addable bool
	// Live reports whether the model still exists. A member that is not live
	// is pruned whatever the pattern says.
	Live bool
}

// RulePreview reports what one rule contributes on a dry run.
type RulePreview struct {
	ID      string `json:"id"`
	Matched int    `json:"matched"` // selected by the pattern, before the cap
	Applied int    `json:"applied"` // actually contributed, after the cap
	Capped  bool   `json:"capped"`
}

// PreviewResult is what a dry run reports. Nothing is written.
type PreviewResult struct {
	Models  []string      `json:"models"`
	Added   []string      `json:"added"`
	Removed []string      `json:"removed"`
	Rules   []RulePreview `json:"rules"`
}

// ruleWire is the decode shape of a rule. The fields that carry a default are
// pointers so an absent key can be told apart from an explicit zero: "enabled"
// omitted means enabled, and "maxMembers" omitted means the default cap rather
// than unlimited.
type ruleWire struct {
	ID         string   `json:"id"`
	Pattern    string   `json:"pattern"`
	Mode       string   `json:"mode"`
	Effect     string   `json:"effect"`
	Enabled    *bool    `json:"enabled"`
	MaxMembers *int     `json:"maxMembers"`
	AllKinds   bool     `json:"allKinds"`
	Resolved   []string `json:"resolved"`
	Excluded   []string `json:"excluded"`
}

func (w ruleWire) rule() Rule {
	r := Rule{
		ID:       strings.TrimSpace(w.ID),
		Pattern:  strings.TrimSpace(w.Pattern),
		Mode:     strings.TrimSpace(w.Mode),
		Effect:   strings.TrimSpace(w.Effect),
		AllKinds: w.AllKinds,
	}
	if r.Mode == "" {
		r.Mode = ModeGlob
	}
	if r.Effect == "" {
		r.Effect = EffectInclude
	}
	r.Enabled = w.Enabled == nil || *w.Enabled
	if w.MaxMembers == nil {
		r.MaxMembers = DefaultMaxMembers
	} else {
		r.MaxMembers = *w.MaxMembers
	}
	r.Resolved = cleanIDs(w.Resolved)
	r.Excluded = cleanIDs(w.Excluded)
	return r
}

// Validate reports the first problem with a rule as a stable code plus a
// human-readable message. An empty code means the rule is usable.
func Validate(r Rule) (code, msg string) {
	pattern := strings.TrimSpace(r.Pattern)
	if pattern == "" {
		return "EMPTY_PATTERN", "a rule needs a pattern"
	}
	// The allowlist spells a deny "!term" or "-term"; a rule is a structured
	// object with an effect field, so a LEADING sigil is refused rather than
	// given a second meaning here. A `!` in the middle of a glob is a
	// different thing: it negates one `*`-separated segment, which is how
	// "every provider except this one" is written without one rule per
	// provider. The leading-position refusal stays, because a pattern that
	// opens with a negation has no positive term to select with.
	if strings.HasPrefix(pattern, "!") || strings.HasPrefix(pattern, "-") {
		return "INVALID_RULE_PATTERN",
			`a pattern may not start with "!" or "-"; set effect to "exclude" instead`
	}
	if r.Mode == ModeGlob && strings.Contains(pattern, "!") {
		// A `!` that opens a `*`-separated segment negates it. One buried
		// inside a literal is part of the model id and stays literal, so only
		// the segment-opening position is special — and a pattern that opens
		// with a negation is already refused above, because it has no
		// positive term left to select with.
		for _, seg := range strings.Split(pattern, "*") {
			if strings.HasPrefix(seg, "!") && strings.TrimPrefix(seg, "!") == "" {
				return "INVALID_RULE_PATTERN",
					`a "!" must be followed by the segment it excludes; write "* !term"`
			}
		}
	}
	switch r.Mode {
	case ModeGlob, ModeKeyword:
	case ModeRegex:
		expr, ok := strings.CutPrefix(pattern, RegexPrefix)
		if !ok {
			return "INVALID_REGEX", `a regex pattern must start with "re:"`
		}
		if _, err := regexp.Compile(expr); err != nil {
			return "INVALID_REGEX", fmt.Sprintf("regex does not compile: %v", err)
		}
	default:
		return "INVALID_RULE_MODE", `mode must be one of "glob", "keyword", "regex"`
	}
	switch r.Effect {
	case EffectInclude, EffectExclude:
	default:
		return "INVALID_RULE_EFFECT", `effect must be "include" or "exclude"`
	}
	if r.MaxMembers < 0 {
		return "INVALID_RULE_LIMIT", "maxMembers may not be negative (0 means unlimited)"
	}
	return "", ""
}

// ValidateAll reports the first problem in a rule list.
func ValidateAll(rules []Rule) (code, msg string) {
	for _, r := range rules {
		if code, msg := Validate(r); code != "" {
			return code, msg
		}
	}
	return "", ""
}

// DecodeRules reads a rule list from a request body: a JSON array of rule
// objects, the raw JSON string the column holds, or nil for "no rules". Ids are
// assigned to rules that arrive without one.
func DecodeRules(v any) ([]Rule, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case []Rule:
		out := make([]Rule, 0, len(t))
		for _, r := range t {
			out = append(out, normalizeRule(r))
		}
		assignIDs(out)
		return out, nil
	case string:
		return ParseRules(t)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("smart: encode rules: %w", err)
		}
		var wire []ruleWire
		if err := json.Unmarshal(b, &wire); err != nil {
			return nil, fmt.Errorf("smart: rules must be an array of rule objects: %w", err)
		}
		out := make([]Rule, 0, len(wire))
		for _, w := range wire {
			out = append(out, w.rule())
		}
		assignIDs(out)
		return out, nil
	}
}

// ParseRules reads the stored column. A blank column, "[]" and "null" all mean
// no rules, which is what every combo written before this feature holds.
func ParseRules(raw string) ([]Rule, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil, nil
	}
	var wire []ruleWire
	if err := json.Unmarshal([]byte(trimmed), &wire); err != nil {
		return nil, fmt.Errorf("smart: stored rules unreadable: %w", err)
	}
	out := make([]Rule, 0, len(wire))
	for _, w := range wire {
		out = append(out, w.rule())
	}
	assignIDs(out)
	return out, nil
}

// EncodeRules writes the column form. An empty list encodes as "[]" so the
// early-out query and the default agree.
func EncodeRules(rules []Rule) (string, error) {
	if len(rules) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(rules)
	if err != nil {
		return "", fmt.Errorf("smart: encode rules: %w", err)
	}
	return string(b), nil
}

// HasRules reports whether a stored column carries anything, without parsing
// it. It is the cheap check a caller makes before doing any work at all.
func HasRules(raw string) bool {
	switch strings.TrimSpace(raw) {
	case "", "[]", "null":
		return false
	}
	return true
}

// AutoMembers is the set of models the rules currently own: the union of every
// enabled include rule's snapshot. It is derived, never stored, so the
// dashboard can badge managed members without guessing.
func AutoMembers(rules []Rule) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		if !r.Enabled || r.Effect != EffectInclude {
			continue
		}
		for _, id := range r.Resolved {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Reconcile applies the rules to one combo's membership and reports the new
// model list, the new rule list (snapshots refreshed) and whether anything
// changed. Calling it twice with the same inputs changes nothing the second
// time.
func Reconcile(models []string, rules []Rule, candidates []Candidate) ([]string, []Rule, bool, error) {
	p, err := buildPlan(models, rules, candidates)
	if err != nil {
		return nil, nil, false, err
	}
	return p.models, p.rules, p.dirty, nil
}

// Preview is Reconcile without the write: it reports what would change.
func Preview(models []string, rules []Rule, candidates []Candidate) (PreviewResult, error) {
	p, err := buildPlan(models, rules, candidates)
	if err != nil {
		return PreviewResult{}, err
	}
	return PreviewResult{
		Models:  p.models,
		Added:   p.added,
		Removed: p.removed,
		Rules:   p.previews,
	}, nil
}

// plan is one reconcile pass over a combo.
type plan struct {
	models   []string
	rules    []Rule
	added    []string
	removed  []string
	dirty    bool
	previews []RulePreview
}

func buildPlan(models []string, rules []Rule, candidates []Candidate) (*plan, error) {
	compiled, err := compileAll(rules)
	if err != nil {
		return nil, err
	}
	p := &plan{rules: make([]Rule, len(rules))}
	for i, r := range rules {
		p.rules[i] = cloneRule(r)
	}
	p.models = cleanIDs(models)
	if len(p.models) != len(models) {
		p.dirty = true
	}

	// Deny first: an exclude rule removes a model from every include rule's
	// reach, so a deny can never be outvoted by a broader include.
	deny := make(map[string]bool)
	for i := range compiled {
		c := &compiled[i]
		if !c.rule.Enabled || c.rule.Effect != EffectExclude {
			continue
		}
		for _, cand := range candidates {
			if c.matches(cand) {
				deny[cand.ID] = true
			}
		}
	}

	// Liveness is keyed by every spelling a stored member may use, so a member
	// is never pruned for being written the other way round.
	liveByID := make(map[string]Candidate, len(candidates)*2)
	for _, cand := range candidates {
		if !cand.Live {
			continue
		}
		liveByID[cand.ID] = cand
		if cand.Bare != "" {
			liveByID[cand.Bare] = cand
		}
		if cand.AltID != "" {
			liveByID[cand.AltID] = cand
		}
	}

	// presentCanonical tracks which live models already occupy a combo slot,
	// keyed by the candidate's own id rather than by the spelling the member
	// happens to use.
	presentCanonical := make(map[string]bool, len(p.models))
	for _, m := range p.models {
		if cand, ok := liveByID[m]; ok {
			presentCanonical[cand.ID] = true
		}
	}

	for i := range compiled {
		c := &compiled[i]
		r := &p.rules[i]
		if !r.Enabled || r.Effect != EffectInclude {
			// A frozen contribution stays in the combo: switching a rule off
			// must not silently drop the models it put there.
			p.previews = append(p.previews, RulePreview{
				ID: r.ID, Matched: len(r.Resolved), Applied: len(r.Resolved),
			})
			continue
		}

		// A member the operator pulled out of the combo by hand is recorded
		// FIRST, so the pass that notices the removal is also the pass that
		// stops offering the model back.
		var userRemoved []string
		for _, id := range r.Resolved {
			if !slices.Contains(p.models, id) {
				userRemoved = append(userRemoved, id)
			}
		}
		if len(userRemoved) > 0 {
			r.Excluded = unionIDs(r.Excluded, userRemoved)
			p.dirty = true
		}

		matched := 0
		desired := make([]string, 0, 8)
		// The operator's manual removals are keyed by every spelling the
		// candidate carries, so a member pulled out under the node-id or
		// provider-id spelling is not offered back under the addressable one.
		// The expansion is per candidate: an AltID two candidates share (the
		// test helper sets AltID to the alias) must not exclude the other one,
		// so the check below asks whether THIS candidate is excluded rather
		// than consulting a flattened set.
		excluded := make(map[string]bool, len(r.Excluded)*2)
		for _, id := range r.Excluded {
			excluded[id] = true
		}
		for _, cand := range candidates {
			if !cand.Addable || !cand.Live {
				continue
			}
			if deny[cand.ID] {
				continue
			}
			if !r.AllKinds && cand.Kind != KindLLM {
				continue
			}
			if excluded[cand.ID] || excluded[cand.Bare] ||
				(cand.AltID != "" && excluded[cand.AltID]) {
				continue
			}
			if !c.matches(cand) {
				continue
			}
			matched++
			desired = append(desired, cand.ID)
		}
		// The cap keeps the models that already occupy the combo first, then
		// fills the remaining slots in the order the gateway would try them.
		// Sorting alphabetically instead made the cut arbitrary: a member the
		// operator had deliberately ordered could be dropped in favour of a
		// later one, and which one survived depended on the spelling of the id
		// rather than on the fallback order the combo exists to express.
		sort.Strings(desired)
		capped := false
		if r.MaxMembers > 0 && len(desired) > r.MaxMembers {
			kept := make([]string, 0, r.MaxMembers)
			for _, id := range desired {
				if presentCanonical[id] {
					kept = append(kept, id)
				}
			}
			for _, id := range desired {
				if len(kept) >= r.MaxMembers {
					break
				}
				if !presentCanonical[id] {
					kept = append(kept, id)
				}
			}
			desired = kept
			capped = true
		}

		var toRemove, toAdd []string
		for _, id := range r.Resolved {
			if slices.Contains(desired, id) {
				continue
			}
			cand, alive := liveByID[id]
			if !alive {
				// The model is gone: the row was deleted, the node was
				// removed, or the operator turned it off.
				toRemove = append(toRemove, id)
				continue
			}
			if !cand.Addable {
				// Alive but unreachable — a provider that lost its connection.
				// Absence of reachability is not evidence of death, so the
				// member stays and simply stops being a source of new ones.
				continue
			}
			if slices.Contains(desired, cand.ID) {
				// The claim is spelled differently from the candidate's own
				// id and is still selected under that spelling.
				continue
			}
			toRemove = append(toRemove, id)
		}
		for _, id := range desired {
			// A model already in the combo under ANY of its spellings is one
			// slot, not two: the alternate spelling is there so a member is
			// never pruned for being written the other way round, never so a
			// second rule can add the same model twice.
			if slices.Contains(r.Resolved, id) || presentCanonical[id] {
				continue
			}
			toAdd = append(toAdd, id)
		}
		if len(toRemove) > 0 {
			p.models = removeIDs(p.models, toRemove)
			p.removed = append(p.removed, toRemove...)
			p.dirty = true
		}
		if len(toAdd) > 0 {
			p.models = append(p.models, toAdd...)
			p.added = append(p.added, toAdd...)
			for _, id := range toAdd {
				presentCanonical[id] = true
			}
			p.dirty = true
		}
		r.Resolved = desired
		p.previews = append(p.previews, RulePreview{
			ID: r.ID, Matched: matched, Applied: len(desired), Capped: capped,
		})
	}
	return p, nil
}

// compiled is a rule with its regex already built, so a reconcile compiles
// each pattern once instead of once per candidate.
type compiled struct {
	rule Rule
	re   *regexp.Regexp
}

func compileAll(rules []Rule) ([]compiled, error) {
	out := make([]compiled, 0, len(rules))
	for _, r := range rules {
		c := compiled{rule: cloneRule(r)}
		if r.Mode == ModeRegex {
			expr := strings.TrimPrefix(strings.TrimSpace(r.Pattern), RegexPrefix)
			re, err := regexp.Compile("^(?:" + expr + ")$")
			if err != nil {
				return nil, fmt.Errorf("smart: rule %s: regex: %w", r.ID, err)
			}
			c.re = re
		}
		out = append(out, c)
	}
	return out, nil
}

// matches reports whether the rule's pattern selects the candidate. Every
// spelling the candidate carries is tried, so "deepseek*" and "kenari/deepseek*"
// both reach "kenari/deepseek-v4", and a pattern written with the provider id
// ("deepseek/*") or a node id ("node-cutad/deepseek*") reaches the same model
// the alias spelling reaches. The AltID is guarded because an empty one is not
// a spelling: a keyword would otherwise contain-match everything.
func (c compiled) matches(cand Candidate) bool {
	switch c.rule.Mode {
	case ModeRegex:
		if c.re.MatchString(cand.ID) || c.re.MatchString(cand.Bare) {
			return true
		}
		return cand.AltID != "" && c.re.MatchString(cand.AltID)
	case ModeKeyword:
		term := strings.ToLower(strings.TrimSpace(c.rule.Pattern))
		if strings.Contains(strings.ToLower(cand.ID), term) ||
			strings.Contains(strings.ToLower(cand.Bare), term) {
			return true
		}
		return cand.AltID != "" && strings.Contains(strings.ToLower(cand.AltID), term)
	default:
		if matchGlob(c.rule.Pattern, cand.ID) || matchGlob(c.rule.Pattern, cand.Bare) {
			return true
		}
		return cand.AltID != "" && matchGlob(c.rule.Pattern, cand.AltID)
	}
}

// matchGlob reports whether a model id matches an ALLOWLIST glob: `*` spans
// separators, the comparison is case-insensitive and anchored at both ends,
// and a pattern without `*` is a whole-string match. Everything but `*` is
// literal, so a pattern can never smuggle in regex syntax of its own.
//
// A `!` in front of a `*`-separated segment negates that segment, the same
// reading the per-key allowlist gives "!term": "* !deepseek*" selects every
// model except the deepseek family, which is the one shape a single rule could
// not otherwise express — "every provider except this one" needed one include
// rule per provider before this. The negation is per segment, so it narrows
// the pattern rather than replacing it: a pattern that is nothing but
// negations matches nothing, because there is no positive term left to select
// with.
//
// This is the semantics of providers.MatchAllowPattern (upstream parity with
// decolua/9router keyAccess.js, #365). It lives here rather than there because
// the commit this branch was cut from predates that function: the providers
// package only ships the segment-local MatchModelPattern, whose single `*`
// cannot cross a `/` and therefore cannot express "kenari/deepseek*". The two
// must never diverge — a pattern an operator writes here has to mean what the
// same pattern means in a per-key allowlist — so once providers ships the
// allowlist matcher this body becomes a delegation to it.
func matchGlob(pattern, modelID string) bool {
	pattern = strings.TrimSpace(pattern)
	target := strings.TrimSpace(modelID)
	if pattern == "" || target == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return strings.EqualFold(pattern, target)
	}
	if pattern == "*" {
		return true
	}
	// A negated segment is checked against the whole target rather than built
	// into the regex: Go's regexp has no lookahead, and the reading wanted here
	// is "this literal must not appear anywhere", which a case-insensitive
	// substring test states directly. The positive segments still go through
	// the anchored regex, so a pattern keeps meaning what it meant before.
	//
	// Only a `!` that opens a segment negates. One buried inside a literal
	// ("ds/deep!seek") is part of the model id and stays literal: reading it as
	// a negation would silently match the wrong set, so the segment is put back
	// together before the split decides anything.
	var negatives []string
	var positives []string
	for _, part := range strings.Split(pattern, "*") {
		if strings.HasPrefix(part, "!") {
			negatives = append(negatives, strings.TrimPrefix(part, "!"))
			continue
		}
		positives = append(positives, part)
	}
	if len(negatives) > 0 {
		lower := strings.ToLower(target)
		for _, neg := range negatives {
			if neg == "" {
				continue
			}
			if strings.Contains(lower, strings.ToLower(neg)) {
				return false
			}
		}
	}
	if len(positives) == 0 || (len(positives) == 1 && positives[0] == "") {
		// Nothing but negations selects nothing: there is no term left that
		// says what to match.
		return false
	}
	var b strings.Builder
	b.WriteString("(?i)^")
	for i, part := range positives {
		if i > 0 {
			b.WriteString(".*")
		}
		b.WriteString(regexp.QuoteMeta(part))
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		// Unreachable: every literal run is quoted and `*` is the only
		// metacharacter left. Fail closed rather than allow on a parse error.
		return false
	}
	return re.MatchString(target)
}

func normalizeRule(r Rule) Rule {
	if strings.TrimSpace(r.Mode) == "" {
		r.Mode = ModeGlob
	}
	if strings.TrimSpace(r.Effect) == "" {
		r.Effect = EffectInclude
	}
	if r.MaxMembers == 0 {
		r.MaxMembers = DefaultMaxMembers
	}
	r.Pattern = strings.TrimSpace(r.Pattern)
	r.Resolved = cleanIDs(r.Resolved)
	r.Excluded = cleanIDs(r.Excluded)
	return r
}

func assignIDs(rules []Rule) {
	for i := range rules {
		if rules[i].ID == "" {
			rules[i].ID = fmt.Sprintf("r%d", i+1)
		}
	}
}

func cloneRule(r Rule) Rule {
	out := r
	out.Resolved = slices.Clone(r.Resolved)
	out.Excluded = slices.Clone(r.Excluded)
	return out
}

// cleanIDs trims, drops blanks and dedupes while keeping first-seen order.
func cleanIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func unionIDs(a, b []string) []string {
	return cleanIDs(append(slices.Clone(a), b...))
}

func removeIDs(ids, drop []string) []string {
	if len(drop) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if slices.Contains(drop, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}
