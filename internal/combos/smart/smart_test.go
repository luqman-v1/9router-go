package smart

import (
	"slices"
	"strings"
	"testing"
)

func cand(id, kind string, addable, live bool) Candidate {
	alias, bare, _ := strings.Cut(id, "/")
	if bare == "" {
		bare = id
	}
	return Candidate{ID: id, Bare: bare, AltID: alias, Kind: kind, Addable: addable, Live: live}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name     string
		rule     Rule
		wantCode string
	}{
		{"glob", Rule{Pattern: "kenari/deepseek*", Mode: ModeGlob, Effect: EffectInclude, MaxMembers: 20}, ""},
		{"keyword", Rule{Pattern: "deepseek", Mode: ModeKeyword, Effect: EffectInclude, MaxMembers: 20}, ""},
		{"regex", Rule{Pattern: `re:^kenari/deepseek-v\d+$`, Mode: ModeRegex, Effect: EffectInclude, MaxMembers: 20}, ""},
		{"exclude", Rule{Pattern: "*preview*", Mode: ModeGlob, Effect: EffectExclude, MaxMembers: 20}, ""},
		{"empty pattern", Rule{Pattern: "  ", Mode: ModeGlob, Effect: EffectInclude}, "EMPTY_PATTERN"},
		{"bang sigil", Rule{Pattern: "!deepseek", Mode: ModeGlob, Effect: EffectInclude}, "INVALID_RULE_PATTERN"},
		{"dash sigil", Rule{Pattern: "-deepseek", Mode: ModeGlob, Effect: EffectInclude}, "INVALID_RULE_PATTERN"},
		{"regex without prefix", Rule{Pattern: `^kenari/`, Mode: ModeRegex, Effect: EffectInclude}, "INVALID_REGEX"},
		{"regex does not compile", Rule{Pattern: `re:^kenari/(unclosed`, Mode: ModeRegex, Effect: EffectInclude}, "INVALID_REGEX"},
		{"unknown mode", Rule{Pattern: "x", Mode: "sql", Effect: EffectInclude}, "INVALID_RULE_MODE"},
		{"unknown effect", Rule{Pattern: "x", Mode: ModeGlob, Effect: "maybe"}, "INVALID_RULE_EFFECT"},
		{"negative limit", Rule{Pattern: "x", Mode: ModeGlob, Effect: EffectInclude, MaxMembers: -1}, "INVALID_RULE_LIMIT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code, _ := Validate(tt.rule); code != tt.wantCode {
				t.Errorf("Validate() code = %q, want %q", code, tt.wantCode)
			}
		})
	}
}

func TestParseRulesDefaults(t *testing.T) {
	for _, raw := range []string{"", "   ", "[]", "null"} {
		rules, err := ParseRules(raw)
		if err != nil {
			t.Fatalf("ParseRules(%q): %v", raw, err)
		}
		if len(rules) != 0 {
			t.Errorf("ParseRules(%q) = %v, want no rules", raw, rules)
		}
	}

	rules, err := ParseRules(`[{"pattern":"deepseek"}]`)
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(rules))
	}
	r := rules[0]
	if !r.Enabled {
		t.Error("an omitted enabled must read as enabled")
	}
	if r.MaxMembers != DefaultMaxMembers {
		t.Errorf("MaxMembers = %d, want the default %d", r.MaxMembers, DefaultMaxMembers)
	}
	if r.Mode != ModeGlob {
		t.Errorf("Mode = %q, want glob", r.Mode)
	}
	if r.Effect != EffectInclude {
		t.Errorf("Effect = %q, want include", r.Effect)
	}
	if r.ID != "r1" {
		t.Errorf("ID = %q, want r1", r.ID)
	}
}

func TestEncodeRulesRoundTrip(t *testing.T) {
	if got, err := EncodeRules(nil); err != nil || got != "[]" {
		t.Fatalf("EncodeRules(nil) = %q, %v; want \"[]\"", got, err)
	}
	in := []Rule{{ID: "r1", Pattern: "a*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 5, Resolved: []string{"x/y"}}}
	encoded, err := EncodeRules(in)
	if err != nil {
		t.Fatalf("EncodeRules: %v", err)
	}
	out, err := ParseRules(encoded)
	if err != nil {
		t.Fatalf("ParseRules: %v", err)
	}
	if len(out) != 1 || out[0].Pattern != "a*" || out[0].MaxMembers != 5 || !slices.Equal(out[0].Resolved, []string{"x/y"}) {
		t.Fatalf("round trip lost data: %+v", out)
	}
}

func TestDecodeRulesFromRequestBody(t *testing.T) {
	rules, err := DecodeRules([]any{
		map[string]any{"pattern": "deepseek", "mode": "keyword"},
		map[string]any{"pattern": "re:^x/", "mode": "regex", "enabled": false, "maxMembers": 0},
	})
	if err != nil {
		t.Fatalf("DecodeRules: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rules, want 2", len(rules))
	}
	if !rules[0].Enabled || rules[0].Mode != ModeKeyword {
		t.Errorf("rule 0 = %+v", rules[0])
	}
	if rules[1].Enabled || rules[1].MaxMembers != 0 {
		t.Errorf("an explicit enabled:false and maxMembers:0 must survive: %+v", rules[1])
	}
	if rules[1].ID != "r2" {
		t.Errorf("rule 1 ID = %q, want r2", rules[1].ID)
	}
}

func TestMatchModes(t *testing.T) {
	built, err := compileAll([]Rule{
		{ID: "glob", Mode: ModeGlob, Pattern: "kenari/deepseek*"},
		{ID: "globstar", Mode: ModeGlob, Pattern: "*deepseek*"},
		{ID: "keyword", Mode: ModeKeyword, Pattern: "DEEPSEEK"},
		{ID: "regex", Mode: ModeRegex, Pattern: `re:^kenari/deepseek-v\d+$`},
	})
	if err != nil {
		t.Fatalf("compileAll: %v", err)
	}
	byID := make(map[string]compiled, len(built))
	for _, c := range built {
		byID[c.rule.ID] = c
	}

	tests := []struct {
		ruleID    string
		candidate Candidate
		want      bool
	}{
		{"glob", cand("kenari/deepseek-v4", KindLLM, true, true), true},
		{"glob", cand("kenari/deepseek-v4", KindLLM, true, true), true},
		{"glob", cand("other/deepseek-v4", KindLLM, true, true), false},
		{"globstar", cand("other/deepseek-v4", KindLLM, true, true), true},
		{"keyword", cand("kenari/deepseek-v4", KindLLM, true, true), true},
		{"keyword", cand("kenari/glm-5", KindLLM, true, true), false},
		{"regex", cand("kenari/deepseek-v4", KindLLM, true, true), true},
		{"regex", cand("kenari/deepseek-v4-preview", KindLLM, true, true), false},
	}
	for _, tt := range tests {
		if got := byID[tt.ruleID].matches(tt.candidate); got != tt.want {
			t.Errorf("%s.matches(%s) = %v, want %v", tt.ruleID, tt.candidate.ID, got, tt.want)
		}
	}
}

func TestReconcileAddsMatchingModel(t *testing.T) {
	rules := []Rule{{ID: "r1", Pattern: "kenari/deepseek*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 20}}
	candidates := []Candidate{cand("kenari/deepseek-v4", KindLLM, true, true)}

	models, got, changed, err := Reconcile(nil, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !changed {
		t.Fatal("a matching model must change the combo")
	}
	if !slices.Equal(models, []string{"kenari/deepseek-v4"}) {
		t.Fatalf("models = %v", models)
	}
	if !slices.Equal(got[0].Resolved, []string{"kenari/deepseek-v4"}) {
		t.Fatalf("snapshot = %v", got[0].Resolved)
	}

	// A second pass with the same inputs must be a no-op.
	again, _, changed, err := Reconcile(models, got, candidates)
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}
	if changed {
		t.Error("reconcile is not idempotent")
	}
	if !slices.Equal(again, models) {
		t.Errorf("second pass changed the list: %v", again)
	}
}

func TestReconcilePrunesDeadMember(t *testing.T) {
	rules := []Rule{{ID: "r1", Pattern: "kenari/deepseek*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 20, Resolved: []string{"kenari/deepseek-v4"}}}
	// The row is gone: not live, not addable.
	models, got, changed, err := Reconcile([]string{"kenari/deepseek-v4"}, rules, nil)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !changed || len(models) != 0 {
		t.Fatalf("models = %v, changed = %v; want the dead member pruned", models, changed)
	}
	if len(got[0].Resolved) != 0 {
		t.Errorf("snapshot still claims the dead model: %v", got[0].Resolved)
	}
}

func TestReconcileKeepsUnreachableMember(t *testing.T) {
	rules := []Rule{{ID: "r1", Pattern: "kenari/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 20, Resolved: []string{"kenari/deepseek-v4"}}}
	// Alive but unreachable: the provider lost its connection.
	candidates := []Candidate{cand("kenari/deepseek-v4", KindLLM, false, true)}

	models, _, changed, err := Reconcile([]string{"kenari/deepseek-v4"}, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if changed {
		t.Error("an unreachable but live member must not be pruned")
	}
	if !slices.Equal(models, []string{"kenari/deepseek-v4"}) {
		t.Errorf("models = %v", models)
	}
}

func TestReconcileKeepsAlternateSpelling(t *testing.T) {
	rules := []Rule{{ID: "r1", Pattern: "ds/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 20, Resolved: []string{"deepseek/deepseek-chat"}}}
	// The candidate spells the same model with the alias; the member was stored
	// with the canonical provider id.
	candidates := []Candidate{{
		ID: "ds/deepseek-chat", Bare: "deepseek-chat", AltID: "deepseek/deepseek-chat",
		Kind: KindLLM, Addable: true, Live: true,
	}}

	models, _, changed, err := Reconcile([]string{"deepseek/deepseek-chat"}, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if changed {
		t.Error("a member spelled with the provider id must not be pruned")
	}
	if !slices.Equal(models, []string{"deepseek/deepseek-chat"}) {
		t.Errorf("models = %v", models)
	}
}

func TestReconcileCap(t *testing.T) {
	rules := []Rule{{ID: "r1", Pattern: "ds/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 2}}
	candidates := []Candidate{
		cand("ds/a", KindLLM, true, true),
		cand("ds/b", KindLLM, true, true),
		cand("ds/c", KindLLM, true, true),
	}
	models, got, _, err := Reconcile(nil, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %v, want the cap applied", models)
	}
	if !slices.Equal(got[0].Resolved, []string{"ds/a", "ds/b"}) {
		t.Errorf("snapshot = %v, want the capped, sorted set", got[0].Resolved)
	}
}

func TestReconcileExcludeWins(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Pattern: "ds/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 20},
		{ID: "r2", Pattern: "*preview*", Mode: ModeGlob, Effect: EffectExclude, Enabled: true, MaxMembers: 20},
	}
	candidates := []Candidate{
		cand("ds/chat", KindLLM, true, true),
		cand("ds/chat-preview", KindLLM, true, true),
	}
	models, _, _, err := Reconcile(nil, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !slices.Equal(models, []string{"ds/chat"}) {
		t.Fatalf("models = %v, want the deny to win", models)
	}
}

func TestReconcileRemembersManualRemoval(t *testing.T) {
	rules := []Rule{{ID: "r1", Pattern: "ds/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 20, Resolved: []string{"ds/a", "ds/b"}}}
	candidates := []Candidate{
		cand("ds/a", KindLLM, true, true),
		cand("ds/b", KindLLM, true, true),
	}
	// The operator pulled ds/b out of the combo by hand.
	models, got, changed, err := Reconcile([]string{"ds/a"}, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !changed {
		t.Fatal("a manual removal must be recorded")
	}
	if !slices.Equal(models, []string{"ds/a"}) {
		t.Fatalf("models = %v", models)
	}
	if !slices.Contains(got[0].Excluded, "ds/b") {
		t.Fatalf("excluded = %v, want ds/b recorded", got[0].Excluded)
	}

	// The next pass must not put it back.
	again, _, changed, err := Reconcile(models, got, candidates)
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}
	if changed {
		t.Error("a remembered removal must not be re-applied")
	}
	if slices.Contains(again, "ds/b") {
		t.Errorf("models = %v, want ds/b to stay out", again)
	}
}

func TestReconcileKindGate(t *testing.T) {
	rules := []Rule{{ID: "r1", Pattern: "ds/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 20}}
	candidates := []Candidate{
		cand("ds/chat", KindLLM, true, true),
		cand("ds/text-embedding-3", "embedding", true, true),
	}
	models, _, _, err := Reconcile(nil, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !slices.Equal(models, []string{"ds/chat"}) {
		t.Fatalf("models = %v, want the embedding model gated out", models)
	}

	wide := []Rule{{ID: "r1", Pattern: "ds/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 20, AllKinds: true}}
	models, _, _, err = Reconcile(nil, wide, candidates)
	if err != nil {
		t.Fatalf("Reconcile allKinds: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %v, want allKinds to admit the embedding model", models)
	}
}

func TestReconcileDisabledRuleFreezes(t *testing.T) {
	rules := []Rule{{ID: "r1", Pattern: "ds/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: false, MaxMembers: 20, Resolved: []string{"ds/a"}}}
	candidates := []Candidate{cand("ds/b", KindLLM, true, true)}

	models, got, changed, err := Reconcile([]string{"ds/a"}, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if changed {
		t.Error("a disabled rule must neither add nor prune")
	}
	if !slices.Equal(models, []string{"ds/a"}) {
		t.Errorf("models = %v", models)
	}
	if !slices.Equal(got[0].Resolved, []string{"ds/a"}) {
		t.Errorf("snapshot = %v, want it frozen", got[0].Resolved)
	}
}

func TestAutoMembers(t *testing.T) {
	rules := []Rule{
		{ID: "r1", Pattern: "a/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, Resolved: []string{"a/1", "a/2"}},
		{ID: "r2", Pattern: "b/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: false, Resolved: []string{"b/1"}},
		{ID: "r3", Pattern: "c/*", Mode: ModeGlob, Effect: EffectExclude, Enabled: true},
	}
	got := AutoMembers(rules)
	if !slices.Equal(got, []string{"a/1", "a/2"}) {
		t.Errorf("AutoMembers = %v, want only the enabled include rule's claim", got)
	}
}

func TestPreview(t *testing.T) {
	rules := []Rule{{ID: "r1", Pattern: "ds/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 1}}
	candidates := []Candidate{
		cand("ds/a", KindLLM, true, true),
		cand("ds/b", KindLLM, true, true),
	}
	res, err := Preview([]string{"pinned/x"}, rules, candidates)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(res.Added) != 1 || res.Added[0] != "ds/a" {
		t.Errorf("added = %v", res.Added)
	}
	if len(res.Rules) != 1 || !res.Rules[0].Capped || res.Rules[0].Matched != 2 || res.Rules[0].Applied != 1 {
		t.Errorf("rule preview = %+v", res.Rules)
	}
	if !slices.Contains(res.Models, "pinned/x") {
		t.Errorf("models = %v, want the pin preserved", res.Models)
	}
}

// Gap 1: a manual removal recorded under one spelling must hold under every
// other spelling of the same model. The operator pulled the member out by hand
// while the dashboard was showing it under the node id; the rule used to offer
// it straight back under the display prefix, because Excluded was compared
// against the candidate's own id only.
func TestReconcileManualRemovalHoldsUnderEverySpelling(t *testing.T) {
	// Production shape: the address is the prefix, the node id is AltID.
	registry := Candidate{ID: "cutad/deepseek-v4-flash", Bare: "deepseek-v4-flash", AltID: "node-cutad/deepseek-v4-flash", Kind: KindLLM, Addable: true, Live: true}
	other := Candidate{ID: "cutad/glm-5", Bare: "glm-5", AltID: "node-cutad/glm-5", Kind: KindLLM, Addable: true, Live: true}
	rules := []Rule{{
		ID: "r1", Pattern: "cutad/*", Mode: ModeGlob, Effect: EffectInclude,
		Enabled: true, MaxMembers: 20,
		Resolved: []string{"cutad/deepseek-v4-flash", "cutad/glm-5"},
		Excluded: []string{"node-cutad/deepseek-v4-flash"},
	}}
	models, got, _, err := Reconcile([]string{"cutad/glm-5"}, rules, []Candidate{registry, other})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !slices.Equal(models, []string{"cutad/glm-5"}) {
		t.Fatalf("models = %v, want the node-id-spelled removal to hold", models)
	}
	if !slices.Contains(got[0].Excluded, "node-cutad/deepseek-v4-flash") {
		t.Errorf("excluded = %v, want the node-id spelling preserved", got[0].Excluded)
	}
	// A removal recorded under the addressable spelling must hold too, and the
	// sibling model on the same node must be untouched.
	rules2 := []Rule{{
		ID: "r1", Pattern: "cutad/*", Mode: ModeGlob, Effect: EffectInclude,
		Enabled: true, MaxMembers: 20,
		Resolved: []string{"cutad/deepseek-v4-flash", "cutad/glm-5"},
		Excluded: []string{"cutad/glm-5"},
	}}
	models2, _, _, err := Reconcile([]string{"cutad/deepseek-v4-flash"}, rules2, []Candidate{registry, other})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !slices.Equal(models2, []string{"cutad/deepseek-v4-flash"}) {
		t.Fatalf("models = %v, want only glm-5 excluded", models2)
	}
}

// Gap 2: the cap must keep the models already in the combo, then fill the rest
// in the order the gateway would try them. Sorting the whole set and slicing
// dropped whichever member happened to sort last, which for a fallback chain
// meant the operator's ordering was silently discarded.
func TestReconcileCapKeepsExistingMembersFirst(t *testing.T) {
	candidates := []Candidate{
		cand("ds/aaa", KindLLM, true, true),
		cand("ds/mmm", KindLLM, true, true),
		cand("ds/zzz", KindLLM, true, true),
	}
	rules := []Rule{{ID: "r1", Pattern: "ds/*", Mode: ModeGlob, Effect: EffectInclude, Enabled: true, MaxMembers: 2}}
	// mmm is already in the combo and must survive the cut even though aaa and
	// zzz sort around it.
	models, _, changed, err := Reconcile([]string{"ds/mmm"}, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !changed {
		t.Fatal("a capped pass must report a change")
	}
	if !slices.Equal(models, []string{"ds/mmm", "ds/aaa"}) {
		t.Fatalf("models = %v, want the existing member kept plus the first filler", models)
	}
	p, err := Preview([]string{"ds/mmm"}, rules, candidates)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(p.Rules) != 1 || !p.Rules[0].Capped {
		t.Fatalf("rules = %+v, want Capped reported", p.Rules)
	}
	// With nothing in the combo yet the fill is alphabetical, and a second
	// pass is a no-op.
	models2, got2, _, err := Reconcile(nil, rules, candidates)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !slices.Equal(models2, []string{"ds/aaa", "ds/mmm"}) {
		t.Fatalf("models = %v, want the first two alphabetically", models2)
	}
	again, _, changed2, err := Reconcile(models2, got2, candidates)
	if err != nil {
		t.Fatalf("Reconcile again: %v", err)
	}
	if changed2 {
		t.Error("a capped pass must be idempotent")
	}
	if !slices.Equal(again, models2) {
		t.Errorf("second pass changed the list: %v", again)
	}
}

// Gap 3: a `!` in front of a `*`-separated segment negates that segment, which
// is how "every provider except this one" is written in a single rule. Before
// this the only way was one include rule per provider.
func TestMatchGlobSegmentNegation(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		target  string
		want    bool
	}{
		{"everything but deepseek", "*!deepseek*", "ds/glm-5", true},
		{"everything but deepseek hits deepseek", "*!deepseek*", "ds/deepseek-chat", false},
		{"everything but deepseek hits bare deepseek", "*!deepseek*", "deepseek-chat", false},
		{"provider scoped except one model", "ds/*!preview*", "ds/chat", true},
		{"provider scoped except one model hits preview", "ds/*!preview*", "ds/chat-preview", false},
		{"negation only matches nothing", "!deepseek*", "ds/glm-5", false},
		{"negation only matches nothing either", "!deepseek*", "ds/deepseek-chat", false},
		{"plain glob unaffected", "ds/*", "ds/chat", true},
		{"plain glob unaffected no match", "ds/*", "oc/chat", false},
		{"bang inside a literal is not a negation", "ds/deep!seek*", "ds/deep!seek-chat", true},
		{"bang inside a literal does not match without it", "ds/deep!seek*", "ds/deepseek-chat", false},
	}
	for _, tt := range tests {
		if got := matchGlob(tt.pattern, tt.target); got != tt.want {
			t.Errorf("%s: matchGlob(%q, %q) = %v, want %v", tt.name, tt.pattern, tt.target, got, tt.want)
		}
	}
}

// A `!` that is not at the head of a segment is a typo, not a negation, and
// must be refused rather than silently read as one.
func TestValidateRejectsMisplacedNegation(t *testing.T) {
	if code, _ := Validate(Rule{Pattern: "ds/*!", Mode: ModeGlob, Effect: EffectInclude, MaxMembers: 20}); code != "INVALID_RULE_PATTERN" {
		t.Errorf("code = %q, want INVALID_RULE_PATTERN", code)
	}
	if code, _ := Validate(Rule{Pattern: "*!deepseek*", Mode: ModeGlob, Effect: EffectInclude, MaxMembers: 20}); code != "" {
		t.Errorf("a segment-head negation must be accepted, got code %q", code)
	}
	if code, _ := Validate(Rule{Pattern: "!deepseek*", Mode: ModeGlob, Effect: EffectInclude, MaxMembers: 20}); code != "INVALID_RULE_PATTERN" {
		t.Errorf("a leading negation must still be refused, got code %q", code)
	}
	// A bang buried inside a literal is part of the model id, not a negation.
	if code, _ := Validate(Rule{Pattern: "ds/deep!seek*", Mode: ModeGlob, Effect: EffectInclude, MaxMembers: 20}); code != "" {
		t.Errorf("a literal bang must be accepted, got code %q", code)
	}
}
