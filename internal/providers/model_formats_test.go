package providers

import "testing"

func TestGetModelFormats_OpenCodeZenLanes(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		model         string
		wantDeclared  bool
		wantTarget    string
		wantSupported string
	}{
		{"claude goes to messages", "ocz", "claude-sonnet-4-6", true, FormatClaude, FormatClaude},
		{"qwen goes to messages", "opencode-zen", "qwen3.6-plus", true, FormatClaude, FormatClaude},
		{"union alpha goes to messages", "ocz", "union-alpha", true, FormatClaude, FormatClaude},
		{"muse spark goes to responses", "ocz", "muse-spark-1.3", true, FormatOpenAIResponses, FormatOpenAIResponses},
		{"contributor free goes to responses", "ocz", "muse-spark-1.3-contributor-free", true, FormatOpenAIResponses, FormatOpenAIResponses},
		{"gpt family goes to responses", "ocz", "gpt-5.4", true, FormatOpenAIResponses, FormatOpenAIResponses},
		{"grok family goes to responses", "ocz", "grok-4.6", true, FormatOpenAIResponses, FormatOpenAIResponses},
		{"deepseek stays on chat", "ocz", "deepseek-v4-pro", true, FormatOpenAI, FormatOpenAI},
		{"gemini stays on chat", "ocz", "gemini-3.6-flash", true, FormatOpenAI, FormatOpenAI},
		{"thinking suffix hits the base entry", "ocz", "muse-spark-1.3(high)", true, FormatOpenAIResponses, FormatOpenAIResponses},
		{"unfetched gpt id uses the family fallback", "ocz", "gpt-9.9-imaginary", true, FormatOpenAIResponses, FormatOpenAIResponses},
		{"unfetched deepseek id uses the family fallback", "ocz", "deepseek-v4-flash-vision-exp", true, FormatOpenAI, FormatOpenAI},
		{"unfetched unknown id defaults to the chat lane", "ocz", "brand-new-zen-model", true, FormatOpenAI, FormatOpenAI},
		{"providers without transports declare nothing", "openai", "gpt-5.4", false, "", ""},
		{"free tier free-form ids keep the family fallback", "oc", "muse-spark-1.2-contributor-free", true, FormatOpenAIResponses, FormatOpenAIResponses},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, declared := GetModelFormats(tt.provider, tt.model)
			if declared != tt.wantDeclared {
				t.Fatalf("declared = %v, want %v", declared, tt.wantDeclared)
			}
			if !declared {
				return
			}
			if got.TargetFormat != tt.wantTarget {
				t.Errorf("TargetFormat = %q, want %q", got.TargetFormat, tt.wantTarget)
			}
			if tt.wantSupported != "" && !got.SupportsFormat(tt.wantSupported) {
				t.Errorf("expected %q among supported formats %v", tt.wantSupported, got.SupportedFormats)
			}
		})
	}
}

// The curated DeepSeek entry is chat-only; only ids the catalog has never seen
// fall back to the family table that also allows a Claude client.
func TestGetModelFormats_CuratedDeepSeekIsChatOnly(t *testing.T) {
	curated, _ := GetModelFormats("ocz", "deepseek-v4-pro")
	if curated.SupportsFormat(FormatClaude) {
		t.Errorf("the curated deepseek-v4-pro entry serves chat only, got %v", curated.SupportedFormats)
	}
	fallback, declared := GetModelFormats("ocz", "deepseek-v4-pro-xlarge")
	if !declared || !fallback.SupportsFormat(FormatClaude) {
		t.Errorf("an unfetched deepseek id keeps the family fallback, got %+v", fallback)
	}
}

// Kimi Code serves both lanes off one key, so a /v1/responses client is routed
// to /responses instead of being downgraded to the single chat URL.
func TestGetModelFormats_KimiDeclaresResponsesLane(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
	}{
		{"flagship", "kimi", "kimi-k3"},
		{"code subscription id", "kimi", "k3"},
		{"pay-as-you-go id", "kimi", "kimi-k2.5"},
		{"thinking suffix hits the base entry", "kimi", "kimi-k2.5-thinking(max)"},
		{"legacy provider id", "kimi-coding", "kimi-for-coding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, declared := GetModelFormats(tt.provider, tt.model)
			if !declared {
				t.Fatalf("kimi models must declare a format contract")
			}
			if got.TargetFormat != FormatOpenAIResponses {
				t.Errorf("TargetFormat = %q, want %q", got.TargetFormat, FormatOpenAIResponses)
			}
			if !got.SupportsFormat(FormatOpenAIResponses) {
				t.Errorf("expected %q among supported formats %v", FormatOpenAIResponses, got.SupportedFormats)
			}
			if !got.SupportsFormat(FormatOpenAI) {
				t.Errorf("a chat client must keep its own lane, got %v", got.SupportedFormats)
			}
		})
	}
}

func TestIsOpencodeFreeTierModel(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     bool
	}{
		{"big-pickle on opencode", "opencode", "big-pickle", false},
		{"big-pickle on opencode-zen", "opencode-zen", "big-pickle", false},
		{"big-pickle on opencode-go", "opencode-go", "big-pickle", false},
		{"big-pickle on antigravity", "antigravity", "big-pickle", false},
		{"big-pickle on alias ocz", "ocz", "big-pickle", false},
		{"big-pickle on unrelated provider", "openai", "big-pickle", false},
		{"uncataloged free model on antigravity", "antigravity", "longcat-2.5-preview-free", false},
		{"uncataloged free model on opencode", "opencode", "longcat-2.5-preview-free", true},
		{"uncataloged free model on opencode-zen", "opencode-zen", "longcat-2.5-preview-free", true},
		{"uncataloged free model on opencode-go", "opencode-go", "longcat-2.5-preview-free", true},
		{"uncataloged free model on alias oc", "oc", "longcat-2.5-preview-free", true},
		{"uncataloged free model on alias ocz", "ocz", "longcat-2.5-preview-free", true},
		{"uncataloged free model on unrelated provider", "openai", "longcat-2.5-preview-free", false},
		{"curated free model on opencode-zen", "opencode-zen", "deepseek-v4-flash-free", true},
		{"curated free model on opencode-go", "opencode-go", "space-bunny-free", true},
		{"non-free model on opencode", "opencode", "gpt-5", false},
		{"non-free model on opencode-zen", "opencode-zen", "deepseek-v4-pro", false},
		{"non-free model on antigravity", "antigravity", "claude-sonnet-4-6", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsOpencodeFreeTierModel(tt.provider, tt.model)
			if got != tt.want {
				t.Errorf("IsOpencodeFreeTierModel(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

func TestGetModelFormats_OpenCodeGoLanes(t *testing.T) {
	tests := []struct {
		name          string
		provider      string
		model         string
		wantDeclared  bool
		wantTarget    string
		wantSupported string
	}{
		{"gemini argon contributor goes to gemini", "opencode-go", "gemini-4-argon-contributor", true, FormatGemini, FormatGemini},
		{"unknown gemini goes to gemini via family fallback", "opencode-go", "gemini-2.5-flash", true, FormatGemini, FormatGemini},
		{"claude haiku goes to claude", "opencode-go", "claude-haiku-5-5", true, FormatClaude, FormatClaude},
		{"longcat free goes to openai", "opencode-go", "longcat-2.5-preview-free", true, FormatOpenAI, FormatOpenAI},
		{"step 5 preview goes to openai", "opencode-go", "step-5-preview", true, FormatOpenAI, FormatOpenAI},
		{"beam free goes to openai", "opencode-go", "beam-free", true, FormatOpenAI, FormatOpenAI},
		{"ocg alias resolves same", "ocg", "gemini-4-argon-contributor", true, FormatGemini, FormatGemini},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, declared := GetModelFormats(tt.provider, tt.model)
			if declared != tt.wantDeclared {
				t.Fatalf("declared = %v, want %v", declared, tt.wantDeclared)
			}
			if !declared {
				return
			}
			if got.TargetFormat != tt.wantTarget {
				t.Errorf("TargetFormat = %q, want %q", got.TargetFormat, tt.wantTarget)
			}
			if tt.wantSupported != "" && !got.SupportsFormat(tt.wantSupported) {
				t.Errorf("expected %q among supported formats %v", tt.wantSupported, got.SupportedFormats)
			}
		})
	}
}
