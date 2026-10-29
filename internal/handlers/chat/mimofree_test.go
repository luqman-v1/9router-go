package chat

import (
	json "encoding/json/v2"
	"testing"
)

func mimoBody(t *testing.T, model, effort string) []byte {
	t.Helper()
	body := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	if effort != "" {
		body["reasoning_effort"] = effort
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func mimoEffort(t *testing.T, body []byte) string {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, ok := out["reasoning_effort"].(string); ok {
		return v
	}
	return ""
}

func TestInjectMimoMarker_ClampsEffort(t *testing.T) {
	tests := []struct {
		name  string
		model string
		in    string
		want  string
	}{
		{
			// mimo-v2.5-pro and v2.6 answer 400 to "max" on this lane; v2.5
			// accepts it, so the clamp must follow the declared levels and not
			// the model family.
			name:  "v2.5-pro downgrades max",
			model: "mimo-v2.5-pro",
			in:    "max",
			want:  "high",
		},
		{
			name:  "v2.6 downgrades xhigh",
			model: "mimo-v2.6-flash",
			in:    "xhigh",
			want:  "high",
		},
		{
			name:  "v2.5 keeps max",
			model: "mimo-v2.5-free",
			in:    "max",
			want:  "max",
		},
		{
			name:  "high is untouched",
			model: "mimo-v2.5-pro",
			in:    "high",
			want:  "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := injectMimoMarker(mimoBody(t, tt.model, tt.in))
			if got := mimoEffort(t, out); got != tt.want {
				t.Errorf("reasoning_effort = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInjectMimoMarker_NoEffortIsLeftAlone(t *testing.T) {
	out := injectMimoMarker(mimoBody(t, "mimo-v2.5-pro", ""))

	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["reasoning_effort"]; ok {
		t.Error("reasoning_effort must not be invented for a request that had none")
	}
}

// The marker injection must survive the refactor that added the effort clamp:
// the anti-abuse system message is what keeps the free endpoint from 403ing.
func TestInjectMimoMarker_StillInjectsMarker(t *testing.T) {
	out := injectMimoMarker(mimoBody(t, "mimo-v2.5-pro", "max"))

	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %v, want the marker prepended", decoded["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" || first["content"] != mimoSystemMarker {
		t.Errorf("first message = %v, want the anti-abuse system marker", first)
	}
}

// TestClampMimoFreeReasoning pins the mandatory-reasoning clamp on the
// mimo-free lane. The lane's endpoints are hardcoded (mimoBootstrapURL,
// mimoChatURL), so it cannot be faked end to end; the clamp itself is the
// part that decides what the upstream receives.
func TestClampMimoFreeReasoning(t *testing.T) {
	out := clampMimoFreeReasoning(mimoBody(t, "mimo-v2.6", "none"), "mimo-v2.6")
	if got := mimoEffort(t, out); got != "minimal" {
		t.Errorf("reasoning_effort = %q, want minimal (the free lane forbids disabling)", got)
	}
	out = clampMimoFreeReasoning(mimoBody(t, "mimo-v2.6", "high"), "mimo-v2.6")
	if got := mimoEffort(t, out); got != "high" {
		t.Errorf("reasoning_effort = %q, want high (another level is untouched)", got)
	}
	out = clampMimoFreeReasoning(mimoBody(t, "mimo-v2.6", ""), "mimo-v2.6")
	if got := mimoEffort(t, out); got != "" {
		t.Errorf("reasoning_effort = %q, want empty (no ask stays no ask)", got)
	}
}
