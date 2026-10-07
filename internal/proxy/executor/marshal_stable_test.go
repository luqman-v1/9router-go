package executor

import (
	"strings"
	"testing"

)

// Prefix stability is what DeepSeek's prompt cache keys on: the serialized
// request body must be byte-identical across turns of the same session for
// every byte the two requests share. encoding/json/v2 randomizes map member
// order by default, so every unmarshal->mutate->remarshal cycle reshuffles
// the entire body and destroys the shared prefix. Deterministic marshaling
// (RFC 8785-style sorted keys) makes the prefix stable.


func TestDeterministicMarshal_PrefixStableAcrossTurns(t *testing.T) {
	turn1 := map[string]any{
		"model": "deepseek-v4-pro",
		"messages": []any{
			map[string]any{"role": "user", "content": "sys"},
			map[string]any{"role": "assistant", "content": "a0"},
		},
	}
	turn2body := append([]any{}, turn1["messages"].([]any)...)
	turn2body = append(turn2body,
		map[string]any{"role": "user", "content": "u0"},
		map[string]any{"role": "assistant", "content": "a1"},
	)
	turn2 := map[string]any{"model": turn1["model"], "messages": turn2body}

	b1, err := marshalStable(turn1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := marshalStable(turn2)
	if err != nil {
		t.Fatal(err)
	}
	// With sorted keys the shared bytes are: {"messages":[ <shared items>,
	// then turn 2 appends more items before the closing of the array, and
	// "model" (sorted after "messages") sits at the tail. Trim turn 1 at the
	// end of its last shared message; turn 2 must start with those bytes.
	shared := `{"messages":[{"content":"sys","role":"user"},{"content":"a0","role":"assistant"}`
	if !strings.HasPrefix(string(b1), shared) || !strings.HasPrefix(string(b2), shared) {
		t.Errorf("shared prefix broken:\nturn1: %s\nturn2: %s", b1, b2)
	}
}

func TestDeterministicMarshal_StableOverManyCalls(t *testing.T) {
	m := map[string]any{"b": 1, "a": map[string]any{"z": []any{1, 2}, "y": "s"}, "c": true}
	first, err := marshalStable(m)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		out, _ := marshalStable(m)
		if string(out) != string(first) {
			t.Fatalf("output differed at call %d:\n%s\n%s", i, first, out)
		}
	}
}
