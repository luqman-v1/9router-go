package translator

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"testing"
)

func TestNormalizeToolSchemasForProvider(t *testing.T) {
	tools := []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "lookup",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"valid": map[string]any{
							"type":    "string",
							"pattern": "^[a-z]+$",
						},
						"invalid": map[string]any{
							"type":    "string",
							"pattern": "(?<invalid",
						},
					},
				},
			},
		},
	}

	// 1. Non-openrouter provider returns tools unchanged
	toolsAnthropic, removed := NormalizeToolSchemasForProvider("anthropic", tools)
	if removed != 0 || len(toolsAnthropic) != 1 {
		t.Fatalf("expected tools untouched for anthropic, got removed=%d", removed)
	}

	// 2. OpenRouter provider strips invalid pattern
	toolsOR, removedOR := NormalizeToolSchemasForProvider("openrouter", tools)
	if removedOR != 1 {
		t.Fatalf("expected 1 pattern removed, got %d", removedOR)
	}
	raw, _ := json.Marshal(toolsOR)
	if bytes.Contains(raw, []byte("(?<invalid")) {
		t.Errorf("expected invalid pattern to be stripped, got %s", string(raw))
	}
	if !bytes.Contains(raw, []byte("^[a-z]+$")) {
		t.Errorf("expected valid pattern to be kept, got %s", string(raw))
	}
}

func TestRequestedModelFromContext_AndSeedStreamState(t *testing.T) {
	// Context model
	ctx := context.Background()
	if got := RequestedModelFromContext(ctx); got != "" {
		t.Errorf("expected empty from plain context, got %q", got)
	}
	ctxWithModel := WithRequestedModel(ctx, "claude-3-5-sonnet")
	if got := RequestedModelFromContext(ctxWithModel); got != "claude-3-5-sonnet" {
		t.Errorf("expected claude-3-5-sonnet, got %q", got)
	}

	// SeedStreamState
	SeedStreamState("sess-1", "my-requested-model")
	// Re-seeding existing key
	SeedStreamState("sess-1", "other-model")
	// Empty key or model
	SeedStreamState("", "model")
	SeedStreamState("sess-2", "")
}

func TestAntigravityThoughtSignatures_StripAndReplace(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"hello","thoughtSignature":"sig-123","thought_signature":"sig-456"}]}]}`)

	// Strip
	stripped := StripThoughtSignatures(body)
	if bytes.Contains(stripped, []byte("thoughtSignature")) || bytes.Contains(stripped, []byte("thought_signature")) {
		t.Errorf("expected thought signatures stripped, got %s", string(stripped))
	}

	// Replace
	replaced := ReplaceThoughtSignatures(body, "valid-default-sig")
	if !bytes.Contains(replaced, []byte("valid-default-sig")) {
		t.Errorf("expected replacement in body, got %s", string(replaced))
	}
}

func TestUnwrapAntigravityResponse(t *testing.T) {
	wrapped := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}}`)
	unwrapped := UnwrapAntigravityResponse(wrapped)
	if !bytes.Contains(unwrapped, []byte(`"candidates"`)) {
		t.Errorf("expected unwrapped candidates, got %s", string(unwrapped))
	}

	// Invalid json returns raw
	invalid := []byte(`{invalid`)
	if got := UnwrapAntigravityResponse(invalid); !bytes.Equal(got, invalid) {
		t.Errorf("expected invalid to return raw, got %s", string(got))
	}
}

func TestOpenAIReasoningDetail_MarshalUnmarshal(t *testing.T) {
	// Case 1: Bare string
	raw1 := []byte(`"thinking about problem"`)
	var d1 OpenAIReasoningDetail
	if err := json.Unmarshal(raw1, &d1); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if d1.Text != "thinking about problem" {
		t.Errorf("expected text, got %q", d1.Text)
	}
	marshaled1, _ := json.Marshal(d1)
	if string(marshaled1) != `"thinking about problem"` {
		t.Errorf("marshal = %s", string(marshaled1))
	}

	// Case 2: Object
	raw2 := []byte(`{"text":"hello","content":"world"}`)
	var d2 OpenAIReasoningDetail
	if err := json.Unmarshal(raw2, &d2); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if d2.Text != "hello" || d2.Content != "world" {
		t.Errorf("expected hello/world, got %+v", d2)
	}
	marshaled2, _ := json.Marshal(d2)
	if !bytes.Contains(marshaled2, []byte(`"content":"world"`)) {
		t.Errorf("marshal object = %s", string(marshaled2))
	}
}

func TestUnwrapClineEnvelope(t *testing.T) {
	wrapped := []byte(`{"status":"success","data":{"id":"msg-1","choices":[{"message":{"content":"hi"}}]}}`)
	unwrapped := UnwrapClineEnvelope(wrapped)
	if !bytes.Contains(unwrapped, []byte(`"msg-1"`)) {
		t.Errorf("expected unwrapped data, got %s", string(unwrapped))
	}

	// Raw passthrough on invalid json
	invalid := []byte(`not-json`)
	if got := UnwrapClineEnvelope(invalid); !bytes.Equal(got, invalid) {
		t.Errorf("expected passthrough, got %s", string(got))
	}
}

func TestFormatAntigravityImageResponse(t *testing.T) {
	// With inlineData
	geminiResp := []byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"data":"base64encodedimage=="}}]}}]}`)
	out, err := FormatAntigravityImageResponse(geminiResp, "a cute cat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Contains(out, []byte("base64encodedimage==")) {
		t.Errorf("expected b64_json in output, got %s", string(out))
	}

	// Without inlineData fallback
	emptyResp := []byte(`{"candidates":[]}`)
	out2, err := FormatAntigravityImageResponse(emptyResp, "fallback prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Contains(out2, []byte("fallback prompt")) {
		t.Errorf("expected fallback prompt in output, got %s", string(out2))
	}
}

func TestSanitizeFetchArgs(t *testing.T) {
	orig := `{"input":{"uri":"https://example.com/api"}}`
	cleaned := sanitizeToolArgs("web_fetch", orig)
	if !bytes.Contains([]byte(cleaned), []byte(`"url":"https://example.com/api"`)) {
		t.Errorf("expected url synthesized from uri, got %s", cleaned)
	}
}

func TestMergeKiroConsecutiveUsers_AndUUID(t *testing.T) {
	u := newKiroUUID()
	if len(u) != 36 {
		t.Errorf("expected 36 chars uuid, got %q", u)
	}

	history := []map[string]any{
		{"userInputMessage": map[string]any{"content": "first", "userInputMessageContext": map[string]any{"tools": []any{"t1"}}}},
		{"userInputMessage": map[string]any{"content": "second", "userInputMessageContext": map[string]any{"tools": []any{"t2"}}}},
	}
	merged := mergeKiroConsecutiveUsers(history)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged user turn, got %d", len(merged))
	}
	content := merged[0]["userInputMessage"].(map[string]any)["content"].(string)
	if content != "first\n\nsecond" {
		t.Errorf("merged content = %q", content)
	}
}

func TestEffortToBudget_AndExtractThoughtSig(t *testing.T) {
	if got := effortToBudget("high"); got != 32000 {
		t.Errorf("effortToBudget(high) = %d, want 32000", got)
	}
	if got := effortToBudget("medium"); got != 16000 {
		t.Errorf("effortToBudget(medium) = %d, want 16000", got)
	}
	if got := effortToBudget("low"); got != 8000 {
		t.Errorf("effortToBudget(low) = %d, want 8000", got)
	}

	if sig := extractThoughtSig("tool_call_1__ts__valid_sig"); sig != "valid_sig" {
		t.Errorf("extractThoughtSig = %q, want valid_sig", sig)
	}
	if sig := extractThoughtSig("tool_call_plain"); sig != "" {
		t.Errorf("extractThoughtSig = %q, want empty", sig)
	}
}

func TestFixAntigravityContents(t *testing.T) {
	if fixAntigravityContents(nil) {
		t.Error("expected false for nil")
	}

	thoughtTrue := true
	req := &GeminiRequest{
		Contents: []GeminiContent{
			{
				Role: "model",
				Parts: []GeminiPart{
					{FunctionResponse: &GeminiFunctionResp{Name: "fn1"}},
				},
			},
			{
				Role: "model",
				Parts: []GeminiPart{
					{Thought: &thoughtTrue},
					{FunctionCall: &GeminiFunctionCall{Name: "call1"}},
				},
			},
		},
	}
	changed := fixAntigravityContents(req)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if req.Contents[0].Role != "user" {
		t.Errorf("expected role user, got %s", req.Contents[0].Role)
	}
	if len(req.Contents[1].Parts) != 1 {
		t.Errorf("expected thought stripped, got %d parts", len(req.Contents[1].Parts))
	}
	if req.Contents[1].Parts[0].ThoughtSignature != DefaultThinkingSignature {
		t.Errorf("expected signature backfilled, got %q", req.Contents[1].Parts[0].ThoughtSignature)
	}
}
