package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"9router/proxy/internal/log"
)

// Keyword matching lets one preset cover a whole model family across providers
// and combos instead of one armed id at a time. A preset carries keywords; any
// model id matching one of them resolves to that preset, so "deepseek" alone
// covers providera/deepseek, providerx/deepseek-4-pro, deepseek-v4, and every
// combo leg that carries one of those ids.
//
// Modes, per keyword:
//
//	word     the keyword is a whole token or a run of tokens: "deepseek"
//	         matches "providerx/deepseek-4-pro" and "deepseek-v4" but not
//	         "deepseekia". This is the default.
//	partial  a case-insensitive substring: "seek" matches "deepseek".
//	regex    an RE2 pattern matched unanchored and case-insensitively unless
//	         the pattern opts out with (?-i). RE2 cannot backtrack, so a
//	         hostile pattern costs linear time rather than hanging the
//	         request path.
//
// Precedence when several rules match one request, highest first:
//
//  1. an exact model/combo binding — the pre-keyword behaviour, unchanged.
//     An exact binding stays authoritative even when its preset prompt is
//     empty: binding a model to an empty preset means "inject nothing here".
//  2. a keyword on the id the client sent, before a keyword on a forwarded
//     leg id — the request's primary id outranks what it resolves to.
//  3. among keyword hits on the same id, the longest keyword wins; a length
//     tie goes to the lowest preset id, so the answer never depends on map
//     iteration order.
//
// Keyword rules live inside the preset, so deleting a preset removes its rules
// with it, and the resolution snapshot is rebuilt after any jailbreak write.
// The request path does no queries and compiles no patterns: it reads one
// immutable in-memory snapshot.

// JailbreakKeyword is one match rule attached to a preset.
type JailbreakKeyword struct {
	Text string `json:"text"`
	// Mode is "", "partial" or "regex". Empty means the default word mode.
	Mode string `json:"mode,omitempty"`
}

// Keyword modes for JailbreakKeyword.Mode.
const (
	JailbreakKeywordWord    = ""        // whole token or token run (default)
	JailbreakKeywordPartial = "partial" // case-insensitive substring
	JailbreakKeywordRegex   = "regex"   // RE2 pattern
)

// Bounds keep a typo from becoming a rule that matches everything, and keep the
// per-request scan proportional to what an operator actually configured.
const (
	maxJailbreakKeywords     = 20
	maxJailbreakKeywordChars = 200
)

// jailbreakDelimiters bound a word-mode keyword: a match must start and end on
// one of these or on the string edge.
const jailbreakDelimiters = "/-_:. "

// NormalizeJailbreakKeywords validates a preset's keywords and returns the
// canonical form to store. Word and partial keywords are lowercased because
// matching is case-insensitive; a regex is kept byte-for-byte, since
// lowercasing would corrupt escapes and character classes such as \D or [^A-Z].
func NormalizeJailbreakKeywords(in []JailbreakKeyword) ([]JailbreakKeyword, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxJailbreakKeywords {
		return nil, fmt.Errorf("too many keywords: %d (max %d)", len(in), maxJailbreakKeywords)
	}
	out := make([]JailbreakKeyword, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, kw := range in {
		text := strings.TrimSpace(kw.Text)
		if text == "" {
			return nil, fmt.Errorf("keyword text is empty")
		}
		if len(text) > maxJailbreakKeywordChars {
			return nil, fmt.Errorf("keyword %q is too long: %d chars (max %d)", text, len(text), maxJailbreakKeywordChars)
		}
		switch kw.Mode {
		case JailbreakKeywordWord, JailbreakKeywordPartial:
			text = strings.ToLower(text)
		case JailbreakKeywordRegex:
			if _, err := regexp.Compile("(?i)" + text); err != nil {
				return nil, fmt.Errorf("invalid regex keyword %q: %w", text, err)
			}
		default:
			return nil, fmt.Errorf("unknown keyword mode %q (want word, partial or regex)", kw.Mode)
		}
		key := kw.Mode + "\x00" + text
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, JailbreakKeyword{Text: text, Mode: kw.Mode})
	}
	return out, nil
}

// jailbreakKeywordRule is one keyword with its pattern already compiled, so no
// request ever compiles a regexp.
type jailbreakKeywordRule struct {
	presetID string
	text     string
	mode     string
	re       *regexp.Regexp // non-nil only for mode regex
}

func newJailbreakKeywordRule(presetID string, kw JailbreakKeyword) (jailbreakKeywordRule, error) {
	rule := jailbreakKeywordRule{presetID: presetID, text: kw.Text, mode: kw.Mode}
	if kw.Mode == JailbreakKeywordRegex {
		re, err := regexp.Compile("(?i)" + kw.Text)
		if err != nil {
			return rule, fmt.Errorf("compile regex keyword %q: %w", kw.Text, err)
		}
		rule.re = re
	}
	return rule, nil
}

// matches reports whether id satisfies this rule. Every mode is
// case-insensitive.
func (rule jailbreakKeywordRule) matches(id string) bool {
	if id == "" || rule.text == "" {
		return false
	}
	switch rule.mode {
	case JailbreakKeywordPartial:
		return strings.Contains(strings.ToLower(id), rule.text)
	case JailbreakKeywordRegex:
		return rule.re != nil && rule.re.MatchString(id)
	default:
		return jailbreakWordMatch(id, rule.text)
	}
}

// jailbreakWordMatch reports whether kw occurs in id with a delimiter or a
// string edge on both sides: "deepseek" matches "providerx/deepseek-4-pro",
// while "seek" and "deepseekia" do not.
func jailbreakWordMatch(id, kw string) bool {
	lower := strings.ToLower(id)
	kw = strings.ToLower(kw)
	from := 0
	for from <= len(lower)-len(kw) {
		i := strings.Index(lower[from:], kw)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(kw)
		if jailbreakAtBoundary(lower, start, end) {
			return true
		}
		from = start + 1
	}
	return false
}

func jailbreakAtBoundary(s string, start, end int) bool {
	if start > 0 && !isJailbreakDelimiter(s[start-1]) {
		return false
	}
	if end < len(s) && !isJailbreakDelimiter(s[end]) {
		return false
	}
	return true
}

func isJailbreakDelimiter(c byte) bool {
	return strings.IndexByte(jailbreakDelimiters, c) >= 0
}

// jailbreakRuleset is the whole jailbreak configuration in one immutable
// snapshot: exact bindings, presets by id, the flattened include rules, and
// the exclude rules — global and per preset.
type jailbreakRuleset struct {
	exact   map[string]JailbreakModelConfig
	presets map[string]*JailbreakDoc
	rules   []jailbreakKeywordRule // include rules, ordered by preset id
	// excludes apply to every preset. A match here means no injection at all,
	// whatever an exact binding or a keyword says.
	excludes []jailbreakKeywordRule
	// presetExcludes are the per-preset deny lists, keyed by preset id.
	presetExcludes map[string][]jailbreakKeywordRule
}

// ResolveJailbreakDoc resolves the injection document for one request. ids are
// the candidate ids in specificity order: the id the client sent first, then
// each forwarded leg id ("provider/model", then the bare model). See the
// precedence note at the top of this file.
func (r *Repo) ResolveJailbreakDoc(ids ...string) (*JailbreakDoc, error) {
	rs := r.jailbreakRuleset()

	// The global exclude list is absolute: it outranks every preset and every
	// exact binding, so it is the operator's "never these models" switch.
	if rs.excluded("", ids) {
		return nil, nil
	}

	for _, id := range ids {
		if id == "" {
			continue
		}
		cfg, ok := rs.exact[id]
		if !ok || !cfg.Enabled {
			continue
		}
		presetID := cfg.PresetID
		if presetID == "" {
			presetID = jailbreakDocKey
		}
		doc, ok := rs.presets[presetID]
		if !ok {
			continue // orphan binding: the preset is gone, keep looking
		}
		if rs.excluded(presetID, ids) {
			return nil, nil // this preset denies the request
		}
		return doc, nil // authoritative, even when the preset prompt is empty
	}

	best := -1
	for _, id := range ids {
		if id == "" {
			continue
		}
		for i := range rs.rules {
			if !rs.rules[i].matches(id) {
				continue
			}
			// Strictly greater keeps the first hit on a length tie, and the
			// slice is ordered by preset id, so the tie-break is the lowest
			// preset id.
			if best < 0 || len(rs.rules[i].text) > len(rs.rules[best].text) {
				best = i
			}
		}
		if best >= 0 {
			break // an earlier id outranks a later one
		}
	}
	if best < 0 {
		return nil, nil
	}
	presetID := rs.rules[best].presetID
	if rs.excluded(presetID, ids) {
		return nil, nil // the winning preset denies the request
	}
	doc := rs.presets[presetID]
	if doc == nil || doc.Prompt == "" {
		return nil, nil
	}
	return doc, nil
}

// excluded reports whether any exclude rule denies this request: the global
// list always, plus presetID's own list when presetID is non-empty. Every
// candidate id is checked, so excluding a combo name denies the whole combo
// and excluding one leg denies that leg.
func (rs *jailbreakRuleset) excluded(presetID string, ids []string) bool {
	for _, id := range ids {
		if id == "" {
			continue
		}
		for i := range rs.excludes {
			if rs.excludes[i].matches(id) {
				return true
			}
		}
		if presetID == "" {
			continue
		}
		for _, rule := range rs.presetExcludes[presetID] {
			if rule.matches(id) {
				return true
			}
		}
	}
	return false
}

// jailbreakRuleset returns the cached snapshot, building it on first use after
// startup or a write.
func (r *Repo) jailbreakRuleset() *jailbreakRuleset {
	if rs, ok := r.jbRules.Load().(*jailbreakRuleset); ok && rs != nil {
		return rs
	}
	r.jbBuild.Lock()
	defer r.jbBuild.Unlock()
	if rs, ok := r.jbRules.Load().(*jailbreakRuleset); ok && rs != nil {
		return rs
	}
	rs, err := r.buildJailbreakRuleset()
	if err != nil {
		// One failed build must not become a query per request, and it must
		// not inject anything either: an empty snapshot means pass-through.
		log.Warn("jailbreak", "ruleset build failed", "error", err)
		rs = &jailbreakRuleset{
			exact:          map[string]JailbreakModelConfig{},
			presets:        map[string]*JailbreakDoc{},
			presetExcludes: map[string][]jailbreakKeywordRule{},
		}
	}
	r.jbRules.Store(rs)
	return rs
}

// invalidateJailbreakRuleset drops the snapshot so the next request rebuilds
// it. Every jailbreak write calls this.
func (r *Repo) invalidateJailbreakRuleset() {
	r.jbRules.Store((*jailbreakRuleset)(nil))
}

func (r *Repo) buildJailbreakRuleset() (*jailbreakRuleset, error) {
	rs := &jailbreakRuleset{
		exact:          map[string]JailbreakModelConfig{},
		presets:        map[string]*JailbreakDoc{},
		presetExcludes: map[string][]jailbreakKeywordRule{},
	}

	pRows, err := r.db.QueryContext(context.Background(),
		`SELECT key, value FROM kv WHERE scope = ? AND (key LIKE ? OR key = ?)`,
		jailbreakScope, jailbreakDocPrefix+"%", jailbreakDocKey,
	)
	if err != nil {
		return nil, fmt.Errorf("list jailbreak presets: %w", err)
	}
	defer func() { _ = pRows.Close() }()
	for pRows.Next() {
		var k, raw string
		if err := pRows.Scan(&k, &raw); err != nil {
			return nil, fmt.Errorf("scan jailbreak preset: %w", err)
		}
		doc := parseJailbreakPresetRow(k, raw)
		if doc == nil {
			continue
		}
		rs.presets[doc.ID] = doc
	}
	if err := pRows.Err(); err != nil {
		return nil, fmt.Errorf("list jailbreak presets: %w", err)
	}

	mRows, err := r.db.QueryContext(context.Background(),
		`SELECT key, value FROM kv WHERE scope = ? AND key LIKE ?`,
		jailbreakScope, jailbreakModelPrefix+"%",
	)
	if err != nil {
		return nil, fmt.Errorf("list jailbreak bindings: %w", err)
	}
	defer func() { _ = mRows.Close() }()
	for mRows.Next() {
		var k, raw string
		if err := mRows.Scan(&k, &raw); err != nil {
			continue // a malformed row means off
		}
		var cfg JailbreakModelConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil || !cfg.Enabled {
			continue
		}
		if cfg.PresetID == "" {
			cfg.PresetID = jailbreakDocKey
		}
		rs.exact[strings.TrimPrefix(k, jailbreakModelPrefix)] = cfg
	}
	if err := mRows.Err(); err != nil {
		return nil, fmt.Errorf("list jailbreak bindings: %w", err)
	}

	// Flatten the global exclude list first, then the per-preset rules in
	// preset-id order so the length tie-break is stable.
	globalExcludes, err := r.GetJailbreakExcludes()
	if err != nil {
		return nil, err
	}
	for _, kw := range globalExcludes {
		rule, err := newJailbreakKeywordRule("", kw)
		if err != nil {
			log.Warn("jailbreak", "skip global exclude", "error", err)
			continue
		}
		rs.excludes = append(rs.excludes, rule)
	}

	presetIDs := make([]string, 0, len(rs.presets))
	for id := range rs.presets {
		presetIDs = append(presetIDs, id)
	}
	sort.Strings(presetIDs)
	for _, id := range presetIDs {
		doc := rs.presets[id]
		if doc == nil {
			continue
		}
		for _, kw := range doc.Excludes {
			rule, err := newJailbreakKeywordRule(id, kw)
			if err != nil {
				log.Warn("jailbreak", "skip exclude", "preset", id, "error", err)
				continue
			}
			rs.presetExcludes[id] = append(rs.presetExcludes[id], rule)
		}
		if doc.Prompt == "" {
			continue // a preset with nothing to inject cannot win a match
		}
		for _, kw := range doc.Keywords {
			rule, err := newJailbreakKeywordRule(id, kw)
			if err != nil {
				log.Warn("jailbreak", "skip keyword", "preset", id, "error", err)
				continue
			}
			rs.rules = append(rs.rules, rule)
		}
	}
	return rs, nil
}

// GetJailbreakExcludes returns the global exclude list: model ids matching one
// of these rules are never injected, whatever any preset or exact binding
// says. An empty list means nothing is excluded.
func (r *Repo) GetJailbreakExcludes() ([]JailbreakKeyword, error) {
	var raw string
	err := r.db.QueryRowContext(
		context.Background(),
		`SELECT value FROM kv WHERE scope = ? AND key = ? LIMIT 1`,
		jailbreakScope, jailbreakExcludeKey,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rules []JailbreakKeyword
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("parse jailbreak excludes: %w", err)
	}
	return NormalizeJailbreakKeywords(rules)
}

// SaveJailbreakExcludes replaces the global exclude list.
func (r *Repo) SaveJailbreakExcludes(rules []JailbreakKeyword) error {
	normalized, err := NormalizeJailbreakKeywords(rules)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	if _, err := r.db.ExecContext(context.Background(),
		`INSERT INTO kv (scope, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(scope, key) DO UPDATE SET value = excluded.value`,
		jailbreakScope, jailbreakExcludeKey, string(raw),
	); err != nil {
		return err
	}
	r.invalidateJailbreakRuleset()
	return nil
}

// JailbreakMatch explains which rule decided one model id, so the dashboard
// can answer "why did (or didn't) this id get injected?" without an operator
// having to reason about the precedence chain by hand.
type JailbreakMatch struct {
	// Injected is the final verdict: true when a prompt is prepended.
	Injected bool `json:"injected"`
	// Reason names the winning rule: "global-exclude", "exact-binding",
	// "keyword", "preset-exclude", or "no-match".
	Reason string `json:"reason"`
	// PresetID and PresetName identify the winning preset, when there is one.
	PresetID   string `json:"presetId,omitempty"`
	PresetName string `json:"presetName,omitempty"`
	// Keyword is the winning rule text, for a keyword match.
	Keyword string `json:"keyword,omitempty"`
	// MatchedID is the candidate id the winning rule matched.
	MatchedID string `json:"matchedId,omitempty"`
	// Detail is a human sentence for the dashboard.
	Detail string `json:"detail,omitempty"`
}

// ExplainJailbreakDoc resolves ids the same way ResolveJailbreakDoc does, and
// reports which rule won. It exists for the dashboard's test-match field: the
// resolution itself is unchanged, only the reason is surfaced.
func (r *Repo) ExplainJailbreakDoc(ids ...string) (*JailbreakMatch, error) {
	rs := r.jailbreakRuleset()
	if rs.excluded("", ids) {
		return &JailbreakMatch{
			Injected: false,
			Reason:   "global-exclude",
			Detail:   "A global exclude rule matches this id, so no preset is ever injected.",
		}, nil
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		cfg, ok := rs.exact[id]
		if !ok || !cfg.Enabled {
			continue
		}
		presetID := cfg.PresetID
		if presetID == "" {
			presetID = jailbreakDocKey
		}
		doc, ok := rs.presets[presetID]
		if !ok {
			continue // orphan binding: the preset is gone, keep looking
		}
		if rs.excluded(presetID, ids) {
			return &JailbreakMatch{
				Injected:  false,
				Reason:    "preset-exclude",
				PresetID:  presetID,
				MatchedID: id,
				Detail:    "An exact binding matched, but that preset's own exclude list denies this id.",
			}, nil
		}
		return &JailbreakMatch{
			Injected:   true,
			Reason:     "exact-binding",
			PresetID:   presetID,
			PresetName: doc.Name,
			MatchedID:  id,
			Detail:     "An exact binding on this id wins over every keyword rule.",
		}, nil
	}
	best := -1
	bestID := ""
	for _, id := range ids {
		if id == "" {
			continue
		}
		for i := range rs.rules {
			if !rs.rules[i].matches(id) {
				continue
			}
			if best < 0 || len(rs.rules[i].text) > len(rs.rules[best].text) {
				best = i
				bestID = id
			}
		}
		if best >= 0 {
			break // an earlier id outranks a later one
		}
	}
	if best < 0 {
		return &JailbreakMatch{
			Injected: false,
			Reason:   "no-match",
			Detail:   "No preset keyword or exact binding covers this id.",
		}, nil
	}
	presetID := rs.rules[best].presetID
	if rs.excluded(presetID, ids) {
		return &JailbreakMatch{
			Injected:  false,
			Reason:    "preset-exclude",
			PresetID:  presetID,
			Keyword:   rs.rules[best].text,
			MatchedID: bestID,
			Detail:    "A keyword matched, but that preset's own exclude list denies this id.",
		}, nil
	}
	doc := rs.presets[presetID]
	name := ""
	if doc != nil {
		name = doc.Name
	}
	return &JailbreakMatch{
		Injected:   true,
		Reason:     "keyword",
		PresetID:   presetID,
		PresetName: name,
		Keyword:    rs.rules[best].text,
		MatchedID:  bestID,
		Detail:     "A keyword rule matched this id.",
	}, nil
}
