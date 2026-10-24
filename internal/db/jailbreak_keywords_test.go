package db

import "testing"

// The keyword layer is what lets one preset cover a model family across
// providers and combo legs. These tests pin the three match modes, the
// precedence order (exact binding, then keyword on the requested id, then a
// leg id, longest keyword first), and the two deny lists.

func newJailbreakTestRepo(t *testing.T) *Repo {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	return NewRepo(database)
}

func TestJailbreakWordMatch(t *testing.T) {
	tests := []struct {
		name string
		id   string
		kw   string
		want bool
	}{
		{"bare model", "deepseek", "deepseek", true},
		{"provider slash model", "providera/deepseek", "deepseek", true},
		{"suffixed variant", "providerx/deepseek-4-pro", "deepseek", true},
		{"colon tag", "tkh/mimo-v2.6-flash:free", "mimo", true},
		{"multi token run", "providerx/deepseek-4-pro", "deepseek-4", true},
		{"provider qualified keyword", "providera/deepseek", "providera/deepseek", true},
		{"suffix is not a token", "deepseekia", "deepseek", false},
		{"prefix is not a token", "notdeepseek", "deepseek", false},
		{"mid word", "deepseek", "seek", false},
		{"unrelated", "openai/gpt-4o", "deepseek", false},
		{"empty keyword", "deepseek", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jailbreakWordMatch(tt.id, tt.kw); got != tt.want {
				t.Errorf("jailbreakWordMatch(%q, %q) = %v, want %v", tt.id, tt.kw, got, tt.want)
			}
		})
	}
}

func TestJailbreakWordMatchIsCaseInsensitive(t *testing.T) {
	if !jailbreakWordMatch("providerx/DeepSeek-4-Pro", "deepseek") {
		t.Error("word match must ignore case on both sides")
	}
	if !jailbreakWordMatch("providerx/deepseek-4-pro", "DeepSeek") {
		t.Error("word match must ignore case on the keyword too")
	}
}

func TestJailbreakKeywordRuleModes(t *testing.T) {
	tests := []struct {
		name string
		id   string
		kw   JailbreakKeyword
		want bool
	}{
		{"partial matches a suffix", "deepseek", JailbreakKeyword{Text: "seek", Mode: JailbreakKeywordPartial}, true},
		{"partial is case-insensitive", "DEEPSEEK", JailbreakKeyword{Text: "seek", Mode: JailbreakKeywordPartial}, true},
		{"partial misses", "gpt-4o", JailbreakKeyword{Text: "seek", Mode: JailbreakKeywordPartial}, false},
		{"regex anchored", "deepseek-v4", JailbreakKeyword{Text: "^deepseek", Mode: JailbreakKeywordRegex}, true},
		{"regex anchored misses", "providerx/deepseek", JailbreakKeyword{Text: "^deepseek", Mode: JailbreakKeywordRegex}, false},
		{"regex alternation", "providerx/deepseek-4-pro", JailbreakKeyword{Text: "^(providera|providerx)/", Mode: JailbreakKeywordRegex}, true},
		{"regex is case-insensitive by default", "DeepSeek-V4", JailbreakKeyword{Text: "^deepseek", Mode: JailbreakKeywordRegex}, true},
		{"regex opt-out is case-sensitive", "deepseek-v4", JailbreakKeyword{Text: "(?-i)Deepseek", Mode: JailbreakKeywordRegex}, false},
		{"regex opt-out matches exact case", "Deepseek-v4", JailbreakKeyword{Text: "(?-i)Deepseek", Mode: JailbreakKeywordRegex}, true},
		{"empty rule never matches", "deepseek", JailbreakKeyword{}, false},
		{"empty id never matches", "", JailbreakKeyword{Text: "deepseek"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, err := newJailbreakKeywordRule("p", tt.kw)
			if err != nil {
				t.Fatalf("newJailbreakKeywordRule: %v", err)
			}
			if got := rule.matches(tt.id); got != tt.want {
				t.Errorf("matches(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestNewJailbreakKeywordRuleRejectsBadRegex(t *testing.T) {
	if _, err := newJailbreakKeywordRule("p", JailbreakKeyword{Text: "(unclosed", Mode: JailbreakKeywordRegex}); err == nil {
		t.Error("an uncompilable regex must be rejected, not stored")
	}
}

func TestNormalizeJailbreakKeywords(t *testing.T) {
	tests := []struct {
		name    string
		in      []JailbreakKeyword
		want    []JailbreakKeyword
		wantErr bool
	}{
		{"nil stays nil", nil, nil, false},
		{"word is lowercased", []JailbreakKeyword{{Text: "  DeepSeek "}}, []JailbreakKeyword{{Text: "deepseek"}}, false},
		{"partial is lowercased", []JailbreakKeyword{{Text: "SEEK", Mode: JailbreakKeywordPartial}}, []JailbreakKeyword{{Text: "seek", Mode: JailbreakKeywordPartial}}, false},
		{
			"regex is kept verbatim",
			[]JailbreakKeyword{{Text: "^Deep\\Seek$", Mode: JailbreakKeywordRegex}},
			[]JailbreakKeyword{{Text: "^Deep\\Seek$", Mode: JailbreakKeywordRegex}},
			false,
		},
		{
			"duplicates collapse",
			[]JailbreakKeyword{{Text: "deepseek"}, {Text: "DeepSeek"}, {Text: "seek", Mode: JailbreakKeywordPartial}, {Text: "seek", Mode: JailbreakKeywordPartial}},
			[]JailbreakKeyword{{Text: "deepseek"}, {Text: "seek", Mode: JailbreakKeywordPartial}},
			false,
		},
		{"empty text", []JailbreakKeyword{{Text: "   "}}, nil, true},
		{"unknown mode", []JailbreakKeyword{{Text: "deepseek", Mode: "fuzzy"}}, nil, true},
		{"bad regex", []JailbreakKeyword{{Text: "(unclosed", Mode: JailbreakKeywordRegex}}, nil, true},
		{"too long", []JailbreakKeyword{{Text: string(make([]byte, maxJailbreakKeywordChars+1))}}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeJailbreakKeywords(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeJailbreakKeywords() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d rules, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("rule %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestNormalizeJailbreakKeywordsRejectsTooMany(t *testing.T) {
	in := make([]JailbreakKeyword, maxJailbreakKeywords+1)
	for i := range in {
		in[i] = JailbreakKeyword{Text: "rule"}
	}
	if _, err := NormalizeJailbreakKeywords(in); err == nil {
		t.Error("more than the maximum must be rejected")
	}
}

func TestSaveJailbreakPresetRejectsBadKeyword(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "p", Prompt: "x", Keywords: []JailbreakKeyword{{Text: "(unclosed", Mode: JailbreakKeywordRegex}}})
	if err == nil {
		t.Fatal("a preset with an uncompilable regex must not save")
	}
}

// One preset, one keyword, many ids: the whole point of the feature.
func TestResolveJailbreakDocKeywordCoversProvidersAndVariants(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{
		ID: "ds", Name: "Deepseek", Prompt: "DS RULE",
		Keywords: []JailbreakKeyword{{Text: "deepseek"}},
	}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	for _, id := range []string{"providera/deepseek", "providerx/deepseek-4-pro", "deepseek-v4", "deepseek"} {
		doc, err := repo.ResolveJailbreakDoc(id)
		if err != nil {
			t.Fatalf("resolve %s: %v", id, err)
		}
		if doc == nil || doc.Prompt != "DS RULE" {
			t.Errorf("%s: got %+v, want the deepseek preset", id, doc)
		}
	}
	for _, id := range []string{"deepseekia", "notdeepseek", "openai/gpt-4o", ""} {
		doc, err := repo.ResolveJailbreakDoc(id)
		if err != nil {
			t.Fatalf("resolve %s: %v", id, err)
		}
		if doc != nil {
			t.Errorf("%s: got %+v, want no injection", id, doc)
		}
	}
}

func TestResolveJailbreakDocExactBindingBeatsKeyword(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "kw", Name: "KW", Prompt: "KEYWORD RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save keyword preset: %v", err)
	}
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "exact", Name: "EXACT", Prompt: "EXACT RULE"}); err != nil {
		t.Fatalf("save exact preset: %v", err)
	}
	if err := repo.SetJailbreakModelPreset("providerx/deepseek-4-pro", "exact", true); err != nil {
		t.Fatalf("bind model: %v", err)
	}

	doc, err := repo.ResolveJailbreakDoc("providerx/deepseek-4-pro")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc == nil || doc.Prompt != "EXACT RULE" {
		t.Errorf("got %+v, want the exact binding to win", doc)
	}

	// A sibling id with no binding still takes the keyword.
	doc, err = repo.ResolveJailbreakDoc("providera/deepseek")
	if err != nil {
		t.Fatalf("resolve sibling: %v", err)
	}
	if doc == nil || doc.Prompt != "KEYWORD RULE" {
		t.Errorf("sibling got %+v, want the keyword preset", doc)
	}
}

func TestResolveJailbreakDocLongestKeywordWins(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "aaa-short", Name: "Short", Prompt: "SHORT RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save short preset: %v", err)
	}
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "bbb-long", Name: "Long", Prompt: "LONG RULE", Keywords: []JailbreakKeyword{{Text: "deepseek-4"}}}); err != nil {
		t.Fatalf("save long preset: %v", err)
	}

	doc, err := repo.ResolveJailbreakDoc("providerx/deepseek-4-pro")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc == nil || doc.Prompt != "LONG RULE" {
		t.Errorf("got %+v, want the longer keyword to win", doc)
	}

	// The shorter keyword still owns the ids the longer one does not match.
	doc, err = repo.ResolveJailbreakDoc("providera/deepseek")
	if err != nil {
		t.Fatalf("resolve plain: %v", err)
	}
	if doc == nil || doc.Prompt != "SHORT RULE" {
		t.Errorf("plain id got %+v, want the shorter keyword preset", doc)
	}
}

func TestResolveJailbreakDocKeywordTieGoesToLowestPresetID(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "zzz", Name: "Z", Prompt: "Z RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save zzz: %v", err)
	}
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "aaa", Name: "A", Prompt: "A RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save aaa: %v", err)
	}
	doc, err := repo.ResolveJailbreakDoc("deepseek")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc == nil || doc.Prompt != "A RULE" {
		t.Errorf("got %+v, want the lowest preset id on a length tie", doc)
	}
}

func TestResolveJailbreakDocRequestedIDBeatsLegID(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	// The combo name carries its own keyword, so the requested id resolves on
	// its own without reaching the leg ids.
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "combo-preset", Name: "Combo", Prompt: "COMBO RULE", Keywords: []JailbreakKeyword{{Text: "alpha"}}}); err != nil {
		t.Fatalf("save combo preset: %v", err)
	}
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "leg-preset", Name: "Leg", Prompt: "LEG RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save leg preset: %v", err)
	}

	doc, err := repo.ResolveJailbreakDoc("fleet-alpha", "providerx/deepseek-4-pro", "deepseek-4-pro")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc == nil || doc.Prompt != "COMBO RULE" {
		t.Errorf("got %+v, want the requested id's keyword to win", doc)
	}

	doc, err = repo.ResolveJailbreakDoc("my-combo", "providerx/deepseek-4-pro", "deepseek-4-pro")
	if err != nil {
		t.Fatalf("resolve leg only: %v", err)
	}
	if doc == nil || doc.Prompt != "LEG RULE" {
		t.Errorf("leg-only got %+v, want the leg keyword preset", doc)
	}
}

func TestResolveJailbreakDocPresetExcludeDeniesKeyword(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{
		ID: "ds", Name: "DS", Prompt: "DS RULE",
		Keywords: []JailbreakKeyword{{Text: "deepseek"}},
		Excludes: []JailbreakKeyword{{Text: "deepseek-4-pro"}},
	}); err != nil {
		t.Fatalf("save preset: %v", err)
	}

	doc, err := repo.ResolveJailbreakDoc("providerx/deepseek-4-pro")
	if err != nil {
		t.Fatalf("resolve excluded: %v", err)
	}
	if doc != nil {
		t.Errorf("excluded id got %+v, want no injection", doc)
	}

	doc, err = repo.ResolveJailbreakDoc("providera/deepseek")
	if err != nil {
		t.Fatalf("resolve allowed: %v", err)
	}
	if doc == nil || doc.Prompt != "DS RULE" {
		t.Errorf("allowed id got %+v, want the preset", doc)
	}
}

func TestResolveJailbreakDocGlobalExcludeBeatsEverything(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "ds", Name: "DS", Prompt: "DS RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "exact", Name: "EXACT", Prompt: "EXACT RULE"}); err != nil {
		t.Fatalf("save exact preset: %v", err)
	}
	if err := repo.SetJailbreakModelPreset("providerx/deepseek-4-pro", "exact", true); err != nil {
		t.Fatalf("bind model: %v", err)
	}
	if err := repo.SaveJailbreakExcludes([]JailbreakKeyword{{Text: "deepseek-4-pro"}}); err != nil {
		t.Fatalf("save global excludes: %v", err)
	}

	// The exact binding loses to the global deny list.
	doc, err := repo.ResolveJailbreakDoc("providerx/deepseek-4-pro")
	if err != nil {
		t.Fatalf("resolve bound id: %v", err)
	}
	if doc != nil {
		t.Errorf("globally excluded id got %+v, want no injection", doc)
	}

	// A keyword match on another id is untouched.
	doc, err = repo.ResolveJailbreakDoc("providera/deepseek")
	if err != nil {
		t.Fatalf("resolve other id: %v", err)
	}
	if doc == nil || doc.Prompt != "DS RULE" {
		t.Errorf("other id got %+v, want the keyword preset", doc)
	}
}

func TestResolveJailbreakDocGlobalExcludeRegex(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "ds", Name: "DS", Prompt: "DS RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	if err := repo.SaveJailbreakExcludes([]JailbreakKeyword{{Text: "/internal-", Mode: JailbreakKeywordPartial}}); err != nil {
		t.Fatalf("save global excludes: %v", err)
	}
	doc, err := repo.ResolveJailbreakDoc("providerx/internal-deepseek")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc != nil {
		t.Errorf("regex exclude did not deny: %+v", doc)
	}
}

func TestResolveJailbreakDocOrphanBindingFallsThrough(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "gone", Name: "Gone", Prompt: "GONE RULE"}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	if err := repo.SetJailbreakModelPreset("providera/deepseek", "gone", true); err != nil {
		t.Fatalf("bind model: %v", err)
	}
	// Delete the preset without unbinding, the way a direct write would leave it.
	if _, err := repo.RawDB().Exec(`DELETE FROM kv WHERE scope = ? AND key = ?`, jailbreakScope, jailbreakDocPrefix+"gone"); err != nil {
		t.Fatalf("delete preset row: %v", err)
	}
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "kw", Name: "KW", Prompt: "KEYWORD RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save keyword preset: %v", err)
	}

	doc, err := repo.ResolveJailbreakDoc("providera/deepseek")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc == nil || doc.Prompt != "KEYWORD RULE" {
		t.Errorf("orphan binding got %+v, want the keyword preset", doc)
	}
}

func TestResolveJailbreakDocEmptyPresetBindingInjectsNothing(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "empty", Name: "Empty", Prompt: ""}); err != nil {
		t.Fatalf("save empty preset: %v", err)
	}
	if err := repo.SetJailbreakModelPreset("providera/deepseek", "empty", true); err != nil {
		t.Fatalf("bind model: %v", err)
	}
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "kw", Name: "KW", Prompt: "KEYWORD RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save keyword preset: %v", err)
	}

	// An exact binding to an empty preset is authoritative: it means "inject
	// nothing here", and it must not fall through to a keyword.
	doc, err := repo.ResolveJailbreakDoc("providera/deepseek")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc != nil && doc.Prompt != "" {
		t.Errorf("empty preset binding got %+v, want no injection", doc)
	}
}

// The snapshot cache must not serve stale rules after a write, or a saved
// keyword would appear to do nothing until the gateway restarts.
func TestJailbreakRulesetCacheInvalidation(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "ds", Name: "DS", Prompt: "FIRST RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save first: %v", err)
	}
	doc, err := repo.ResolveJailbreakDoc("providera/deepseek")
	if err != nil || doc == nil || doc.Prompt != "FIRST RULE" {
		t.Fatalf("first resolve: doc=%+v err=%v", doc, err)
	}

	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "ds", Name: "DS", Prompt: "SECOND RULE", Keywords: []JailbreakKeyword{{Text: "providera"}}}); err != nil {
		t.Fatalf("save second: %v", err)
	}
	doc, err = repo.ResolveJailbreakDoc("providera/deepseek")
	if err != nil || doc == nil || doc.Prompt != "SECOND RULE" {
		t.Fatalf("after preset rewrite: doc=%+v err=%v", doc, err)
	}
	// The old keyword is gone, so a sibling id no longer resolves.
	doc, err = repo.ResolveJailbreakDoc("providerx/deepseek")
	if err != nil {
		t.Fatalf("resolve sibling: %v", err)
	}
	if doc != nil {
		t.Errorf("stale keyword still matched: %+v", doc)
	}

	if err := repo.SetJailbreakModelPreset("providerx/deepseek", "ds", true); err != nil {
		t.Fatalf("bind model: %v", err)
	}
	doc, err = repo.ResolveJailbreakDoc("providerx/deepseek")
	if err != nil || doc == nil {
		t.Fatalf("after binding: doc=%+v err=%v", doc, err)
	}

	if err := repo.SaveJailbreakExcludes([]JailbreakKeyword{{Text: "providerx"}}); err != nil {
		t.Fatalf("save excludes: %v", err)
	}
	doc, err = repo.ResolveJailbreakDoc("providerx/deepseek")
	if err != nil {
		t.Fatalf("after excludes: %v", err)
	}
	if doc != nil {
		t.Errorf("global exclude not visible after save: %+v", doc)
	}

	if err := repo.SaveJailbreakExcludes(nil); err != nil {
		t.Fatalf("clear excludes: %v", err)
	}
	doc, err = repo.ResolveJailbreakDoc("providerx/deepseek")
	if err != nil || doc == nil {
		t.Fatalf("after clearing excludes: doc=%+v err=%v", doc, err)
	}
}

func TestGetJailbreakExcludesRoundTrip(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	got, err := repo.GetJailbreakExcludes()
	if err != nil {
		t.Fatalf("empty read: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty read returned %+v", got)
	}
	want := []JailbreakKeyword{{Text: "deepseek-4-pro"}, {Text: "^internal", Mode: JailbreakKeywordRegex}}
	if err := repo.SaveJailbreakExcludes(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = repo.GetJailbreakExcludes()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("rule %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// GetJailbreakDocForModel keeps its single-id contract for every existing
// caller: exact binding first, then a keyword.
func TestGetJailbreakDocForModelResolvesKeyword(t *testing.T) {
	repo := newJailbreakTestRepo(t)
	if err := repo.SaveJailbreakPreset(&JailbreakDoc{ID: "ds", Name: "DS", Prompt: "DS RULE", Keywords: []JailbreakKeyword{{Text: "deepseek"}}}); err != nil {
		t.Fatalf("save preset: %v", err)
	}
	doc, err := repo.GetJailbreakDocForModel("providera/deepseek")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if doc == nil || doc.Prompt != "DS RULE" {
		t.Errorf("got %+v, want the keyword preset", doc)
	}
	doc, err = repo.GetJailbreakDocForModel("openai/gpt-4o")
	if err != nil {
		t.Fatalf("resolve unrelated: %v", err)
	}
	if doc != nil {
		t.Errorf("unrelated id got %+v, want nil", doc)
	}
}
