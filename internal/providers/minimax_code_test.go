package providers

import (
	"slices"
	"testing"
	"time"
)

// The mcode providers have to be complete entries, not just ids: a missing
// base URL or alias strands a saved connection, and a missing alias makes the
// model unreachable by the prefix the dashboard shows.
func TestMiniMaxCodeRegistry(t *testing.T) {
	tests := []struct {
		provider string
		baseURL  string
		alias    string
	}{
		{
			provider: "minimax-code",
			baseURL:  "https://agent.minimax.cn/mavis/api/v1/llm/v1/messages",
			alias:    "mmc",
		},
		{
			provider: "minimax-code-global",
			baseURL:  "https://agent.minimax.io/mavis/api/v1/llm/v1/messages",
			alias:    "mmg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			cfg, ok := KnownProviders[tt.provider]
			if !ok {
				t.Fatalf("%s is not in KnownProviders", tt.provider)
			}
			if cfg.BaseURL != tt.baseURL {
				t.Errorf("BaseURL = %q, want %q", cfg.BaseURL, tt.baseURL)
			}
			// Claude Messages is the wire format; it is what routes a
			// /v1/messages client to this endpoint untranslated.
			if cfg.Format != FormatClaude {
				t.Errorf("Format = %q, want %q", cfg.Format, FormatClaude)
			}
			// The credential is the OAuth bearer, not an x-api-key key.
			if cfg.AuthHeader != "Authorization" || cfg.AuthScheme != "bearer" {
				t.Errorf("auth = %q/%q, want Authorization/bearer", cfg.AuthHeader, cfg.AuthScheme)
			}
			for header, want := range map[string]string{
				"User-Agent":        "MiniMaxAgent",
				"X-Mavis-Agent-Id":  "main",
				"anthropic-version": "2023-06-01",
			} {
				if got := cfg.StaticHeaders[header]; got != want {
					t.Errorf("StaticHeaders[%q] = %q, want %q", header, got, want)
				}
			}

			if got := GetProviderAlias(tt.provider); got != tt.alias {
				t.Errorf("GetProviderAlias = %q, want %q", got, tt.alias)
			}
			if got := ResolveAlias(tt.alias); got != tt.provider {
				t.Errorf("ResolveAlias(%q) = %q, want %q", tt.alias, got, tt.provider)
			}
			if got := RefreshLead(tt.provider); got != 10*time.Minute {
				t.Errorf("RefreshLead = %v, want 10m", got)
			}
		})
	}
}

// The two mcode sites must stay separate providers. An account on the China
// site says nothing about the global one, and collapsing them would route a
// token to a host that never issued it (AGENTS.md §3.A).
func TestMiniMaxCodeSitesAreNotAliased(t *testing.T) {
	for _, alias := range []string{"mmc", "mmg", "minimax-code", "minimax-code-global"} {
		resolved := ResolveAlias(alias)
		if resolved != "minimax-code" && resolved != "minimax-code-global" {
			t.Errorf("ResolveAlias(%q) = %q, want an mcode provider", alias, resolved)
		}
	}
	if ResolveAlias("mmc") == ResolveAlias("mmg") {
		t.Fatal("the two mcode sites resolve to one provider")
	}
	// `mm` belongs to the API-key provider and must stay there.
	if got := ResolveAlias("mm"); got != "minimax" {
		t.Errorf("ResolveAlias(mm) = %q, want the API-key minimax provider", got)
	}
}

// The catalog has to carry the models the registry publishes, or the dashboard
// shows an empty provider the moment it is connected.
func TestMiniMaxCodeModels(t *testing.T) {
	want := []string{"MiniMax-M3.1-Flash-Preview", "MiniMax-M3", "MiniMax-M2.7", "MiniMax-M2.7-highspeed"}
	for _, provider := range []string{"minimax-code", "minimax-code-global"} {
		models := ProviderModels[provider]
		if len(models) == 0 {
			t.Errorf("no catalog models for %s", provider)
			continue
		}
		for _, model := range want {
			if !slices.Contains(models, model) {
				t.Errorf("%s catalog is missing %s", provider, model)
			}
		}
	}
}

// The credits lane thinks in adaptive effort: the declared levels are what the
// dashboard picker offers, so a wrong set is a 400 waiting to happen.
func TestMiniMaxCodeThinkingLevels(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		want     []string
	}{
		{"minimax-code", "MiniMax-M3.1-Flash-Preview", []string{"low", "medium", "high", "xhigh", "max"}},
		{"minimax-code", "MiniMax-M3", []string{"none", "high"}},
		{"minimax-code", "MiniMax-M2.7", []string{"low", "medium", "high", "xhigh", "max"}},
		{"minimax-code-global", "MiniMax-M3.1-Flash-Preview", []string{"low", "medium", "high", "xhigh", "max"}},
		{"minimax-code-global", "MiniMax-M3", []string{"none", "high"}},
		// The published aliases must resolve to the same sets as the ids.
		{"mmc", "MiniMax-M3", []string{"none", "high"}},
		{"mmg", "MiniMax-M3.1-Flash-Preview", []string{"low", "medium", "high", "xhigh", "max"}},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			got := GetThinkingLevels(tt.provider, tt.model)
			if !slices.Equal(got, tt.want) {
				t.Errorf("GetThinkingLevels = %v, want %v", got, tt.want)
			}
		})
	}
}

// M3.1 and the M2.7 pair cannot switch thinking off; M3 can. Getting this
// backwards sends an unsupported thinking block on every request.
func TestMiniMaxCodeCapabilities(t *testing.T) {
	tests := []struct {
		model      string
		vision     bool
		canDisable bool
	}{
		{"MiniMax-M3.1-Flash-Preview", true, false},
		{"MiniMax-M3", true, true},
		{"MiniMax-M2.7", false, false},
		{"MiniMax-M2.7-highspeed", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			caps := GetCapabilitiesForModel("minimax-code", tt.model)
			if caps.ThinkingFormat != "claude-adaptive" {
				t.Errorf("ThinkingFormat = %q, want claude-adaptive", caps.ThinkingFormat)
			}
			if !caps.Reasoning {
				t.Error("Reasoning = false, want true")
			}
			if caps.Vision != tt.vision {
				t.Errorf("Vision = %v, want %v", caps.Vision, tt.vision)
			}
			if got := canDisableThinking(caps); got != tt.canDisable {
				t.Errorf("thinking can be disabled = %v, want %v", got, tt.canDisable)
			}
			// The global site serves the same catalog, so it must answer
			// identically rather than falling back to the generic rows.
			if global := GetCapabilitiesForModel("minimax-code-global", tt.model); global != caps {
				t.Errorf("global caps = %+v, want the same as %+v", global, caps)
			}
		})
	}
}
