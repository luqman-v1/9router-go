package chat

import (
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/models"
)

func TestApplyCliModelMapping_GatesInOrder(t *testing.T) {
	tests := []struct {
		name    string
		ua      string
		key     *models.APIKey
		request string
		want    string
	}{
		{
			name:    "no user agent passes through",
			ua:      "",
			key:     &models.APIKey{ID: "k1", Scopes: []string{"routing:cli_mapping"}},
			request: "opus",
			want:    "opus",
		},
		{
			name:    "non cli user agent passes through",
			ua:      "curl/8.5.0",
			key:     &models.APIKey{ID: "k1", Scopes: []string{"routing:cli_mapping"}},
			request: "opus",
			want:    "opus",
		},
		{
			name:    "cli user agent without scope passes through",
			ua:      "claude-cli/1.0.0",
			key:     &models.APIKey{ID: "k1"},
			request: "opus",
			want:    "opus",
		},
		{
			name:    "keyless cli request passes through",
			ua:      "claude-cli/1.0.0",
			key:     nil,
			request: "opus",
			want:    "opus",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			r.Header.Set("User-Agent", tc.ua)
			got := (&ChatHandler{}).applyCliModelMapping(r, tc.request)
			if got != tc.want {
				t.Errorf("applyCliModelMapping(%q, ua=%q) = %q, want %q", tc.request, tc.ua, got, tc.want)
			}
		})
	}
}

func TestDetectCliToolID_KnownUserAgents(t *testing.T) {
	tests := []struct {
		ua   string
		want string
	}{
		{"claude-cli/1.0.0 (external, cli)", "claude"},
		{"claude-code/2.1.4", "claude"},
		{"curl/8.5.0", ""},
		{"", ""},
		{"openclaw/1.0 claude-cli/2.0", "claude"},
		{"myclaude-cli/1.0", ""}, // boundary: alpha before the match refuses
	}
	for _, tc := range tests {
		if got := db.DetectCliToolID(tc.ua); got != tc.want {
			t.Errorf("DetectCliToolID(%q) = %q, want %q", tc.ua, got, tc.want)
		}
	}
}

func TestHasCliMappingScope(t *testing.T) {
	if db.HasCliMappingScope(nil) {
		t.Error("nil key must not carry the scope")
	}
	key := &models.APIKey{Scopes: []string{"routing:invoke"}}
	if db.HasCliMappingScope(key) {
		t.Error("routing:invoke must not imply routing:cli_mapping (flat grants, not a ladder)")
	}
	key.Scopes = append(key.Scopes, "routing:cli_mapping")
	if !db.HasCliMappingScope(key) {
		t.Error("explicit grant must pass")
	}
}

func TestNormalizeCliSlot_VariantMarkers(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"claude-opus-5[1m]", "claude-opus-5"},
		{"opus", "opus"},
		{"sonnet", "sonnet"},
	}
	for _, tc := range tests {
		if got := normalizeCliSlot(tc.in); got != tc.want {
			t.Errorf("normalizeCliSlot(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCliMappingSourceKeys(t *testing.T) {
	if got := cliMappingSourceKeys("opus"); len(got) != 4 {
		t.Errorf("opus should expand to 4 source keys, got %d: %v", len(got), got)
	}
	if got := cliMappingSourceKeys("custom-slot"); len(got) != 1 || got[0] != "custom-slot" {
		t.Errorf("unknown slot must pass through, got %v", got)
	}
}
