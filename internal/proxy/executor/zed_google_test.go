package executor

import (
	"encoding/json"
	"testing"

	"9router/proxy/internal/providers"
)

func TestZedWireProviderGoogle(t *testing.T) {
	// Zed's catalogue names the Gemini family "google".
	if got := providers.ZedWireProvider("gemini-3.8-flash"); got != "google" {
		t.Errorf("ZedWireProvider(gemini-3.8-flash) = %q, want google", got)
	}
}

func TestZedGoogleProviderRequest(t *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash","stream":true,"max_tokens":32,
	 "messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`)

	providerRequest, err := zedGoogleProviderRequest(body, "gemini-3.8-flash")
	if err != nil {
		t.Fatalf("zedGoogleProviderRequest: %v", err)
	}
	if providerRequest["model"] != "gemini-3.8-flash" {
		t.Errorf("model = %v", providerRequest["model"])
	}
	// Zed's google provider speaks the Gemini dialect.
	contents, ok := providerRequest["contents"].([]any)
	if !ok || len(contents) == 0 {
		t.Fatalf("contents = %v, want a non-empty array", providerRequest["contents"])
	}
	first, ok := contents[0].(map[string]any)
	if !ok {
		t.Fatalf("contents[0] = %T, want an object", contents[0])
	}
	parts, ok := first["parts"].([]any)
	if !ok || len(parts) == 0 {
		t.Fatalf("parts = %v, want a non-empty array", first["parts"])
	}
	part, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("parts[0] = %T, want an object", parts[0])
	}
	if _, hasText := part["text"]; !hasText {
		t.Errorf("part has no text: %v", part)
	}
	// The generation settings live under generationConfig, camelCase.
	config, ok := providerRequest["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig = %v, want an object", providerRequest["generationConfig"])
	}
	if _, ok := config["maxOutputTokens"]; !ok {
		t.Errorf("generationConfig has no maxOutputTokens: %v", config)
	}
}

func TestZedGoogleProviderRequestRejectsGarbage(t *testing.T) {
	if _, err := zedGoogleProviderRequest([]byte("not json"), "gemini-3.8-flash"); err == nil {
		t.Fatal("expected an error for a malformed body")
	}
}

func TestZedAggregateGeminiSSE(t *testing.T) {
	// A thinking model emits the answer first and then a terminal event whose
	// part carries no text. The aggregation must keep the answer.
	stream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"PONG\"}],\"role\":\"model\"}}]}\n\n" +
		"data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[]}}],\"usageMetadata\":{\"promptTokenCount\":9,\"candidatesTokenCount\":4}}\n\n"

	got := zedAggregateGeminiSSE([]byte(stream))
	if len(got) == 0 {
		t.Fatal("expected an aggregated document")
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := doc["usageMetadata"]; !ok {
		t.Errorf("usage metadata was dropped: %s", got)
	}
	candidates, _ := doc["candidates"].([]any)
	if len(candidates) == 0 {
		t.Fatalf("no candidates: %s", got)
	}
	first, _ := candidates[0].(map[string]any)
	content, _ := first["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) == 0 {
		t.Fatalf("no parts: %s", got)
	}
	part, _ := parts[0].(map[string]any)
	if part["text"] != "PONG" {
		t.Errorf("text = %v, want PONG", part["text"])
	}
}

func TestZedAggregateGeminiSSEConcatenatesParts(t *testing.T) {
	stream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"PO\"}],\"role\":\"model\"}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"NG\"}],\"role\":\"model\"}}]}\n\n"

	got := zedAggregateGeminiSSE([]byte(stream))
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	candidates, _ := doc["candidates"].([]any)
	first, _ := candidates[0].(map[string]any)
	content, _ := first["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	if part["text"] != "PONG" {
		t.Errorf("text = %v, want PONG", part["text"])
	}
}

func TestZedAggregateGeminiSSESkipsThoughtParts(t *testing.T) {
	// A thinking part is not answer text and must not reach the client.
	stream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"thinking...\",\"thought\":true},{\"text\":\"PONG\"}],\"role\":\"model\"}}]}\n\n"

	got := zedAggregateGeminiSSE([]byte(stream))
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	candidates, _ := doc["candidates"].([]any)
	first, _ := candidates[0].(map[string]any)
	content, _ := first["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	part, _ := parts[0].(map[string]any)
	if part["text"] != "PONG" {
		t.Errorf("text = %v, want PONG", part["text"])
	}
}

func TestZedAggregateGeminiSSENoEvents(t *testing.T) {
	if got := zedAggregateGeminiSSE([]byte("data: [DONE]\n\n")); len(got) != 0 {
		t.Errorf("expected no document, got %s", got)
	}
}
