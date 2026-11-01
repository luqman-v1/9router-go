package providers

import "testing"

func TestIsDiscoverableAntigravityModel(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "current flash is callable", id: "gemini-3.1-flash", want: true},
		{name: "pro high is callable", id: "gemini-3.1-pro-high", want: true},
		{name: "unknown new id is callable by default", id: "gemini-9-ultra", want: true},
		{name: "non-chat image surface", id: "gemini-3-pro-image-preview", want: false},
		{name: "non-chat tts surface", id: "gemini-3.1-flash-tts-preview", want: false},
		{name: "retired id", id: "gemini-2.5-flash", want: false},
		{name: "retired pro preview", id: "gemini-3-pro-preview", want: false},
		{name: "internal chat slot", id: "chat_20706", want: false},
		{name: "internal tab slot", id: "tab_flash_lite_preview", want: false},
		{name: "image suffix", id: "some-model-image", want: false},
		{name: "embedding", id: "text-embedding-004", want: false},
		{name: "veo video", id: "veo-3", want: false},
		{name: "empty", id: "", want: false},
		{name: "whitespace", id: "   ", want: false},
		// Segment-boundary matching: a legitimate id that merely contains the
		// letters must never be rejected.
		{name: "imagen inside a word is not a surface", id: "gemini-3-imagination", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDiscoverableAntigravityModel(tt.id); got != tt.want {
				t.Errorf("IsDiscoverableAntigravityModel(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestGitHubCopilotChatHeaders(t *testing.T) {
	headers := GitHubCopilotChatHeaders("", "")
	want := map[string]string{
		"copilot-integration-id": "copilot-developer-cli",
		"openai-intent":          "conversation-agent",
		"x-interaction-type":     "conversation-user",
		"copilot-harness-id":     "copilot-sdk",
		"x-github-api-version":   GitHubCopilotAPIVersion,
		"Accept":                 "application/json",
		"Content-Type":           "application/json",
		"X-Initiator":            "user",
	}
	for k, v := range want {
		if got := headers[k]; got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	// The VS Code extension identity must NOT appear: an over-complete
	// fingerprint is itself a flagging signal.
	for _, absent := range []string{
		"editor-plugin-version",
		"x-vscode-user-agent-library-version",
	} {
		if _, ok := headers[absent]; ok {
			t.Errorf("header %s must not be sent on the CLI identity path", absent)
		}
	}
	// The machine id must be stable across calls: minting a fresh random id
	// per request would itself be an anti-fingerprint tell. The two values are
	// captured before comparison so the assertion compares stored results
	// rather than re-invoking the same expression.
	first, second := GitHubCopilotMachineID(), GitHubCopilotMachineID()
	if first != second {
		t.Errorf("GitHubCopilotMachineID() is not stable across calls: %q vs %q", first, second)
	}
}

func TestGitHubCopilotCLIVersionOverride(t *testing.T) {
	const env = "GITHUB_COPILOT_CLI_VERSION"
	t.Setenv(env, "1.2.3")
	if got := GitHubCopilotCLIVersionString(); got != "1.2.3" {
		t.Errorf("GitHubCopilotCLIVersionString() = %q, want 1.2.3", got)
	}
	// A value that is not version-shaped must be ignored rather than injected
	// into a header.
	t.Setenv(env, "evil\r\nX-Injected: 1")
	if got := GitHubCopilotCLIVersionString(); got != GitHubCopilotCLIVersion {
		t.Errorf("GitHubCopilotCLIVersionString() = %q, want the built-in constant", got)
	}
}
