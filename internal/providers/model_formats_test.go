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
