package executor

import (
	"encoding/json"
	"testing"

	"9router/proxy/internal/providers"
)

func TestKiroUpstreamBody_BuilderIDWithoutArnGetsDefaultProfile(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)
	tests := []struct {
		name     string
		connData map[string]any
		want     string
	}{
		{
			name:     "builder-id without arn gets the builder default",
			connData: map[string]any{"authMethod": "builder-id"},
			want:     providers.KiroDefaultProfileARNBuilderID,
		},
		{
			name:     "builder-id with an explicit arn keeps it",
			connData: map[string]any{"authMethod": "builder-id", "profileArn": "arn:aws:codewhisperer:eu-west-1:1:profile/own"},
			want:     "arn:aws:codewhisperer:eu-west-1:1:profile/own",
		},
		{
			name:     "idc without arn sends no profile",
			connData: map[string]any{"authMethod": "idc"},
			want:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := kiroUpstreamBody(&Request{Body: body, ModelName: "claude-sonnet-4.5", ConnData: tt.connData})
			if err != nil {
				t.Fatalf("kiroUpstreamBody: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			got, _ := m["profileArn"].(string)
			if got != tt.want {
				t.Errorf("profileArn = %q, want %q", got, tt.want)
			}
		})
	}
}
