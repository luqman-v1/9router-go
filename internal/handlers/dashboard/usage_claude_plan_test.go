package dashboard

import "testing"

// The OAuth profile publishes organization_type as an enum and the multiplier
// separately, so both have to be read to render "Max 20x" rather than "Max".
func TestClaudeProfilePlan(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "max 20x from the rate-limit tier",
			body: `{"organization":{"organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x"}}`,
			want: "Max 20x",
		},
		{
			name: "max 5x",
			body: `{"organization":{"organization_type":"claude_max","rate_limit_tier":"default_claude_max_5x"}}`,
			want: "Max 5x",
		},
		{
			name: "a max seat with no multiplier is still max",
			body: `{"organization":{"organization_type":"claude_max"}}`,
			want: "Max",
		},
		{
			name: "pro",
			body: `{"organization":{"organization_type":"claude_pro"}}`,
			want: "Pro",
		},
		{
			name: "team and enterprise",
			body: `{"organization":{"organization_type":"claude_team"}}`,
			want: "Team",
		},
		{
			name: "an unknown seat yields no badge rather than a guess",
			body: `{"organization":{"organization_type":"claude_desktop"}}`,
			want: "",
		},
		{
			name: "a profile with no organization",
			body: `{"account":{"uuid":"x"}}`,
			want: "",
		},
		{
			name: "a body that is not json",
			body: `not json`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeProfilePlan([]byte(tt.body)); got != tt.want {
				t.Errorf("plan = %q, want %q", got, tt.want)
			}
		})
	}
}

// A Max seat's multiplier is the only thing rate_limit_tier refines; it must
// never turn a Pro seat into a Max one.
func TestClaudeProfilePlan_TierDoesNotLeakAcrossSeats(t *testing.T) {
	body := []byte(`{"organization":{"organization_type":"claude_pro","rate_limit_tier":"default_claude_max_20x"}}`)
	if got := claudeProfilePlan(body); got != "Pro" {
		t.Errorf("plan = %q, want Pro", got)
	}
}
