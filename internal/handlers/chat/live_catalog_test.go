package chat

import (
	"testing"
)

func TestExpandKiroVariants(t *testing.T) {
	tests := []struct {
		name    string
		upID    string
		wantIDs []string
	}{
		{
			name:    "auto skips agentic variants",
			upID:    "auto",
			wantIDs: []string{"auto", "auto-thinking"},
		},
		{
			name:    "regular model expands to four variants",
			upID:    "claude-sonnet-4",
			wantIDs: []string{"claude-sonnet-4", "claude-sonnet-4-thinking", "claude-sonnet-4-agentic", "claude-sonnet-4-thinking-agentic"},
		},
		{
			name:    "synthetic suffix is stripped first",
			upID:    "claude-sonnet-4-thinking-agentic",
			wantIDs: []string{"claude-sonnet-4", "claude-sonnet-4-thinking", "claude-sonnet-4-agentic", "claude-sonnet-4-thinking-agentic"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandKiroVariants(tt.upID, "Kiro x", 200000)
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("expandKiroVariants(%q) returned %d variants (%v), want %d", tt.upID, len(got), idsOf(got), len(tt.wantIDs))
			}
			for i, want := range tt.wantIDs {
				if got[i].ID != want {
					t.Errorf("variant %d = %q, want %q (all: %v)", i, got[i].ID, want, idsOf(got))
				}
			}
			if got[0].ContextLength != 200000 {
				t.Errorf("expected context length 200000, got %d", got[0].ContextLength)
			}
		})
	}
}

func TestKiroDisplayName(t *testing.T) {
	tests := []struct {
		name   string
		model  string
		id     string
		rate   float64
		expect string
	}{
		{name: "rate 1 omits multiplier", model: "Sonnet", id: "claude-sonnet-4", rate: 1, expect: "Kiro Sonnet"},
		{name: "custom rate is shown", model: "Sonnet", id: "claude-sonnet-4", rate: 2.5, expect: "Kiro Sonnet (2.5x credit)"},
		{name: "falls back to id", model: "", id: "auto", rate: 1, expect: "Kiro auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kiroDisplayName(tt.model, tt.id, tt.rate); got != tt.expect {
				t.Errorf("kiroDisplayName(%q, %q, %v) = %q, want %q", tt.model, tt.id, tt.rate, got, tt.expect)
			}
		})
	}
}

func TestParseGrokCLILiveModels(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []LiveModel
	}{
		{
			name: "data envelope",
			body: `{"data":[{"id":"grok-build","context_length":300000},{"model_id":"grok-4.7"}]}`,
			want: []LiveModel{
				{ID: "grok-build", ContextLength: 300000, MaxOutput: 64000},
				{ID: "grok-4.7"},
			},
		},
		{
			name: "bare array",
			body: `[{"id":"grok-4.5-high","maxOutputTokens":8000}]`,
			want: []LiveModel{{ID: "grok-4.5-high", MaxOutput: 8000}},
		},
		{
			name: "object map keyed by id",
			body: `{"grok-4.6":{"contextWindow":128000}}`,
			want: []LiveModel{{ID: "grok-4.6", ContextLength: 128000}},
		},
		{
			name: "duplicates collapse",
			body: `{"data":[{"id":"grok-4.5"},{"id":"grok-4.5"}]}`,
			want: []LiveModel{{ID: "grok-4.5"}},
		},
		{
			name: "unparseable body yields nothing",
			body: `not-json`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGrokCLILiveModels([]byte(tt.body))
			if len(got) != len(tt.want) {
				t.Fatalf("parseGrokCLILiveModels(%s) = %v, want %v", tt.body, idsOf(got), idsOf(tt.want))
			}
			for i := range tt.want {
				if got[i].ID != tt.want[i].ID {
					t.Errorf("model %d id = %q, want %q", i, got[i].ID, tt.want[i].ID)
				}
				if got[i].ContextLength != tt.want[i].ContextLength {
					t.Errorf("model %d contextLength = %d, want %d", i, got[i].ContextLength, tt.want[i].ContextLength)
				}
				if got[i].MaxOutput != tt.want[i].MaxOutput {
					t.Errorf("model %d maxOutput = %d, want %d", i, got[i].MaxOutput, tt.want[i].MaxOutput)
				}
			}
		})
	}
}

// kiroRegion decides the q.* host a catalog request goes to. A region the host
// does not serve must not reach the hostname: the host does not exist there, so
// the request NXDOMAINs instead of answering.
func TestKiroRegion(t *testing.T) {
	tests := []struct {
		name       string
		profileArn string
		want       string
	}{
		{name: "served region passes through", profileArn: "arn:aws:codewhisperer:eu-central-1:123456789012:profile/ABC", want: "eu-central-1"},
		{name: "us-east-1 passes through", profileArn: "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC", want: "us-east-1"},
		{name: "unserved ARN region falls back", profileArn: "arn:aws:codewhisperer:eu-west-1:123456789012:profile/ABC", want: kiroDefaultRegion},
		{name: "empty ARN falls back", profileArn: "", want: kiroDefaultRegion},
		{name: "non-ARN falls back", profileArn: "not-an-arn", want: kiroDefaultRegion},
		{name: "hostile region cannot reach the hostname", profileArn: "arn:aws:codewhisperer:x.attacker.tld:1:profile/A", want: kiroDefaultRegion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kiroRegion(tt.profileArn); got != tt.want {
				t.Errorf("kiroRegion(%q) = %q, want %q", tt.profileArn, got, tt.want)
			}
		})
	}
}

func idsOf(models []LiveModel) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	return out
}
