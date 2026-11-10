package translator

import (
	json "encoding/json/v2"
	"strings"
	"testing"
)

func decodeKiro(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal kiro envelope: %v", err)
	}
	return m
}

func TestOpenAIToKiro_BuildsConversationState(t *testing.T) {
	body := []byte(`{"model":"kr/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}],"max_tokens":1024,"stream":false}`)
	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "claude-sonnet-4.5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := decodeKiro(t, out)
	if _, ok := m["systemPrompt"]; ok {
		t.Error("top-level systemPrompt must not be present (gateway 400s on it)")
	}
	cs, ok := m["conversationState"].(map[string]any)
	if !ok {
		t.Fatalf("missing conversationState, got keys %v", m)
	}
	if cs["chatTriggerType"] != "MANUAL" {
		t.Errorf("chatTriggerType = %v, want MANUAL", cs["chatTriggerType"])
	}
	if _, ok := cs["conversationId"].(string); !ok {
		t.Error("missing conversationId")
	}
	cur, ok := cs["currentMessage"].(map[string]any)
	if !ok {
		t.Fatal("missing currentMessage")
	}
	uim, ok := cur["userInputMessage"].(map[string]any)
	if !ok {
		t.Fatal("missing currentMessage.userInputMessage")
	}
	if uim["modelId"] != "claude-sonnet-4.5" {
		t.Errorf("modelId = %v, want claude-sonnet-4.5", uim["modelId"])
	}
	if uim["origin"] != "AI_EDITOR" {
		t.Errorf("origin = %v, want AI_EDITOR", uim["origin"])
	}
	content, _ := uim["content"].(string)
	if !strings.Contains(content, "hi") {
		t.Errorf("current content should carry the user text, got %q", content)
	}
	// system/time prefix must be inside the user turn, not top-level.
	if !strings.Contains(content, "[Context: Current time is") {
		t.Errorf("expected time context inside user turn, got %q", content)
	}
	ic, ok := m["inferenceConfig"].(map[string]any)
	if !ok {
		t.Fatal("missing inferenceConfig")
	}
	mt, _ := ic["maxTokens"].(float64)
	if mt != kiroDefaultMaxTokens {
		t.Errorf("maxTokens = %v, want %d", ic["maxTokens"], kiroDefaultMaxTokens)
	}
}

func TestOpenAIToKiro_SystemBecomesInstructionsInUserTurn(t *testing.T) {
	body := []byte(`{"messages":[{"role":"system","content":"You are terse."},{"role":"user","content":"hello"}]}`)
	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "claude-sonnet-4.5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeKiro(t, out)
	cs := m["conversationState"].(map[string]any)
	uim := cs["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	content, _ := uim["content"].(string)
	if !strings.Contains(content, "<instructions>") || !strings.Contains(content, "You are terse.") {
		t.Errorf("system prompt must be wrapped in <instructions> inside the user turn, got %q", content)
	}
}

func TestOpenAIToKiro_MultiTurnHistoryAlternates(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":"first"},
		{"role":"assistant","content":"answer1"},
		{"role":"user","content":"second"}
	]}`)
	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeKiro(t, out)
	cs := m["conversationState"].(map[string]any)
	hist, _ := cs["history"].([]any)
	if len(hist) == 0 {
		t.Fatal("expected history turns")
	}
	// The trailing user turn is the current message; history holds earlier turns.
	cur := cs["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	if c, _ := cur["content"].(string); !strings.Contains(c, "second") {
		t.Errorf("current turn should be the last user message, got %q", c)
	}
	// No two adjacent user turns (Kiro requires alternation).
	prevUser := false
	for _, h := range hist {
		turn := h.(map[string]any)
		_, isUser := turn["userInputMessage"]
		if isUser && prevUser {
			t.Error("history has consecutive user turns")
		}
		prevUser = isUser
		if isUser {
			uim := turn["userInputMessage"].(map[string]any)
			if uim["modelId"] != "m" {
				t.Errorf("history user turn modelId = %v, want m", uim["modelId"])
			}
		}
	}
}

func TestOpenAIToKiro_ToolCallsAndResults(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"weather?"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"NYC\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"sunny"}
	]}`)
	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeKiro(t, out)
	cs := m["conversationState"].(map[string]any)
	hist, _ := cs["history"].([]any)
	found := false
	for _, h := range hist {
		arm, ok := h.(map[string]any)["assistantResponseMessage"].(map[string]any)
		if !ok {
			continue
		}
		uses, _ := arm["toolUses"].([]any)
		if len(uses) == 0 {
			continue
		}
		found = true
		u := uses[0].(map[string]any)
		if u["name"] != "get_weather" || u["toolUseId"] != "call_1" {
			t.Errorf("unexpected toolUse: %v", u)
		}
		input := u["input"].(map[string]any)
		if input["city"] != "NYC" {
			t.Errorf("tool input not parsed from arguments JSON: %v", input)
		}
	}
	if !found {
		t.Error("expected an assistant turn carrying toolUses in history")
	}
	// Current (tool result) user turn carries userInputMessageContext.toolResults.
	cur := cs["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	ctx, _ := cur["userInputMessageContext"].(map[string]any)
	results, _ := ctx["toolResults"].([]any)
	if len(results) != 1 {
		t.Fatalf("expected 1 tool result in current turn, got %v", ctx)
	}
	if results[0].(map[string]any)["toolUseId"] != "call_1" {
		t.Errorf("unexpected tool result: %v", results[0])
	}
}

func TestOpenAIToKiro_ImageBecomesKiroBlock(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"what is this?"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}
	]}]}`)
	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeKiro(t, out)
	cs := m["conversationState"].(map[string]any)
	uim := cs["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	imgs, _ := uim["images"].([]any)
	if len(imgs) != 1 {
		t.Fatalf("expected 1 image, got %v", uim["images"])
	}
	img := imgs[0].(map[string]any)
	if img["format"] != "png" {
		t.Errorf("format = %v, want png", img["format"])
	}
	src := img["source"].(map[string]any)
	if src["bytes"] != "AAAA" {
		t.Errorf("source.bytes = %v, want AAAA", src["bytes"])
	}
}

func TestOpenAIToKiro_ProfileArnOnlyWhenProvided(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "m", ProfileArn: "arn:aws:codewhisperer:us-east-1:1:profile/p"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decodeKiro(t, out)["profileArn"] != "arn:aws:codewhisperer:us-east-1:1:profile/p" {
		t.Error("profileArn should be forwarded when provided")
	}

	out2, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := decodeKiro(t, out2)["profileArn"]; ok {
		t.Error("profileArn must be omitted when the caller passes none; the default is resolved by the executor")
	}
}

func TestOpenAIToKiro_EmptyUserTurnGetsPlaceholder(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":""}]}`)
	out, err := OpenAIToKiro(body, KiroTranslateOptions{Model: "m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m := decodeKiro(t, out)
	cs := m["conversationState"].(map[string]any)
	uim := cs["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
	c, _ := uim["content"].(string)
	if !strings.Contains(c, kiroEmptyUserPlaceholder) {
		t.Errorf("empty user turn should get %q placeholder, got %q", kiroEmptyUserPlaceholder, c)
	}
}
