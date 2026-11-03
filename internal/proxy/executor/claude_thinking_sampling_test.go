package executor

import (
	json "encoding/json/v2"
	"testing"
)

func TestEnforceClaudeThinkingSampling(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantHasTemp bool
		wantTemp    float64
		wantHasTopP bool
	}{
		{
			name:        "thinking enabled with temperature pins to 1.0",
			input:       `{"model":"claude-3-7-sonnet","thinking":{"type":"enabled","budget_tokens":2048},"temperature":0.7}`,
			wantHasTemp: true,
			wantTemp:    1.0,
			wantHasTopP: false,
		},
		{
			name:        "thinking enabled with top_p converts to temperature 1.0 and drops top_p",
			input:       `{"model":"claude-3-7-sonnet","thinking":{"type":"enabled","budget_tokens":2048},"top_p":0.9}`,
			wantHasTemp: true,
			wantTemp:    1.0,
			wantHasTopP: false,
		},
		{
			name:        "thinking adaptive with both pins temperature and drops top_p",
			input:       `{"model":"claude-3-7-sonnet","thinking":{"type":"adaptive"},"temperature":0.5,"top_p":0.8}`,
			wantHasTemp: true,
			wantTemp:    1.0,
			wantHasTopP: false,
		},
		{
			name:        "thinking enabled without sampling params leaves temperature unset",
			input:       `{"model":"claude-3-7-sonnet","thinking":{"type":"enabled","budget_tokens":2048}}`,
			wantHasTemp: false,
			wantHasTopP: false,
		},
		{
			name:        "thinking disabled with temperature leaves temperature unchanged",
			input:       `{"model":"claude-3-7-sonnet","thinking":{"type":"disabled"},"temperature":0.7}`,
			wantHasTemp: true,
			wantTemp:    0.7,
			wantHasTopP: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal([]byte(tt.input), &m); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			enforceClaudeThinkingSampling(m)

			temp, hasTemp := m["temperature"]
			if hasTemp != tt.wantHasTemp {
				t.Errorf("hasTemp = %v, want %v", hasTemp, tt.wantHasTemp)
			}
			if tt.wantHasTemp {
				if tempVal, ok := temp.(float64); !ok || tempVal != tt.wantTemp {
					t.Errorf("temperature = %v, want %v", temp, tt.wantTemp)
				}
			}

			_, hasTopP := m["top_p"]
			if hasTopP != tt.wantHasTopP {
				t.Errorf("hasTopP = %v, want %v", hasTopP, tt.wantHasTopP)
			}
		})
	}
}

func TestEnsureClaudeMessagesFor_ThinkingTemperatureSampling(t *testing.T) {
	// 1. Without sampling params: temperature is NOT injected
	in1 := `{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`
	out1 := EnsureClaudeMessagesFor([]byte(in1), "claude", "claude-3-7-sonnet")
	var m1 map[string]any
	_ = json.Unmarshal(out1, &m1)
	if _, hasTemp := m1["temperature"]; hasTemp {
		t.Errorf("expected no temperature injected when omitted, got %v", m1["temperature"])
	}

	// 2. With temperature: pinned to 1.0
	in2 := `{"model":"claude-3-7-sonnet","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high","temperature":0.3}`
	out2 := EnsureClaudeMessagesFor([]byte(in2), "claude", "claude-3-7-sonnet")
	var m2 map[string]any
	_ = json.Unmarshal(out2, &m2)
	if temp, ok := m2["temperature"].(float64); !ok || temp != 1.0 {
		t.Errorf("expected temperature=1.0, got %v", m2["temperature"])
	}
}
