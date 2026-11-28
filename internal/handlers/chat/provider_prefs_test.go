package chat

import (
	"strings"
	"testing"
)

// alias: the repo's own alias table maps "oa" → "openai"; tests lean on it
// to prove preferences match the canonical id an alias resolves to.
func TestParseProviderPreferences(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		active  bool
		order   []string
		only    []string
		ignore  []string
		noFalls bool
	}{
		{
			name:   "absent object is inert",
			body:   `{"model":"x","messages":[]}`,
			active: false,
		},
		{
			name:   "malformed object is inert",
			body:   `{"provider":{"only":123}}`,
			active: false,
		},
		{
			name:   "only restricts",
			body:   `{"provider":{"only":["openai"]}}`,
			active: true,
			only:   []string{"openai"},
		},
		{
			name:   "ignore excludes",
			body:   `{"provider":{"ignore":["deepseek","groq"]}}`,
			active: true,
			ignore: []string{"deepseek", "groq"},
		},
		{
			name:   "order lists",
			body:   `{"provider":{"order":["groq","openai"]}}`,
			active: true,
			order:  []string{"groq", "openai"},
		},
		{
			name:    "allow_fallbacks false activates",
			body:    `{"provider":{"allow_fallbacks":false}}`,
			active:  true,
			noFalls: true,
		},
		{
			name:   "nested provider field is not the object",
			body:   `{"messages":[{"role":"user","content":"tell me about the provider"}]}`,
			active: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseProviderPreferences([]byte(tt.body))
			if got.active() != tt.active {
				t.Fatalf("active() = %v, want %v", got.active(), tt.active)
			}
			if strings.Join(got.Only, ",") != strings.Join(tt.only, ",") {
				t.Errorf("Only = %v, want %v", got.Only, tt.only)
			}
			if strings.Join(got.Ignore, ",") != strings.Join(tt.ignore, ",") {
				t.Errorf("Ignore = %v, want %v", got.Ignore, tt.ignore)
			}
			if strings.Join(got.Order, ",") != strings.Join(tt.order, ",") {
				t.Errorf("Order = %v, want %v", got.Order, tt.order)
			}
			if got.AllowFallbacks != nil && *got.AllowFallbacks == tt.noFalls {
				t.Errorf("AllowFallbacks = %v, want no-fallbacks %v", *got.AllowFallbacks, tt.noFalls)
			}
		})
	}
}

func TestStripProviderPreferencesField(t *testing.T) {
	t.Run("removes the object and keeps everything else", func(t *testing.T) {
		body := []byte(`{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hi"}],"provider":{"only":["deepseek"]}}`)
		got := string(stripProviderPreferencesField(body))
		if strings.Contains(got, `"provider"`) {
			t.Errorf("stripped body still carries the provider field: %s", got)
		}
		if !strings.Contains(got, "deepseek/deepseek-chat") || !strings.Contains(got, `"hi"`) {
			t.Errorf("stripped body lost request fields: %s", got)
		}
	})
	t.Run("body without the object is returned untouched", func(t *testing.T) {
		body := []byte(`{"model":"deepseek/deepseek-chat"}`)
		if got := stripProviderPreferencesField(body); string(got) != string(body) {
			t.Errorf("body without the object was rewritten: %s", got)
		}
	})
}

func TestProviderPreferencesFilterCandidates(t *testing.T) {
	nodes := map[string]string{"myprefix": "my-custom-node"}
	tests := []struct {
		name       string
		prefs      providerPreferences
		candidates []string
		want       []string
		wantErr    bool
	}{
		{
			name:       "inert prefs pass the list through",
			prefs:      providerPreferences{},
			candidates: []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"},
			want:       []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"},
		},
		{
			name:       "ignore drops the provider",
			prefs:      providerPreferences{Ignore: []string{"deepseek"}},
			candidates: []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"},
			want:       []string{"groq/llama-3.3-70b-versatile"},
		},
		{
			name:       "only keeps the allowlist",
			prefs:      providerPreferences{Only: []string{"groq"}},
			candidates: []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile", "openai/gpt-5"},
			want:       []string{"groq/llama-3.3-70b-versatile"},
		},
		{
			name:       "order is a stable preference, not an allowlist",
			prefs:      providerPreferences{Order: []string{"groq", "deepseek"}},
			candidates: []string{"deepseek/deepseek-chat", "openai/gpt-5", "groq/llama-3.3-70b-versatile"},
			want:       []string{"groq/llama-3.3-70b-versatile", "deepseek/deepseek-chat", "openai/gpt-5"},
		},
		{
			name:       "allow_fallbacks false turns order into a hard allowlist",
			prefs:      providerPreferences{Order: []string{"groq"}, AllowFallbacks: boolPtr(false)},
			candidates: []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"},
			want:       []string{"groq/llama-3.3-70b-versatile"},
		},
		{
			name:       "a hard order nothing survives is an error, not a fall-through",
			prefs:      providerPreferences{Order: []string{"anthropic"}, AllowFallbacks: boolPtr(false)},
			candidates: []string{"deepseek/deepseek-chat", "groq/llama-3.3-70b-versatile"},
			wantErr:    true,
		},
		{
			name:       "only that matches nothing is an error",
			prefs:      providerPreferences{Only: []string{"anthropic"}},
			candidates: []string{"deepseek/deepseek-chat"},
			wantErr:    true,
		},
		{
			name:       "aliases resolve to the canonical id",
			prefs:      providerPreferences{Ignore: []string{"oa"}},
			candidates: []string{"openai/gpt-5", "groq/llama-3.3-70b-versatile"},
			want:       []string{"groq/llama-3.3-70b-versatile"},
		},
		{
			name:       "a node prefix entry matches its node id",
			prefs:      providerPreferences{Ignore: []string{"my-custom-node"}},
			candidates: []string{"myprefix/some-model", "deepseek/deepseek-chat"},
			want:       []string{"deepseek/deepseek-chat"},
		},
		{
			name:       "an empty candidate list is passed through, not an error",
			prefs:      providerPreferences{Only: []string{"anthropic"}},
			candidates: nil,
			want:       nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.prefs.filterCandidates(tt.candidates, nodes)
			if (err != nil) != tt.wantErr {
				t.Fatalf("filterCandidates() error = %v, wantErr %v", err, tt.wantErr)
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("filterCandidates() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProviderPreferencesAllowsSingle(t *testing.T) {
	tests := []struct {
		name     string
		prefs    providerPreferences
		provider string
		wantErr  bool
	}{
		{
			name:     "no prefs permits anything",
			prefs:    providerPreferences{},
			provider: "deepseek",
		},
		{
			name:     "matching only permits",
			prefs:    providerPreferences{Only: []string{"deepseek"}},
			provider: "deepseek",
		},
		{
			name:     "outside only is rejected",
			prefs:    providerPreferences{Only: []string{"openai"}},
			provider: "deepseek",
			wantErr:  true,
		},
		{
			name:     "ignored provider is rejected",
			prefs:    providerPreferences{Ignore: []string{"groq"}},
			provider: "groq",
			wantErr:  true,
		},
		{
			name:     "order alone never rejects a single provider",
			prefs:    providerPreferences{Order: []string{"groq"}},
			provider: "deepseek",
		},
		{
			name:     "alias spellings match the canonical id",
			prefs:    providerPreferences{Only: []string{"oa"}},
			provider: "openai",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.prefs.allowsSingle(tt.provider)
			if (err != nil) != tt.wantErr {
				t.Fatalf("allowsSingle(%q) error = %v, wantErr %v", tt.provider, err, tt.wantErr)
			}
		})
	}
}

func TestSplitProviderModel(t *testing.T) {
	tests := []struct {
		entry        string
		wantProvider string
		wantModel    string
	}{
		{entry: "deepseek/deepseek-chat", wantProvider: "deepseek", wantModel: "deepseek-chat"},
		{entry: "myprefix/some/model", wantProvider: "myprefix", wantModel: "some/model"},
		{entry: "bare", wantProvider: "bare", wantModel: ""},
	}
	for _, tt := range tests {
		t.Run(tt.entry, func(t *testing.T) {
			provider, model := splitProviderModel(tt.entry)
			if provider != tt.wantProvider || model != tt.wantModel {
				t.Errorf("splitProviderModel(%q) = (%q, %q), want (%q, %q)", tt.entry, provider, model, tt.wantProvider, tt.wantModel)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }
