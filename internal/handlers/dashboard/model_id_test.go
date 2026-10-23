package dashboard

import "testing"

// The guard exists because a paste that was never split used to be stored as one id:
// "model1 model2:free model3-ext" became a single model whose id contained the spaces,
// and nothing could address it without reproducing them exactly.
func TestInvalidModelIDReason(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		valid bool
	}{
		{name: "plain id", id: "glm-5.3", valid: true},
		{name: "id with a version suffix", id: "claude-opus-4-5", valid: true},
		{name: "id with a colon", id: "muse-spark-1:free", valid: true},
		{name: "id with a colon and dot", id: "gpt-5.2:free", valid: true},
		{name: "id with a colon and slash-free", id: "model1:free", valid: true},
		{name: "id with a dot", id: "gpt-5.2-turbo", valid: true},
		{name: "empty", id: "", valid: false},
		{name: "unsplit space paste", id: "model1 model2:free model3-ext", valid: false},
		{name: "unsplit comma paste", id: "model1,model2:free,model3-ext", valid: false},
		{name: "unsplit semicolon paste", id: "model1;model2:free;model3-ext", valid: false},
		{name: "tab", id: "model1\tmodel2", valid: false},
		{name: "newline", id: "model1\nmodel2", valid: false},
		{name: "slash in namespaced model id", id: "deepseek/deepseek-v4-1-flash", valid: true},
		{name: "slash in multi-segment id", id: "nara/glm-5.3", valid: true},
		{name: "slash at start is invalid", id: "/glm-5.3", valid: false},
		{name: "slash at end is invalid", id: "glm-5.3/", valid: false},
		{name: "consecutive slashes is invalid", id: "glm//5.3", valid: false},
		{name: "single slash is invalid", id: "/", valid: false},
		{name: "pipe is the kv separator", id: "glm|5", valid: false},
		{name: "quote", id: `glm"5`, valid: false},
		{name: "angle bracket", id: "glm<5", valid: false},
		{name: "backtick", id: "glm`5", valid: false},
		{name: "too long", id: string(make([]byte, 0)) + repeatByte(201), valid: false},
		{name: "at the length limit", id: repeatByte(200), valid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := invalidModelIDReason(tt.id)
			if tt.valid && reason != "" {
				t.Fatalf("invalidModelIDReason(%q) = %q, want accepted", tt.id, reason)
			}
			if !tt.valid && reason == "" {
				t.Fatalf("invalidModelIDReason(%q) accepted it, want a reason", tt.id)
			}
		})
	}
}

func repeatByte(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}
