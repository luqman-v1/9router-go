package executor

import (
	"encoding/json/jsontext"
	"strings"
	"testing"
)

// determinismRuns mirrors the probe in #1167: 200 identical calls. json/v2
// does not order map members by default, so a seam that marshals a map
// without Deterministic(true) emits a different byte sequence within a couple
// of calls; two hundred makes the assertion reliable without slowing the
// suite.
const determinismRuns = 200

// TestDeepSeekBoundSchemaTextIsByteStable pins the deepSeek-web prompt seam:
// the same schema and nonce must always render to the same bytes, so the
// rendered prompt keeps its prefix-cache hits. Removing marshalStable from
// deepSeekBoundSchemaText fails this test.
func TestDeepSeekBoundSchemaTextIsByteStable(t *testing.T) {
	raw := jsontext.Value(`{"type":"object","properties":{"alpha":{"type":"string"},"bravo":{"type":"integer"},"charlie":{"type":"boolean"},"delta":{"type":"number"},"echo":{"type":"string"},"foxtrot":{"type":"array","items":{"type":"string"}},"golf":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"string"}}},"hotel":{"type":"string"}},"required":["alpha","bravo"]}`)
	first := deepSeekBoundSchemaText(raw, "nonce-123")
	if !strings.Contains(first, `"_nonce"`) || !strings.Contains(first, "nonce-123") {
		t.Fatalf("deepSeekBoundSchemaText did not bind the nonce: %s", first)
	}
	for i := 0; i < determinismRuns; i++ {
		if got := deepSeekBoundSchemaText(raw, "nonce-123"); got != first {
			t.Fatalf("call %d: deepSeekBoundSchemaText not byte-stable:\nfirst: %s\n got:  %s", i, first, got)
		}
	}
}

// TestDeepSeekWithoutNonceIsByteStable pins the arguments seam: stripping the
// _nonce binding and re-encoding must always produce the same bytes.
// Removing marshalStable from deepSeekWithoutNonce fails this test.
func TestDeepSeekWithoutNonceIsByteStable(t *testing.T) {
	args := map[string]any{
		"alpha": "a", "bravo": 2.0, "charlie": true, "delta": []any{"x", "y"},
		"echo":    map[string]any{"nested": "value", "other": 1.0},
		"foxtrot": "f", "golf": nil, "hotel": 3.5,
		"_nonce": "nonce-123",
	}
	first, ok := deepSeekWithoutNonce(args)
	if !ok {
		t.Fatal("deepSeekWithoutNonce rejected a valid arguments object")
	}
	if strings.Contains(first, "_nonce") {
		t.Fatalf("deepSeekWithoutNonce did not strip the nonce: %s", first)
	}
	if !strings.Contains(first, `"alpha"`) {
		t.Fatalf("deepSeekWithoutNonce dropped arguments: %s", first)
	}
	for i := 0; i < determinismRuns; i++ {
		got, ok := deepSeekWithoutNonce(args)
		if !ok {
			t.Fatalf("call %d: deepSeekWithoutNonce rejected a valid arguments object", i)
		}
		if got != first {
			t.Fatalf("call %d: deepSeekWithoutNonce not byte-stable:\nfirst: %s\n got:  %s", i, first, got)
		}
	}
}
