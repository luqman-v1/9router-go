package translator

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

// DedupeToolsDeepSeek rewrites the body only when duplicate tool names are
// actually dropped, so its non-determinism is intermittent — exactly the case
// that silently burns DeepSeek's prefix cache for users whose tool list has
// duplicates. The rewrite must serialize deterministically.
func TestDedupeToolsDeepSeekDeterministic(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],
		"tools":[
			{"type":"function","function":{"name":"read","description":"a","parameters":{"type":"object","properties":{"p":{"type":"string"}}}}},
			{"type":"function","function":{"name":"read","description":"duplicate","parameters":{"type":"object","properties":{"p":{"type":"string"}}}}},
			{"type":"function","function":{"name":"write","description":"b","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}
		]}`)

	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		out := DedupeToolsDeepSeek(in, "deepseek-v4-pro")
		if len(out) == len(in) {
			t.Fatal("expected the duplicate tool to be dropped and the body rewritten")
		}
		seen[fmt.Sprintf("%x", sha256.Sum256(out))]++
	}
	if len(seen) != 1 {
		t.Errorf("DedupeToolsDeepSeek produced %d distinct bodies over 200 identical requests, want 1", len(seen))
	}
}
