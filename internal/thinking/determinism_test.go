package thinking

import "testing"

func TestApplyJSONDeterministic(t *testing.T) {
	input := []byte(`{"model":"m","reasoning_effort":"high","messages":[]}`)
	seen := map[string]struct{}{}
	for i := 0; i < 200; i++ {
		out := ApplyJSON(input, "openai", "openai", "gpt-5", nil)
		seen[string(out)] = struct{}{}
	}
	if len(seen) != 1 {
		t.Fatalf("ApplyJSON nondeterministic: %d distinct outputs", len(seen))
	}
}
