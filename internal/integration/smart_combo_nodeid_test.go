//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The dashboard keys a provider node's custom-model rows with the node id —
// ProviderDetailView stores them under storageAlias, which is selectedNode.id
// — while every reader addresses the model by the node's display prefix
// (/v1/models publishes "prefix/id"; the resolver maps prefix → node). The
// candidate builder used to skip any row whose alias was neither a prefix nor
// a registry id, so a rule aimed at those models reported "Adds 0 of 0
// matches" while the unit tests stayed green: they seeded the custom row under
// the prefix, a shape the dashboard never writes.
//
// This drives the exact production shape through the real router.
func TestSmartRulesMatchCustomModelsKeyedByNodeID(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())

	if _, err := env.Repo.CreateProviderNode("node-cutad", "openai-compatible", "CutadAI",
		`{"prefix":"cutad","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create node: %v", err)
	}
	priority := 1
	data := `{"apiKey":"sk-cutad","baseUrl":"` + upstream.URL + `/chat/completions","providerSpecificData":{"prefix":"cutad"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-cutad", "node-cutad", "apikey", "Cutad", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	// Production shape: key AND value carry the node id, not the prefix.
	if err := env.Repo.SetKV("customModels", "node-cutad|deepseek-v4-flash|llm",
		`{"providerAlias":"node-cutad","id":"deepseek-v4-flash","type":"llm"}`); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
	env.AddCombo(t, "combo-ds", "ds-all", []string{})

	cases := []struct {
		pattern string
		mode    string
		want    string
	}{
		{"deepseek*", "glob", "cutad/deepseek-v4-flash"},
		{"deepseek", "keyword", "cutad/deepseek-v4-flash"},
		{"cutad/deepseek-v4-flash", "glob", "cutad/deepseek-v4-flash"},
		{"re:^cutad/deepseek-v4-flash$", "regex", "cutad/deepseek-v4-flash"},
	}
	for _, tc := range cases {
		res := env.Post(t, "/api/combos/combo-ds/smart/preview", map[string]any{
			"matchRules": []any{map[string]any{"pattern": tc.pattern, "mode": tc.mode}},
		})
		if res.Status != http.StatusOK {
			t.Fatalf("%s %s: POST preview = %d, want 200 (body: %s)",
				tc.mode, tc.pattern, res.Status, truncate(res.Body))
		}
		var out struct {
			Candidates int `json:"candidates"`
			Added      []string
			Rules      []struct {
				Matched int `json:"matched"`
				Applied int `json:"applied"`
			} `json:"rules"`
		}
		if err := json.Unmarshal(res.Body, &out); err != nil {
			t.Fatalf("%s %s: decode: %v", tc.mode, tc.pattern, err)
		}
		if out.Candidates != 1 || len(out.Added) != 1 || out.Added[0] != tc.want ||
			len(out.Rules) != 1 || out.Rules[0].Matched != 1 || out.Rules[0].Applied != 1 {
			t.Fatalf("%s %q: got candidates=%d added=%v rules=%+v, want the one model %q",
				tc.mode, tc.pattern, out.Candidates, out.Added, out.Rules, tc.want)
		}
	}
	fmt.Printf("node-id-keyed candidates matched under all four modes\n")

	// The stored membership keeps the prefix spelling: it is the one the
	// resolver and /v1/models agree on. A dry run must not have written it.
	stored, err := env.Repo.GetComboRules(t.Context(), "combo-ds")
	if err != nil {
		t.Fatalf("GetComboRules: %v", err)
	}
	if stored != "" && stored != "[]" {
		t.Errorf("the preview wrote rules: %q", stored)
	}

	// Saving the rules materializes the member under the prefix spelling.
	res := env.Post(t, "/api/combos", map[string]any{
		"id":         "combo-save",
		"name":       "ds-save",
		"models":     []string{},
		"matchRules": []any{map[string]any{"pattern": "deepseek*", "mode": "glob"}},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("create combo with rules = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	combo, err := env.Repo.GetComboById("combo-save")
	if err != nil {
		t.Fatalf("GetComboById: %v", err)
	}
	if combo == nil {
		t.Fatalf("combo-save was not stored")
	}
	var members []string
	if err := json.Unmarshal([]byte(combo.Models), &members); err != nil {
		t.Fatalf("decode combo models %q: %v", combo.Models, err)
	}
	if len(members) != 1 || members[0] != "cutad/deepseek-v4-flash" {
		t.Fatalf("saved members = %v, want [cutad/deepseek-v4-flash]", members)
	}
}

// A member written under the node-id spelling survives a reconcile: the
// candidate's AltID keeps the second spelling live so a rule snapshot stored
// before the fix is not pruned.
func TestSmartRulesKeepNodeIDSpelledMember(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())

	if _, err := env.Repo.CreateProviderNode("node-cutad", "openai-compatible", "CutadAI",
		`{"prefix":"cutad","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create node: %v", err)
	}
	priority := 1
	data := `{"apiKey":"sk-cutad","baseUrl":"` + upstream.URL + `/chat/completions","providerSpecificData":{"prefix":"cutad"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-cutad", "node-cutad", "apikey", "Cutad", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := env.Repo.SetKV("customModels", "node-cutad|deepseek-v4-flash|llm",
		`{"providerAlias":"node-cutad","id":"deepseek-v4-flash","type":"llm"}`); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
	env.AddCombo(t, "combo-ds", "ds-all", []string{"node-cutad/deepseek-v4-flash"})
	if err := env.Repo.SetComboRules(t.Context(), "combo-ds",
		`[{"id":"r1","pattern":"deepseek*","mode":"glob","effect":"include","enabled":true,"maxMembers":20,"allKinds":false,"resolved":["node-cutad/deepseek-v4-flash"]}]`); err != nil {
		t.Fatalf("store rules: %v", err)
	}

	res := env.Post(t, "/api/combos/reconcile", nil)
	if res.Status != http.StatusOK {
		t.Fatalf("POST reconcile = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var applied struct {
		Reconciled int      `json:"reconciled"`
		Removed    []string `json:"removed"`
	}
	res.Decode(t, &applied)
	if applied.Reconciled != 0 {
		t.Errorf("reconciled = %d, want 0 (the node-id member must survive)", applied.Reconciled)
	}
	if len(applied.Removed) != 0 {
		t.Errorf("removed = %v, want nothing pruned", applied.Removed)
	}
}

// A rule pattern written with the candidate's second spelling — the canonical
// provider id beside the alias, or a node id beside its display prefix —
// selects the same model. The matcher used to consult only ID and Bare, so
// "deepseek/*" reported "Adds 0 of 0 matches" against a candidate spelled
// "ds/deepseek-chat" (AltID "deepseek/deepseek-chat"), and "node-cutad/deepseek*"
// reported the same against "cutad/deepseek-v4-flash" (AltID
// "node-cutad/deepseek-v4-flash"). Both spellings are ones an operator has a
// reason to write: the provider id is what the connection is registered under,
// and the node id is what the dashboard stores the custom rows under.
func TestSmartRulesMatchAlternateSpellings(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	priority := 1
	dsData := `{"apiKey":"sk-ds","baseUrl":"` + upstream.URL + `/chat/completions"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-ds", "deepseek", "apikey", "DeepSeek", &priority, dsData); err != nil {
		t.Fatalf("create deepseek connection: %v", err)
	}
	if _, err := env.Repo.CreateProviderNode("node-cutad", "openai-compatible", "CutadAI",
		`{"prefix":"cutad","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create node: %v", err)
	}
	nodeData := `{"apiKey":"sk-cutad","baseUrl":"` + upstream.URL + `/chat/completions","providerSpecificData":{"prefix":"cutad"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-cutad", "node-cutad", "apikey", "Cutad", &priority, nodeData); err != nil {
		t.Fatalf("create node connection: %v", err)
	}
	if err := env.Repo.SetKV("customModels", "node-cutad|deepseek-v4-flash|llm",
		`{"providerAlias":"node-cutad","id":"deepseek-v4-flash","type":"llm"}`); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
	env.AddCombo(t, "combo-alt", "alt-spellings", []string{})
	cases := []struct {
		name    string
		pattern string
		mode    string
		want    string
	}{
		{"provider id glob", "deepseek/*", "glob", "ds/deepseek-chat"},
		{"provider id regex", `re:^deepseek/.*$`, "regex", "ds/deepseek-chat"},
		{"provider id keyword", "deepseek/", "keyword", "ds/deepseek-chat"},
		{"node id glob", "node-cutad/deepseek*", "glob", "cutad/deepseek-v4-flash"},
		{"node id regex", `re:^node-cutad/deepseek-v4-flash$`, "regex", "cutad/deepseek-v4-flash"},
		{"node id keyword", "node-cutad/", "keyword", "cutad/deepseek-v4-flash"},
	}
	for _, tc := range cases {
		res := env.Post(t, "/api/combos/combo-alt/smart/preview", map[string]any{
			"matchRules": []any{map[string]any{"pattern": tc.pattern, "mode": tc.mode}},
		})
		if res.Status != http.StatusOK {
			t.Fatalf("%s: POST preview = %d, want 200 (body: %s)", tc.name, res.Status, truncate(res.Body))
		}
		var out struct {
			Added []string
			Rules []struct {
				Matched int `json:"matched"`
				Applied int `json:"applied"`
			} `json:"rules"`
		}
		if err := json.Unmarshal(res.Body, &out); err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if len(out.Rules) != 1 || out.Rules[0].Matched < 1 || out.Rules[0].Applied < 1 {
			t.Fatalf("%s %q: matched/applied = %+v, want at least one under the alternate spelling",
				tc.name, tc.pattern, out.Rules)
		}
		if !slices.Contains(out.Added, tc.want) {
			t.Fatalf("%s %q: added = %v, want it to contain %q", tc.name, tc.pattern, out.Added, tc.want)
		}
	}
	// An exclude rule spelled the same way keeps the model out: the deny map is
	// keyed by the candidate's own id, so the alternate spelling still denies.
	res := env.Post(t, "/api/combos/combo-alt/smart/preview", map[string]any{
		"matchRules": []any{
			map[string]any{"pattern": "*", "mode": "glob"},
			map[string]any{"pattern": "deepseek/*", "mode": "glob", "effect": "exclude"},
		},
	})
	if res.Status != http.StatusOK {
		t.Fatalf("exclude preview = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var out struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("exclude decode: %v", err)
	}
	for _, m := range out.Models {
		if strings.HasPrefix(m, "ds/") {
			t.Fatalf("models = %v, want the provider-id-spelled exclude to have removed the ds/ members", out.Models)
		}
	}
	fmt.Printf("alternate-spelling rules matched under glob, keyword and regex\n")
}

// Gap 1: a member the operator pulled out under one spelling stays out under
// every other spelling. The dashboard shows the node-id spelling while the
// rule claims the prefix one, so comparing Excluded against the candidate's
// own id alone offered the model straight back.
func TestSmartRulesExcludedHoldsUnderEverySpelling(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	if _, err := env.Repo.CreateProviderNode("node-cutad", "openai-compatible", "CutadAI",
		`{"prefix":"cutad","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create node: %v", err)
	}
	priority := 1
	data := `{"apiKey":"sk-cutad","baseUrl":"` + upstream.URL + `/chat/completions","providerSpecificData":{"prefix":"cutad"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-cutad", "node-cutad", "apikey", "Cutad", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	if err := env.Repo.SetKV("customModels", "node-cutad|deepseek-v4-flash|llm",
		`{"providerAlias":"node-cutad","id":"deepseek-v4-flash","type":"llm"}`); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
	env.AddCombo(t, "combo-excl", "excl", []string{})
	// The operator removed the member while the dashboard spelled it with the
	// node id; the rule claims the prefix spelling.
	rules := `[{"id":"r1","pattern":"cutad/*","mode":"glob","effect":"include","enabled":true,"maxMembers":20,"resolved":["cutad/deepseek-v4-flash"],"excluded":["node-cutad/deepseek-v4-flash"]}]`
	res := env.Post(t, "/api/combos/combo-excl/smart/preview", map[string]any{"matchRules": []any{
		map[string]any{"pattern": "cutad/*", "mode": "glob", "resolved": []string{"cutad/deepseek-v4-flash"}, "excluded": []string{"node-cutad/deepseek-v4-flash"}},
	}})
	if res.Status != http.StatusOK {
		t.Fatalf("preview = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var out struct {
		Models []string `json:"models"`
		Rules  []struct {
			Matched int `json:"matched"`
			Applied int `json:"applied"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Models) != 0 {
		t.Fatalf("models = %v, want the node-id-spelled removal to hold", out.Models)
	}
	if out.Rules[0].Matched != 0 || out.Rules[0].Applied != 0 {
		t.Fatalf("matched/applied = %d/%d, want 0/0", out.Rules[0].Matched, out.Rules[0].Applied)
	}
	_ = rules
}

// Gap 2: the cap keeps the members already in the combo, so a fallback chain
// the operator ordered by hand is not silently re-cut alphabetically.
func TestSmartRulesCapKeepsExistingMembers(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	if _, err := env.Repo.CreateProviderNode("node-cap", "openai-compatible", "CapAI",
		`{"prefix":"cap","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create node: %v", err)
	}
	priority := 1
	data := `{"apiKey":"sk-cap","baseUrl":"` + upstream.URL + `/chat/completions","providerSpecificData":{"prefix":"cap"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-cap", "node-cap", "apikey", "Cap", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	for _, m := range []string{"aaa", "mmm", "zzz"} {
		if err := env.Repo.SetKV("customModels", "node-cap|"+m+"|llm",
			`{"providerAlias":"node-cap","id":"`+m+`","type":"llm"}`); err != nil {
			t.Fatalf("seed %s: %v", m, err)
		}
	}
	// mmm is already a member and sorts between the other two.
	env.AddCombo(t, "combo-cap", "cap", []string{"cap/mmm"})
	res := env.Post(t, "/api/combos/combo-cap/smart/preview", map[string]any{"matchRules": []any{
		map[string]any{"pattern": "cap/*", "mode": "glob", "maxMembers": 2},
	}})
	if res.Status != http.StatusOK {
		t.Fatalf("preview = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var out struct {
		Models []string `json:"models"`
		Rules  []struct {
			Matched int  `json:"matched"`
			Applied int  `json:"applied"`
			Capped  bool `json:"capped"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Rules[0].Capped {
		t.Fatalf("rules = %+v, want Capped reported", out.Rules)
	}
	if !slices.Equal(out.Models, []string{"cap/mmm", "cap/aaa"}) {
		t.Fatalf("models = %v, want the existing member kept plus the first filler", out.Models)
	}
}

// Gap 3: a `!` in front of a `*`-separated segment negates it, which is how
// "every provider except this one" is written in a single rule.
func TestSmartRulesSegmentNegation(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	priority := 1
	dsData := `{"apiKey":"sk-ds","baseUrl":"` + upstream.URL + `/chat/completions"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-ds", "deepseek", "apikey", "DeepSeek", &priority, dsData); err != nil {
		t.Fatalf("create deepseek connection: %v", err)
	}
	if _, err := env.Repo.CreateProviderNode("node-ken", "openai-compatible", "KenariAI",
		`{"prefix":"kenari","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create node: %v", err)
	}
	kenData := `{"apiKey":"sk-ken","baseUrl":"` + upstream.URL + `/chat/completions","providerSpecificData":{"prefix":"kenari"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-ken", "node-ken", "apikey", "Kenari", &priority, kenData); err != nil {
		t.Fatalf("create kenari connection: %v", err)
	}
	if err := env.Repo.SetKV("customModels", "node-ken|kenari-chat|llm",
		`{"providerAlias":"node-ken","id":"kenari-chat","type":"llm"}`); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
	env.AddCombo(t, "combo-neg", "neg", []string{})
	// Everything except the deepseek family: the 8 registry models are denied
	// and the kenari custom model survives.
	res := env.Post(t, "/api/combos/combo-neg/smart/preview", map[string]any{"matchRules": []any{
		map[string]any{"pattern": "*!deepseek*", "mode": "glob"},
	}})
	if res.Status != http.StatusOK {
		t.Fatalf("preview = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var out struct {
		Models []string `json:"models"`
		Rules  []struct {
			Matched int `json:"matched"`
			Applied int `json:"applied"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Rules[0].Matched != 1 || out.Rules[0].Applied != 1 {
		t.Fatalf("matched/applied = %d/%d, want 1/1 (only the kenari model)", out.Rules[0].Matched, out.Rules[0].Applied)
	}
	if !slices.Equal(out.Models, []string{"kenari/kenari-chat"}) {
		t.Fatalf("models = %v, want only the non-deepseek model", out.Models)
	}
	// A pattern that is nothing but negations selects nothing.
	res2 := env.Post(t, "/api/combos/combo-neg/smart/preview", map[string]any{"matchRules": []any{
		map[string]any{"pattern": "!deepseek*", "mode": "glob"},
	}})
	if res2.Status != http.StatusBadRequest {
		t.Fatalf("a leading negation must be refused: status = %d, body = %s", res2.Status, truncate(res2.Body))
	}
}

// The load endpoint is the write half of the preview, and its merge semantics
// are the point: the models the rules claim are added, the models no rule
// claims any more are removed, and the models the operator pinned by hand are
// left exactly where they are. A replace would drop those pins; an append
// would leave stale rule-owned members behind.
func TestSmartRulesLoadMergesWithoutDroppingPins(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	if _, err := env.Repo.CreateProviderNode("node-cutad", "openai-compatible", "CutadAI",
		`{"prefix":"cutad","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create node: %v", err)
	}
	priority := 1
	data := `{"apiKey":"sk-cutad","baseUrl":"` + upstream.URL + `/chat/completions","providerSpecificData":{"prefix":"cutad"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-cutad", "node-cutad", "apikey", "Cutad", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	for _, m := range []string{"deepseek-v4-flash", "glm-5"} {
		if err := env.Repo.SetKV("customModels", "node-cutad|"+m+"|llm",
			`{"providerAlias":"node-cutad","id":"`+m+`","type":"llm"}`); err != nil {
			t.Fatalf("seed %s: %v", m, err)
		}
	}
	// A pinned model the operator chose by hand. A stale member that is no
	// longer live is NOT dropped: absence from the catalogue is weak evidence
	// of death (the same reading the reconcile path applies), so only a live
	// member the rules no longer claim is removed.
	env.AddCombo(t, "combo-load", "load", []string{"ds/pinned", "cutad/old-model"})
	res := env.Post(t, "/api/combos/combo-load/smart/load", map[string]any{"matchRules": []any{
		map[string]any{"pattern": "cutad/*", "mode": "glob"},
	}})
	if res.Status != http.StatusOK {
		t.Fatalf("POST load = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var out struct {
		Added   []string `json:"added"`
		Removed []string `json:"removed"`
		Models  []string `json:"models"`
		Changed bool     `json:"changed"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Changed {
		t.Fatal("changed = false, want the membership written")
	}
	// The pin survives, the dead member is kept (weak evidence of death), and
	// the two matches land.
	if !slices.Equal(out.Models, []string{"ds/pinned", "cutad/old-model", "cutad/deepseek-v4-flash", "cutad/glm-5"}) {
		t.Fatalf("models = %v, want the pin kept and the matches added", out.Models)
	}
	if !slices.Contains(out.Added, "cutad/deepseek-v4-flash") || !slices.Contains(out.Added, "cutad/glm-5") {
		t.Fatalf("added = %v, want both matches", out.Added)
	}
	if len(out.Removed) != 0 {
		t.Fatalf("removed = %v, want nothing removed", out.Removed)
	}
	// The rules travelled with the membership: the snapshot each rule carries
	// is what a later reconcile reads to know which members it owns.
	stored, err := env.Repo.GetComboRules(t.Context(), "combo-load")
	if err != nil {
		t.Fatalf("GetComboRules: %v", err)
	}
	if stored == "" || stored == "[]" {
		t.Fatalf("stored rules = %q, want the rule list persisted", stored)
	}
	// A second load with the same rules is a no-op, and the pin is still there.
	res2 := env.Post(t, "/api/combos/combo-load/smart/load", map[string]any{"matchRules": []any{
		map[string]any{"pattern": "cutad/*", "mode": "glob"},
	}})
	if res2.Status != http.StatusOK {
		t.Fatalf("second POST load = %d, want 200 (body: %s)", res2.Status, truncate(res2.Body))
	}
	var out2 struct {
		Models  []string `json:"models"`
		Changed bool     `json:"changed"`
	}
	if err := json.Unmarshal(res2.Body, &out2); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if out2.Changed {
		t.Error("a second load with the same rules must report no change")
	}
	if !slices.Equal(out2.Models, out.Models) {
		t.Fatalf("models = %v, want them unchanged", out2.Models)
	}
}

// A combo that asked to respect its stored order skips the rotation strategy
// and walks the list as-is. The capability reorder stays: a request that needs
// vision must still float a capable model to the front, or the chain would
// offer a text-only model and fail.
func TestComboRespectStoredOrder(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	priority := 1
	data := `{"apiKey":"sk-x","baseUrl":"` + upstream.URL + `/chat/completions"}`
	if err := env.Repo.CreateProviderConnectionFull("conn-a", "deepseek", "apikey", "A", &priority, data); err != nil {
		t.Fatalf("create conn a: %v", err)
	}
	if err := env.Repo.CreateProviderConnectionFull("conn-b", "openrouter", "apikey", "B", &priority, data); err != nil {
		t.Fatalf("create conn b: %v", err)
	}
	// Two live registry models, stored in a deliberate order.
	env.AddCombo(t, "combo-order", "order", []string{"ds/deepseek-chat", "oc/gpt-4o"})
	// Round-robin would rotate the first model away on a new turn; the flag
	// pins the stored order instead.
	if err := env.Repo.UpdateSettingsRaw(map[string]any{
		"comboStrategies": map[string]any{
			"order": map[string]any{
				"strategy":           "round-robin",
				"respectStoredOrder": true,
			},
		},
	}); err != nil {
		t.Fatalf("set strategy: %v", err)
	}
	// Two consecutive requests must both start at the stored first member:
	// round-robin would have moved to the second one on the second turn.
	for i := range 2 {
		res := env.Post(t, "/chat/completions", map[string]any{
			"model":    "order",
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		})
		if res.Status != http.StatusOK {
			t.Fatalf("chat %d = %d, want 200 (body: %s)", i+1, res.Status, truncate(res.Body))
		}
		// The upstream records the bare model id, so the assertion is on that.
		if got := upstream.Last(t).Model(t); got != "deepseek-chat" {
			t.Fatalf("request %d used %q, want the stored order's first member", i+1, got)
		}
	}
	// The flag's effect is that the rotation is skipped entirely, which the
	// stored order above already shows. The converse — that the same combo
	// without the flag goes through the rotation path — is pinned by the unit
	// test on applyComboStrategy rather than here: the rotation pointer needs
	// several turns to advance, and a request-level test of it would be
	// asserting the strategy's own cadence instead of the flag's bypass.
}

// The combo editor's sort dropdown reads one endpoint for everything it needs:
// latency, context window, kind and capabilities. The spellings are the point —
// the latency ring and the custom-model rows are keyed by the node id, while a
// combo member is spelled with the display prefix, so a lookup by the member's
// own spelling has to hit.
func TestModelMetaPublishesBothSpellings(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	if _, err := env.Repo.CreateProviderNode("node-cutad", "openai-compatible", "CutadAI",
		`{"prefix":"cutad","apiType":"openai-compatible"}`); err != nil {
		t.Fatalf("create node: %v", err)
	}
	priority := 1
	data := `{"apiKey":"sk-cutad","baseUrl":"` + upstream.URL + `/chat/completions","providerSpecificData":{"prefix":"cutad"}}`
	if err := env.Repo.CreateProviderConnectionFull("conn-cutad", "node-cutad", "apikey", "Cutad", &priority, data); err != nil {
		t.Fatalf("create connection: %v", err)
	}
	// A custom row carrying the operator's own declarations: kind, capability
	// flags and context window.
	if err := env.Repo.SetKV("customModels", "node-cutad|deepseek-v4-flash|llm",
		`{"providerAlias":"node-cutad","id":"deepseek-v4-flash","type":"llm","contextWindow":131072,"caps":{"vision":true,"reasoning":true}}`); err != nil {
		t.Fatalf("seed custom model: %v", err)
	}
	// One observation in the ring, recorded against the node id.
	if _, err := env.Repo.RawDB().Exec(
		`INSERT INTO requestDetails (id, timestamp, provider, model, connectionId, status, data)
		 VALUES ('req-meta-1', '2026-10-06T01:00:00Z', 'node-cutad', 'deepseek-v4-flash', 'conn-cutad', 'success', ?)`,
		`{"latency":{"ttft":250,"total":900}}`,
	); err != nil {
		t.Fatalf("seed request detail: %v", err)
	}
	res := env.Get(t, "/api/models/meta")
	if res.Status != http.StatusOK {
		t.Fatalf("GET meta = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	var out struct {
		Meta map[string]struct {
			Provider      string `json:"provider"`
			Model         string `json:"model"`
			TTFTMs        int64  `json:"ttftMs"`
			TotalMs       int64  `json:"totalMs"`
			Samples       int64  `json:"samples"`
			ContextWindow int    `json:"contextWindow"`
			Kind          string `json:"kind"`
			Vision        bool   `json:"vision"`
			Reasoning     bool   `json:"reasoning"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The node-id spelling carries everything the sources declared.
	byNodeID, ok := out.Meta["node-cutad|deepseek-v4-flash"]
	if !ok {
		t.Fatalf("node-id key missing from %v", out.Meta)
	}
	if byNodeID.TTFTMs != 250 || byNodeID.TotalMs != 900 || byNodeID.Samples != 1 {
		t.Errorf("latency = %+v, want ttft 250 / total 900 / 1 sample", byNodeID)
	}
	if byNodeID.ContextWindow != 131072 {
		t.Errorf("contextWindow = %d, want the operator's 131072", byNodeID.ContextWindow)
	}
	if byNodeID.Kind != "llm" {
		t.Errorf("kind = %q, want llm", byNodeID.Kind)
	}
	if !byNodeID.Vision || !byNodeID.Reasoning {
		t.Errorf("caps = %+v, want vision and reasoning from the custom row", byNodeID)
	}
	// The prefix spelling — the one a combo member uses — finds the same row.
	byPrefix, ok := out.Meta["cutad|deepseek-v4-flash"]
	if !ok {
		t.Fatalf("prefix key missing from %v", out.Meta)
	}
	if byPrefix.TTFTMs != byNodeID.TTFTMs || byPrefix.ContextWindow != byNodeID.ContextWindow ||
		byPrefix.Vision != byNodeID.Vision {
		t.Errorf("prefix entry = %+v, want the same numbers as the node-id entry", byPrefix)
	}
}
