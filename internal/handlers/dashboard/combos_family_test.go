package dashboard

import (
	json "encoding/json/v2"
	"net/http"
	"strings"
	"testing"
)

// TestAutoFamilyCombosEndpoint exercises the real route: with an active
// gemini connection the registry's flash models must collapse into one
// editable auto-family combo, and regenerating must upsert instead of stack.
func TestAutoFamilyCombosEndpoint(t *testing.T) {
	repo, cleanup := setupTestDB(t)
	defer cleanup()
	router := setupTestRouter(repo)

	seedConnection(t, repo.RawDB(), "gemini")

	rec := postJSON(t, router, "/api/combos/auto-family", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("auto-family expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	created, _ := res["created"].([]any)
	hasFlash := false
	for _, f := range created {
		if f == "gemini-flash" {
			hasFlash = true
		}
	}
	if !hasFlash {
		t.Fatalf("expected a gemini-flash family among %v", created)
	}

	combo, err := repo.GetComboById(autoFamilyComboID("gemini-flash"))
	if err != nil {
		t.Fatalf("GetComboById: %v", err)
	}
	if combo == nil {
		t.Fatal("expected auto-family-gemini-flash combo row to exist")
	}
	if combo.Kind == nil || *combo.Kind != autoFamilyComboKind {
		t.Fatalf("kind = %v, want %q", combo.Kind, autoFamilyComboKind)
	}
	members, err := comboModels(combo.Models)
	if err != nil {
		t.Fatalf("comboModels: %v", err)
	}
	if len(members) < 2 {
		t.Fatalf("expected at least 2 members, got %v", members)
	}
	for _, m := range members {
		if !strings.HasPrefix(m, "gmni/") && !strings.Contains(m, "gemini") {
			t.Errorf("member %q does not belong to the gemini catalog", m)
		}
	}

	// Regenerating upserts: same count, no duplicate rows.
	firstCount, _ := res["count"].(float64)
	rec = postJSON(t, router, "/api/combos/auto-family", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second auto-family expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res2 map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res2["count"] != res["count"] {
		t.Errorf("regeneration changed family count: %v -> %v", firstCount, res2["count"])
	}
}

func TestModelFamily(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "claude sonnet versions collapse",
			input: "claude-sonnet-4.6",
			want:  "claude-sonnet",
		},
		{
			name:  "claude opus single-digit version",
			input: "claude-opus-4.7",
			want:  "claude-opus",
		},
		{
			name:  "gpt mini variants keep size tier",
			input: "gpt-5.4-mini",
			want:  "gpt-mini",
		},
		{
			name:  "gpt codex across generations",
			input: "gpt-5.2-codex",
			want:  "gpt-codex",
		},
		{
			name:  "gemini preview flag dropped",
			input: "gemini-3.1-flash-preview",
			want:  "gemini-flash",
		},
		{
			name:  "gemini generations meet",
			input: "gemini-3-flash-preview",
			want:  "gemini-flash",
		},
		{
			name:  "deepseek flash keeps tier",
			input: "deepseek-v4.1-flash",
			want:  "deepseek-flash",
		},
		{
			name:  "kimi v-prefixed version",
			input: "kimi-k2.6",
			want:  "kimi",
		},
		{
			name:  "free marker dropped",
			input: "deepseek-v4.1-flash:free",
			want:  "deepseek-flash",
		},
		{
			name:  "grok plain version",
			input: "grok-4.3",
			want:  "grok",
		},
		{
			name:  "registry model with owner path",
			input: "nvidia/nemotron-3-super-120b-a12b:free",
			want:  "nvidia/nemotron-super-120b-a12b",
		},
		{
			name:  "kimi mid-name generation",
			input: "kimi-k2.6",
			want:  "kimi",
		},
		{
			name:  "kimi generation suffix",
			input: "kimi-k2.7-code",
			want:  "kimi-code",
		},
		{
			name:  "minimax generations merge",
			input: "minimax-m3",
			want:  "minimax",
		},
		{
			name:  "openai o-series line kept",
			input: "o1",
			want:  "o1",
		},
		{
			name:  "openai o-series mini kept",
			input: "o3-mini",
			want:  "o3-mini",
		},
		{
			name:  "plain version only falls back to original",
			input: "4.6",
			want:  "4.6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelFamily(tt.input); got != tt.want {
				t.Errorf("modelFamily(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestModelFamily_MergesAcrossProvidersAndVersions(t *testing.T) {
	models := []string{
		"cc/claude-sonnet-4.6",
		"claude/claude-sonnet-4.5",
		"cc/claude-opus-4.7",
	}
	families := make(map[string][]string)
	for _, m := range models {
		_, id := splitAliasModel(m)
		f := modelFamily(id)
		families[f] = appendUnique(families[f], m)
	}

	if len(families) != 2 {
		t.Fatalf("expected 2 families, got %d: %v", len(families), families)
	}
	if got := len(families["claude-sonnet"]); got != 2 {
		t.Errorf("claude-sonnet family = %d members, want 2: %v", got, families["claude-sonnet"])
	}
}

func TestAutoFamilyComboID_StableAndSafe(t *testing.T) {
	if got, want := autoFamilyComboID("claude-sonnet"), "auto-family-claude-sonnet"; got != want {
		t.Errorf("autoFamilyComboID(claude-sonnet) = %q, want %q", got, want)
	}
	// Same family must always map to the same id so regeneration upserts.
	if autoFamilyComboID("gpt-mini") != autoFamilyComboID("gpt-mini") {
		t.Error("autoFamilyComboID is not stable")
	}
}

func TestIsEmbeddingModel(t *testing.T) {
	cases := map[string]bool{
		"text-embedding-3-small": true,
		"gemini-embedding-001":   true,
		"claude-sonnet-4.6":      false,
	}
	for model, want := range cases {
		if got := isEmbeddingModel(model); got != want {
			t.Errorf("isEmbeddingModel(%q) = %v, want %v", model, got, want)
		}
	}
}
