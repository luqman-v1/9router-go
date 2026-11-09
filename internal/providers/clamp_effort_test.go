package providers

import "testing"

func TestClampDeepseekEffort(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		level    string
		want     string
	}{
		{
			name:     "max survives when the model declares it",
			provider: "mimo-free",
			model:    "mimo-v2.5-free",
			level:    "max",
			want:     "max",
		},
		{
			// mimo-v2.5-pro and v2.6 answer 400 to "max" on the Go lane (upstream
			// probed this live); v2.5 accepts it, so the clamp has to key off the
			// declared levels rather than the model name.
			name:     "max downgrades when the level set omits it",
			provider: "mimo-free",
			model:    "mimo-v2.5-pro",
			level:    "max",
			want:     "high",
		},
		{
			name:     "xhigh downgrades the same way",
			provider: "mimo-free",
			model:    "mimo-v2.6-flash",
			level:    "xhigh",
			want:     "high",
		},
		{
			name:     "high is already the floor for xhigh models",
			provider: "mimo-free",
			model:    "mimo-v2.5-pro",
			level:    "high",
			want:     "high",
		},
		{
			name:     "medium maps to high",
			provider: "mimo-free",
			model:    "mimo-v2.5-pro",
			level:    "medium",
			want:     "high",
		},
		{
			// Upstream 313559f4: the declared levels win over the DeepSeek
			// low→high collapse — mimo-v2.5-pro declares low, so it goes out
			// verbatim now.
			name:     "low survives when the level set declares it",
			provider: "mimo-free",
			model:    "mimo-v2.5-pro",
			level:    "low",
			want:     "low",
		},
		{
			// zen's free deepseek-v4.1-flash publishes low|high|max and its
			// upstream takes the level verbatim.
			name:     "zen free deepseek keeps low",
			provider: "opencode",
			model:    "deepseek-v4.1-flash-free",
			level:    "low",
			want:     "low",
		},
		{
			name:     "zen free deepseek maps xhigh onto its declared max",
			provider: "opencode",
			model:    "deepseek-v4.1-flash-free",
			level:    "xhigh",
			want:     "max",
		},
		{
			name:     "zen free deepseek has no medium, so it clamps to high",
			provider: "opencode",
			model:    "deepseek-v4.1-flash-free",
			level:    "medium",
			want:     "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClampDeepseekEffort(tt.provider, tt.model, tt.level); got != tt.want {
				t.Errorf("ClampDeepseekEffort(%q, %q, %q) = %q, want %q",
					tt.provider, tt.model, tt.level, got, tt.want)
			}
		})
	}
}

// A level set that declares no "max" at all must still be downgraded rather than
// passing "max" through: an absent level set means the model never advertised max.
func TestClampDeepseekEffort_UndeclaredMaxIsDowngraded(t *testing.T) {
	if got := ClampDeepseekEffort("no-such-provider", "no-such-model", "max"); got != "high" {
		t.Errorf("undeclared model: got %q, want high", got)
	}
}
