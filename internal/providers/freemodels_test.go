package providers

import "testing"

func TestProviderHasFreeModels(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{"opencode", true},
		{"oc", true}, // alias resolves to opencode
		{"glm", true},
		{"gemini", true},
		{"cloudflare-ai", true},
		{"qoder", true},
		{"openai", false},
		{"anthropic", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ProviderHasFreeModels(tt.provider); got != tt.want {
			t.Errorf("ProviderHasFreeModels(%q) = %v, want %v", tt.provider, got, tt.want)
		}
	}
}

func TestKeylessModelIDs(t *testing.T) {
	ids := KeylessModelIDs("opencode")
	if len(ids) == 0 {
		t.Fatal("KeylessModelIDs(opencode) returned none")
	}
	// The list must be sorted so a fresh install seeds deterministically.
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("KeylessModelIDs(opencode) is not sorted at %d: %s > %s", i, ids[i-1], ids[i])
		}
	}
	// The -free suffix is the only thing that can badge these, so every
	// catalogued keyless id that carries it must be present.
	found := false
	for _, id := range ids {
		if id == "big-pickle" {
			found = true
		}
	}
	if !found {
		t.Error("KeylessModelIDs(opencode) is missing big-pickle")
	}
	// A provider with no keyless regime returns nil.
	if got := KeylessModelIDs("gemini"); got != nil {
		t.Errorf("KeylessModelIDs(gemini) = %v, want nil", got)
	}
	if got := KeylessModelIDs("openai"); got != nil {
		t.Errorf("KeylessModelIDs(openai) = %v, want nil", got)
	}
}

func TestCatalogModelIDs(t *testing.T) {
	// glm-cn carries three flash ids plus the signup-bonus id; the one-time
	// regime still grants access, so all four are catalogued.
	ids := CatalogModelIDs("glm-cn")
	if len(ids) != 4 {
		t.Errorf("CatalogModelIDs(glm-cn) returned %d ids, want 4", len(ids))
	}
	if got := CatalogModelIDs("openai"); got != nil {
		t.Errorf("CatalogModelIDs(openai) = %v, want nil", got)
	}
}

func TestIsFreeModel(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    FreeModelCandidate
		want     bool
	}{
		{
			name:     "catalogued keyless model",
			provider: "opencode",
			model:    FreeModelCandidate{ID: "big-pickle"},
			want:     true,
		},
		{
			name:     "catalogued model via alias",
			provider: "oc",
			model:    FreeModelCandidate{ID: "big-pickle"},
			want:     true,
		},
		{
			name:     "catalogued free flash",
			provider: "glm",
			model:    FreeModelCandidate{ID: "glm-4.7-flash"},
			want:     true,
		},
		{
			name:     "not catalogued and no signal",
			provider: "opencode",
			model:    FreeModelCandidate{ID: "gpt-5.5"},
			want:     false,
		},
		{
			name:     "isFree true on a documented free provider",
			provider: "gemini",
			model:    FreeModelCandidate{ID: "gemini-9-ultra", IsFree: new(true)},
			want:     true,
		},
		{
			name:     "isFree true on a provider with no free tier is not free",
			provider: "openai",
			model:    FreeModelCandidate{ID: "gpt-5.5", IsFree: new(true)},
			want:     false,
		},
		{
			name:     ":free suffix on a documented free provider",
			provider: "openrouter",
			model:    FreeModelCandidate{ID: "some/model:free"},
			want:     true,
		},
		{
			name:     ":free suffix on a provider with no free tier",
			provider: "openai",
			model:    FreeModelCandidate{ID: "some/model:free"},
			want:     false,
		},
		{
			name:     "zero prices on a documented free provider",
			provider: "gemini",
			model:    FreeModelCandidate{ID: "gemini-9-ultra", PromptPrice: "0", CompletionPrice: "0.000000"},
			want:     true,
		},
		{
			name:     "zero prompt but priced completion is not free",
			provider: "gemini",
			model:    FreeModelCandidate{ID: "gemini-9-ultra", PromptPrice: "0", CompletionPrice: "0.000001"},
			want:     false,
		},
		{
			name:     "opencode -free suffix is not a payload signal",
			provider: "opencode",
			model:    FreeModelCandidate{ID: "brand-new-free"},
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsFreeModel(tt.provider, tt.model); got != tt.want {
				t.Errorf("IsFreeModel(%q, %q) = %v, want %v", tt.provider, tt.model.ID, got, tt.want)
			}
		})
	}
}

func TestIsModelFreeBadge(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    FreeModelCandidate
		want     bool
	}{
		{
			name:     "truthy free string",
			provider: "openai",
			model:    FreeModelCandidate{ID: "gpt-5.5", Free: "free"},
			want:     true,
		},
		{
			// A truthy-but-not-`true` value must NOT count: the string
			// "false" is truthy in JavaScript and was the original bug.
			name:     "the string false is not a signal",
			provider: "openai",
			model:    FreeModelCandidate{ID: "gpt-5.5", Free: "false"},
			want:     false,
		},
		{
			name:     "free word in display name",
			provider: "openai",
			model:    FreeModelCandidate{ID: "gpt-5.5", DisplayName: "GPT 5.5 (Free)"},
			want:     true,
		},
		{
			name:     "free as a substring of a word does not badge",
			provider: "openai",
			model:    FreeModelCandidate{ID: "gpt-5.5", DisplayName: "Freedom Model"},
			want:     false,
		},
		{
			name:     "catalogue entry badges",
			provider: "glm",
			model:    FreeModelCandidate{ID: "glm-4.7-flash"},
			want:     true,
		},
		{
			name:     "plain paid model does not badge",
			provider: "openai",
			model:    FreeModelCandidate{ID: "gpt-5.5", DisplayName: "GPT 5.5"},
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsModelFreeBadge(tt.provider, tt.model); got != tt.want {
				t.Errorf("IsModelFreeBadge() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTruthy(t *testing.T) {
	tests := []struct {
		value any
		want  bool
	}{
		{true, true},
		{false, false},
		{"true", true},
		{"free", true},
		{"FREE", true},
		{"false", false},
		{"", false},
		{1, false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := truthy(tt.value); got != tt.want {
			t.Errorf("truthy(%v) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestZeroPrice(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"0", true},
		{"0.0", true},
		{"0.000000", true},
		{"", false},
		{"0.000001", false},
		{"1", false},
		{"-1", false},
		{"abc", false},
	}
	for _, tt := range tests {
		if got := zeroPrice(tt.value); got != tt.want {
			t.Errorf("zeroPrice(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

func TestContainsFreeWord(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Free", true},
		{"GPT 5.5 (Free)", true},
		{"free tier", true},
		{"Modelo Grátis", true},
		{"Freedom", false},
		{"freely", false},
		{"GPT 5.5", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := containsFreeWord(tt.name); got != tt.want {
			t.Errorf("containsFreeWord(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFreeBudgetGrantsAccess(t *testing.T) {
	access := []freeRegime{
		regimeRecurringDaily, regimeRecurringMonthly, regimeRecurringCredit,
		regimeRecurringUncapped, regimeOneTimeInitial, regimeKeyless,
	}
	for _, r := range access {
		if !freeBudgetGrantsAccess(r) {
			t.Errorf("freeBudgetGrantsAccess(%q) = false, want true", r)
		}
	}
	if freeBudgetGrantsAccess(regimeDiscontinued) {
		t.Error("freeBudgetGrantsAccess(discontinued) = true, want false")
	}
	// An unknown regime must fail closed, not default to granting access.
	if freeBudgetGrantsAccess(freeRegime("something-new")) {
		t.Error("freeBudgetGrantsAccess(unknown) = true, want false")
	}
}

func TestEveryFreeCatalogEntryResolvesToAKnownProvider(t *testing.T) {
	for _, entry := range freeModelCatalog {
		if entry.Provider == "" || entry.ModelID == "" {
			t.Errorf("catalogue entry has an empty field: %+v", entry)
			continue
		}
		if _, ok := KnownProviders[entry.Provider]; !ok {
			// An alias is acceptable; anything else is a typo that would make
			// the entry silently never match.
			if GetProviderAlias(entry.Provider) == "" {
				t.Errorf("catalogue entry names unknown provider %q (model %q)", entry.Provider, entry.ModelID)
			}
		}
	}
}
