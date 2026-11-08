package headroom

import (
	"context"
	json "encoding/json/v2"
	"strings"
	"testing"
)

// buildToolBody assembles a request body carrying one compressible tool result.
// The body is marshaled rather than concatenated: filler contains real newlines,
// and a literal newline inside a JSON string is invalid, so a hand-written body
// would fail to unmarshal and fail open before any compression decision — which
// would make these tests pass for the wrong reason.
func buildToolBody(t *testing.T, name, filler string) []byte {
	t.Helper()
	body := map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are helpful."},
			map[string]any{"role": "user", "content": "run the build"},
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
				map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": name, "arguments": "{}"}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": strings.Repeat(filler, 200)},
		},
	}
	out, err := json.Marshal(body, json.Deterministic(true))
	if err != nil {
		t.Fatalf("buildToolBody: %v", err)
	}
	return out
}

// buildNativeBody assembles a Responses-shaped body whose compressible text
// lives outside the messages array, in function_call_output outputs.
func buildNativeBody(t *testing.T) []byte {
	t.Helper()
	body := map[string]any{
		"model": "gpt-5",
		"input": []any{
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": strings.Repeat("native tool output line\n", 200)},
		},
	}
	out, err := json.Marshal(body, json.Deterministic(true))
	if err != nil {
		t.Fatalf("buildNativeBody: %v", err)
	}
	return out
}

func TestNormalizeEngine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty resolves to built-in", "", EngineBuiltin},
		{"builtin stays built-in", EngineBuiltin, EngineBuiltin},
		{"external stays external", EngineExternal, EngineExternal},
		{"unknown resolves to built-in", "nonsense", EngineBuiltin},
		{"case sensitive typo resolves to built-in", "Builtin", EngineBuiltin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeEngine(tt.in); got != tt.want {
				t.Errorf("NormalizeEngine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestBuiltinEngineCommitsThroughClient is the end-to-end unit proof: the
// built-in engine, reached through the public Compress path with no external
// proxy configured, folds a tool result and commits a smaller body.
func TestBuiltinEngineCommitsThroughClient(t *testing.T) {
	c := NewClient("", "", nil)
	if !c.useBuiltin {
		t.Fatal("NewClient must select the built-in engine")
	}
	body := buildToolBody(t, "shell", "2024-01-01T00:00:00Z INFO build step completed ok\n")
	out, did, err := c.Compress(context.Background(), body, "gpt-4o", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !did {
		t.Fatalf("expected the built-in engine to compress a foldable tool result")
	}
	if len(out) >= len(body) {
		t.Fatalf("body did not shrink: %d >= %d", len(out), len(body))
	}
	// The structural fields the provider depends on must survive.
	for _, want := range []string{"call_1", "gpt-4o", "You are helpful."} {
		if !strings.Contains(string(out), want) {
			t.Errorf("compressed body lost %q: %s", want, out)
		}
	}
}

// TestBuiltinEngineIsSelectedWithoutProxy pins the default, because a fresh
// install has no Python proxy and must still compress.
func TestBuiltinEngineIsSelectedWithoutProxy(t *testing.T) {
	for _, url := range []string{"", DefaultURL} {
		if !NewClient(url, "", nil).useBuiltin {
			t.Errorf("NewClient(%q) must select the built-in engine", url)
		}
	}
	if !NewClientWithEngine("http://localhost:9999", "", nil, "").useBuiltin {
		t.Error("an empty engine must normalize to the built-in engine")
	}
	if !NewClientWithEngine("http://localhost:9999", "", nil, "nonsense").useBuiltin {
		t.Error("an unrecognized engine must normalize to the built-in engine")
	}
}

// TestExternalEngineSelectsProxy pins the opt-in: only EngineExternal reaches
// the network path, and an external engine with no URL to reach falls back to
// the built-in engine rather than failing every request.
func TestExternalEngineSelectsProxy(t *testing.T) {
	if NewClientWithEngine("http://localhost:9999", "", nil, EngineExternal).useBuiltin {
		t.Error("EngineExternal must select the external proxy")
	}
	if !NewClientWithEngine("", "", nil, EngineExternal).useBuiltin {
		t.Error("external engine with no URL must fall back to the built-in engine")
	}
}

// TestBuiltinEngineRefusesProtectedTool verifies a tool the compressor was never
// cleared for is forwarded byte for byte.
func TestBuiltinEngineRefusesProtectedTool(t *testing.T) {
	body := buildToolBody(t, "read", "func Foo() { println(\\\"source line padding\\\") }\n")
	out, did, err := NewClient("", "", nil).Compress(context.Background(), body, "gpt-4o", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if did {
		t.Fatal("a protected tool's output must not be compressed")
	}
	if string(out) != string(body) {
		t.Fatal("the body must be forwarded unchanged on refusal")
	}
}

// TestBuiltinEngineNativePath verifies the Responses-shaped path compresses too.
// Without the built-in branch there, a body whose tool output lives outside the
// messages array would silently skip compression whenever the built-in engine is
// active — which is the default.
func TestBuiltinEngineNativePath(t *testing.T) {
	body := buildNativeBody(t)
	out, did, err := NewClient("", "", nil).Compress(context.Background(), body, "gpt-5", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !did {
		t.Fatal("native tool output must be compressed by the built-in engine")
	}
	if len(out) >= len(body) {
		t.Fatalf("body did not shrink: %d >= %d", len(out), len(body))
	}
	// The call id is how the provider joins the output to its call.
	if !strings.Contains(string(out), "c1") {
		t.Errorf("compressed body lost the call id: %s", out)
	}
}

// TestBuiltinEngineHonorsCancellation verifies a canceled downstream context
// stops dispatch instead of spending the request's CPU.
func TestBuiltinEngineHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := NewClient("", "", nil).Compress(ctx, buildToolBody(t, "shell", "padding line\n"), "gpt-4o", 0)
	if err == nil {
		t.Fatal("a canceled context must return an error, not compress")
	}
}

// TestBuiltinEngineReportsRealSavings pins the accounting contract: the engine
// must never report a saving it did not deliver, because that number is what an
// operator reads when deciding whether to keep compression on.
func TestBuiltinEngineReportsRealSavings(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "You are helpful."},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": strings.Repeat("log line here\n", 200)},
	}
	res, ok := NewBuiltinEngine().CompressMessages(msgs)
	if !ok {
		t.Fatal("expected the engine to compress a foldable tool result")
	}
	if res.TokensSaved != res.TokensBefore-res.TokensAfter {
		t.Errorf("TokensSaved %d != TokensBefore-TokensAfter %d", res.TokensSaved, res.TokensBefore-res.TokensAfter)
	}
	if res.TokensSaved <= 0 {
		t.Error("a committed compression must report positive savings")
	}
	if res.CompressionRatio >= 1.0 {
		t.Errorf("compression ratio %.3f must be below 1 for a saving", res.CompressionRatio)
	}
	if len(res.TransformsSummary) == 0 {
		t.Error("transforms_summary must describe what the engine did")
	}
	// CCR is a capability the in-process engine does not have, so it must say so
	// rather than emitting a hash the gateway cannot later retrieve.
	if len(res.CcrHashes) != 0 {
		t.Errorf("the built-in engine must not emit CCR hashes, got %v", res.CcrHashes)
	}
}

// TestBuiltinEngineRefusesNoGain verifies a result that is not smaller is
// discarded, so a compressor that reports savings it did not deliver cannot
// commit them.
func TestBuiltinEngineRefusesNoGain(t *testing.T) {
	// Two short messages: nothing is eligible, so the engine must decline.
	msgs := []any{
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "assistant", "content": "hello"},
	}
	if res, ok := NewBuiltinEngine().CompressMessages(msgs); ok {
		t.Errorf("an uncompressible conversation must not report a saving: %+v", res)
	}
}

// TestBuiltinEnginePreservesStructure verifies the engine's output survives the
// same structural gate the external proxy's does. The gate is what makes "the
// structure survived" a property of the response rather than of the producer.
func TestBuiltinEnginePreservesStructure(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
			map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "shell", "arguments": `{"command":"go build ./..."}`}},
		}},
		map[string]any{"role": "tool", "tool_call_id": "call_1", "content": strings.Repeat("log line here\n", 200)},
	}
	res, ok := NewBuiltinEngine().CompressMessages(msgs)
	if !ok {
		t.Fatal("expected the engine to compress")
	}
	if !validateMessagesPreserved(msgs, res.Messages) {
		t.Error("the engine's output must preserve the structural invariants")
	}
	if len(res.Messages) != len(msgs) {
		t.Errorf("message count changed: %d != %d", len(res.Messages), len(msgs))
	}
}

// TestBuiltinEngineCompressText verifies the single-leaf entry point declines a
// leaf it cannot shrink, rather than returning a rewrite.
func TestBuiltinEngineCompressText(t *testing.T) {
	got, ok := NewBuiltinEngine().CompressText("short text")
	if ok || got != "short text" {
		t.Errorf("short text must be returned unchanged, got %q ok=%v", got, ok)
	}
}

// TestBuiltinEngineOutputIsStable verifies two runs over the same input produce
// the same messages.
//
// The comparison decodes both sides rather than comparing bytes: the engine
// re-marshals a map, and encoding/json/v2 orders map keys arbitrarily, so two
// byte-identical inputs can come back with the keys in a different order. That
// is a property of the encoder, not a defect in the compression — the provider
// parses JSON, it does not diff text.
func TestBuiltinEngineOutputIsStable(t *testing.T) {
	mk := func() []any {
		return []any{
			map[string]any{"role": "tool", "tool_call_id": "c1", "content": strings.Repeat("log line here\n", 200)},
		}
	}
	first, ok1 := NewBuiltinEngine().CompressMessages(mk())
	second, ok2 := NewBuiltinEngine().CompressMessages(mk())
	if !ok1 || !ok2 {
		t.Fatal("both runs must compress")
	}
	if len(first.Messages) != len(second.Messages) {
		t.Fatalf("message count differs between runs: %d != %d", len(first.Messages), len(second.Messages))
	}
	for i := range first.Messages {
		a, errA := json.Marshal(first.Messages[i])
		b, errB := json.Marshal(second.Messages[i])
		if errA != nil || errB != nil {
			t.Fatalf("marshal: %v %v", errA, errB)
		}
		var ao, bo any
		if err := json.Unmarshal(a, &ao); err != nil {
			t.Fatalf("unmarshal a: %v", err)
		}
		if err := json.Unmarshal(b, &bo); err != nil {
			t.Fatalf("unmarshal b: %v", err)
		}
		if !jsonEqual(ao, bo) {
			t.Errorf("message %d differs between runs:\n%s\n%s", i, a, b)
		}
	}
}

// jsonEqual compares two decoded JSON values by shape, ignoring map key order.
func jsonEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, va := range av {
			wb, ok := bv[k]
			if !ok || !jsonEqual(va, wb) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
