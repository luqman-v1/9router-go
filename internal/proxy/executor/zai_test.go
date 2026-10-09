package executor

import (
	json "encoding/json/v2"
	"testing"
)

// applyZaiThinking rewrites a client's thinking request into the dialect Z.ai
// accepts. Each case is a way the old passthrough produced a request Z.ai
// rejects, so the assertions are on the exact wire fields rather than on
// "something was written".

func TestApplyZaiThinking(t *testing.T) {
	tests := []struct {
		name  string
		model string
		in    string
		want  map[string]any
	}{
		{
			// The #4656 bug: GLM-5.3 rejects thinking.type:disabled with
			// 400 code 1210, so a request for no reasoning must be clamped
			// to the lowest effort instead of being switched off.
			name:  "glm-5.3 clamps a none request instead of disabling",
			model: "glm-5.3",
			in:    `{"model":"glm-5.3","reasoning_effort":"none"}`,
			want: map[string]any{
				"reasoning_effort": "low",
				"thinking":         map[string]any{"type": "enabled"},
			},
		},
		{
			// Same rule on the exact-model entry. glm-5.3-flash declares no
			// thinkingEffortSupported upstream either, so reasoning_effort is
			// dropped and only the clamped thinking object goes out.
			name:  "glm-5.3-flash clamps a none request",
			model: "glm-5.3-flash",
			in:    `{"model":"glm-5.3-flash","reasoning_effort":"none"}`,
			want: map[string]any{
				"thinking": map[string]any{"type": "enabled"},
			},
		},
		{
			// thinkingCanDisable is false on the glm-5.2 exact entry, so a
			// none request is clamped there too rather than disabled.
			name:  "glm-5.2 clamps a none request",
			model: "glm-5.2",
			in:    `{"model":"glm-5.2","reasoning_effort":"none"}`,
			want: map[string]any{
				"thinking": map[string]any{"type": "enabled"},
			},
		},
		{
			// A model that CAN disable uses enable_thinking, because z.ai
			// ignores thinking.type:disabled outright.
			name:  "a switchable model disables via enable_thinking",
			model: "glm-4.6v",
			in:    `{"model":"glm-4.6v","reasoning_effort":"none"}`,
			want: map[string]any{
				"enable_thinking": false,
			},
		},
		{
			name:  "off is read as none",
			model: "glm-4.6v",
			in:    `{"model":"glm-4.6v","reasoning_effort":"off"}`,
			want: map[string]any{
				"enable_thinking": false,
			},
		},
		{
			// z.ai accepts exactly low|high|max. medium maps to high and
			// minimal to low; anything above high becomes max.
			name:  "effort is mapped onto the three values z.ai accepts",
			model: "glm-5.3",
			in:    `{"model":"glm-5.3","reasoning_effort":"medium"}`,
			want: map[string]any{
				"reasoning_effort": "high",
				"thinking":         map[string]any{"type": "enabled"},
			},
		},
		{
			name:  "xhigh becomes max",
			model: "glm-5.3",
			in:    `{"model":"glm-5.3","reasoning_effort":"xhigh"}`,
			want: map[string]any{
				"reasoning_effort": "max",
				"thinking":         map[string]any{"type": "enabled"},
			},
		},
		{
			// reasoning_effort is only read from GLM-5.2 onward. Sending it
			// to an older model asks for a field the API does not know.
			name:  "an older model drops reasoning_effort",
			model: "glm-4.6v",
			in:    `{"model":"glm-4.6v","reasoning_effort":"high"}`,
			want: map[string]any{
				"thinking": map[string]any{"type": "enabled"},
			},
		},
		{
			// A Claude-shaped client asks through thinking.type, and the
			// disabled value must still produce enable_thinking.
			name:  "claude shaped disabled becomes enable_thinking",
			model: "glm-4.6v",
			in:    `{"model":"glm-4.6v","thinking":{"type":"disabled"}}`,
			want: map[string]any{
				"enable_thinking": false,
			},
		},
		{
			// The nested Responses shape is consumed, not forwarded: leaving
			// `reasoning` next to the reasoning_effort z.ai is about to
			// receive would send it a body carrying both dialects.
			name:  "nested reasoning effort is consumed",
			model: "glm-5.3",
			in:    `{"model":"glm-5.3","reasoning":{"effort":"high"}}`,
			want: map[string]any{
				"reasoning":        nil,
				"reasoning_effort": "high",
				"thinking":         map[string]any{"type": "enabled"},
			},
		},
		{
			// z.ai bodies carry both an explicit output_config.effort and a
			// reasoning_effort. The explicit one is the more deliberate of
			// the two, so it is the one honoured — and the losing key is
			// stripped, not forwarded alongside.
			name:  "output_config.effort wins over reasoning_effort",
			model: "glm-5.3",
			in:    `{"model":"glm-5.3","output_config":{"effort":"low"},"reasoning_effort":"high"}`,
			want: map[string]any{
				"reasoning_effort": "low",
				"thinking":         map[string]any{"type": "enabled"},
			},
		},
		{
			// A model with no reasoning must not carry thinking fields at
			// all, whatever the client sent.
			name:  "a non reasoning model is stripped",
			model: "gpt-4o",
			in:    `{"model":"gpt-4o","reasoning_effort":"high"}`,
			want:  map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := applyZaiThinking([]byte(tt.in), tt.model)
			if err != nil {
				t.Fatalf("applyZaiThinking() error = %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("result is not a JSON object: %v", err)
			}
			assertThinkingFields(t, got, tt.want)
		})
	}
}

// TestApplyZaiThinking_LeavesUnrelatedBodiesAlone pins that the rewrite is a
// no-op for the common case — a client that never mentioned thinking — so
// ordinary traffic cannot be perturbed by this executor.
func TestApplyZaiThinking_LeavesUnrelatedBodiesAlone(t *testing.T) {
	tests := []struct {
		name  string
		model string
		in    string
	}{
		{
			name:  "no thinking field at all",
			model: "glm-5.3",
			in:    `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			// A body this gateway does not shape is the upstream's problem;
			// guessing here would turn a parse error into a mis-routed request.
			name:  "unparsable body is returned unchanged",
			model: "glm-5.3",
			in:    `not json at all`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := applyZaiThinking([]byte(tt.in), tt.model)
			if err != nil {
				t.Fatalf("applyZaiThinking() error = %v", err)
			}
			if string(out) != tt.in {
				t.Errorf("body = %s, want unchanged %s", out, tt.in)
			}
		})
	}
}

// assertThinkingFields compares only the fields this transform owns, so a
// failure names the thinking keys instead of dumping the whole body.
func assertThinkingFields(t *testing.T, got, want map[string]any) {
	t.Helper()
	for _, key := range []string{"thinking", "reasoning_effort", "enable_thinking", "reasoning"} {
		if !sameJSONValue(got[key], want[key]) {
			t.Errorf("%s = %#v, want %#v (full body: %#v)", key, got[key], want[key], got)
		}
	}
}

func sameJSONValue(a, b any) bool {
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ab) == string(bb)
}
