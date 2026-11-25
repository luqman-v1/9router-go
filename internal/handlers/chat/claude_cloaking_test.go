package chat

import (
	json "encoding/json/v2"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy/executor"
)

func TestIsAnthropicUpstream(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		cfg      *providers.ProviderConfig
		want     bool
	}{
		{
			name:     "direct claude messages",
			provider: "claude",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.anthropic.com/v1/messages"},
			want:     true,
		},
		{
			name:     "direct anthropic messages with query",
			provider: "anthropic",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.anthropic.com/v1/messages?beta=true"},
			want:     true,
		},
		{
			name:     "edge relay forward to anthropic",
			provider: "claude",
			cfg: &providers.ProviderConfig{
				BaseURL: "https://my-relay.vercel.app",
				StaticHeaders: map[string]string{
					"x-relay-target": "https://api.anthropic.com",
					"x-relay-path":   "/v1/messages",
				},
			},
			want: true,
		},
		{
			name:     "edge relay forward with existing query",
			provider: "anthropic",
			cfg: &providers.ProviderConfig{
				BaseURL: "https://my-relay.vercel.app",
				StaticHeaders: map[string]string{
					"x-relay-target": "https://api.anthropic.com",
					"x-relay-path":   "/v1/messages?beta=true",
				},
			},
			want: true,
		},
		{
			name:     "non-anthropic provider",
			provider: "openai",
			cfg:      &providers.ProviderConfig{BaseURL: "https://api.anthropic.com/v1/messages"},
			want:     false,
		},
		{
			name:     "anthropic custom endpoint",
			provider: "claude",
			cfg:      &providers.ProviderConfig{BaseURL: "https://custom.host/v1/messages"},
			want:     false,
		},
		{
			name:     "nil config",
			provider: "claude",
			cfg:      nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAnthropicUpstream(tt.provider, tt.cfg); got != tt.want {
				t.Fatalf("isAnthropicUpstream(%s, %+v) = %v, want %v", tt.provider, tt.cfg, got, tt.want)
			}
		})
	}
}

func TestAppendBetaQuery(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "https://api.anthropic.com/v1/messages",
			want:  "https://api.anthropic.com/v1/messages?beta=true",
		},
		{
			input: "/v1/messages",
			want:  "/v1/messages?beta=true",
		},
		{
			input: "https://api.anthropic.com/v1/messages?existing=1",
			want:  "https://api.anthropic.com/v1/messages?existing=1&beta=true",
		},
		{
			input: "/v1/messages?beta=true",
			want:  "/v1/messages?beta=true",
		},
		{
			input: "https://api.anthropic.com/v1/messages?foo=bar&beta=true",
			want:  "https://api.anthropic.com/v1/messages?foo=bar&beta=true",
		},
	}

	for _, tt := range tests {
		if got := appendBetaQuery(tt.input); got != tt.want {
			t.Errorf("appendBetaQuery(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestApplyClaudeCloaking(t *testing.T) {
	body := []byte(`{"model":"claude-3-opus-20240229","messages":[{"role":"user","content":"hello"}]}`)
	out := applyClaudeCloaking(body, "oauth-token-123", "sess-1")
	if string(out) == string(body) {
		t.Fatalf("expected cloaking to modify body")
	}
	s := string(out)
	if !strings.Contains(s, "x-anthropic-billing-header:") || !strings.Contains(s, "metadata") {
		t.Fatalf("missing cloaked headers in cloaked request: %s", s)
	}
}

func TestApplyClaudeCloaking_StableAcrossTurnsForPromptCaching(t *testing.T) {
	turn1Body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"turn 1"}]}`)
	turn2Body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"turn 1"},{"role":"assistant","content":"reply 1"},{"role":"user","content":"turn 2"}]}`)

	out1 := applyClaudeCloaking(turn1Body, "sk-ant-oat-token", "sess-fixed-123")
	out2 := applyClaudeCloaking(turn2Body, "sk-ant-oat-token", "sess-fixed-123")

	var m1, m2 map[string]any
	_ = json.Unmarshal(out1, &m1)
	_ = json.Unmarshal(out2, &m2)

	sys1 := m1["system"].([]any)[0].(map[string]any)["text"].(string)
	sys2 := m2["system"].([]any)[0].(map[string]any)["text"].(string)

	if sys1 != sys2 {
		t.Fatalf("billing header mutated across turns, breaking prompt cache:\nTurn 1: %s\nTurn 2: %s", sys1, sys2)
	}
}

func TestStandardKey_SkipsToolCloakingAndDecoys(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-opus-20240229",
		"messages": [{"role": "user", "content": "run calc"}],
		"tools": [{"name": "calculator", "description": "calc"}]
	}`)
	apiKey := "sk-ant-api03-regular-key"
	isOAuth := false

	pipedBody := body
	var claudeToolMap map[string]string
	if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
		pipedBody = applyClaudeCloaking(pipedBody, apiKey, "sess-1")
		var reqMap map[string]any
		if err := json.Unmarshal(pipedBody, &reqMap); err == nil {
			claudeToolMap = cloakClaudeTools(reqMap)
			if out, err := json.Marshal(reqMap); err == nil {
				pipedBody = out
			}
		}
	}

	if claudeToolMap != nil {
		t.Fatalf("expected nil claudeToolMap for standard key, got %v", claudeToolMap)
	}
	if string(pipedBody) != string(body) {
		t.Fatalf("expected body unchanged for standard key, got %s", string(pipedBody))
	}
}

func TestOAuthKey_AppliesToolCloakingAndDecoys(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-opus-20240229",
		"messages": [{"role": "user", "content": "run calc"}],
		"tools": [{"name": "calculator", "description": "calc"}]
	}`)
	apiKey := "sk-ant-oat-token"
	isOAuth := true

	pipedBody := body
	var claudeToolMap map[string]string
	if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
		pipedBody = applyClaudeCloaking(pipedBody, apiKey, "sess-1")
		var reqMap map[string]any
		if err := json.Unmarshal(pipedBody, &reqMap); err == nil {
			claudeToolMap = cloakClaudeTools(reqMap)
			if out, err := json.Marshal(reqMap); err == nil {
				pipedBody = out
			}
		}
	}

	if claudeToolMap == nil || claudeToolMap["calculator_ide"] != "calculator" {
		t.Fatalf("expected calculator_ide mapped to calculator, got %v", claudeToolMap)
	}
	s := string(pipedBody)
	if !strings.Contains(s, "calculator_ide") {
		t.Fatalf("tools should be suffixed with _ide: %s", s)
	}
	if !strings.Contains(s, `"name":"Task"`) {
		t.Fatalf("decoy tools should be injected for OAuth: %s", s)
	}
}

func TestCloakClaudeTools_DoesNotDoubleSuffix(t *testing.T) {
	body := []byte(`{
		"model": "claude-3-opus-20240229",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "tu_1",
						"name": "todo_write_ide",
						"input": {}
					}
				]
			}
		],
		"tools": [{"name": "todo_write_ide", "description": "todo write"}]
	}`)
	apiKey := "sk-ant-oat-token"
	isOAuth := true

	pipedBody := body
	var claudeToolMap map[string]string
	if isOAuth || strings.Contains(apiKey, "sk-ant-oat") {
		pipedBody = applyClaudeCloaking(pipedBody, apiKey, "sess-1")
		var reqMap map[string]any
		if err := json.Unmarshal(pipedBody, &reqMap); err == nil {
			claudeToolMap = cloakClaudeTools(reqMap)
			if out, err := json.Marshal(reqMap); err == nil {
				pipedBody = out
			}
		}
	}

	if claudeToolMap == nil || claudeToolMap["todo_write_ide"] != "todo_write" {
		t.Fatalf("expected todo_write_ide mapped to todo_write, got %v", claudeToolMap)
	}
	s := string(pipedBody)
	if strings.Contains(s, "todo_write_ide_ide") {
		t.Fatalf("found double-suffixed tool name in request: %s", s)
	}
	if !strings.Contains(s, `"name":"todo_write_ide"`) {
		t.Fatalf("expected tool_use name to remain todo_write_ide, got %s", s)
	}
}

func TestCloakClaudeTools_ToolReferenceBlocks(t *testing.T) {
	tests := []struct {
		name      string
		reqJSON   string
		wantCheck func(t *testing.T, req map[string]any, toolMap map[string]string)
	}{
		{
			name: "renames tool_reference in tool_result content for cloaked tool",
			reqJSON: `{
				"tools": [
					{"name": "ToolSearch", "input_schema": {"type": "object", "properties": {}}},
					{"name": "Monitor", "defer_loading": true, "input_schema": {"type": "object", "properties": {}}}
				],
				"messages": [
					{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "ToolSearch", "input": {}}]},
					{"role": "user", "content": [
						{"type": "tool_result", "tool_use_id": "t1", "content": [
							{"type": "tool_reference", "tool_name": "Monitor"}
						]}
					]}
				]
			}`,
			wantCheck: func(t *testing.T, req map[string]any, toolMap map[string]string) {
				if toolMap == nil || toolMap["Monitor_ide"] != "Monitor" {
					t.Fatalf("expected Monitor_ide in toolMap, got %v", toolMap)
				}
				msgs := req["messages"].([]any)
				userMsg := msgs[1].(map[string]any)
				userContent := userMsg["content"].([]any)
				toolResult := userContent[0].(map[string]any)
				trContent := toolResult["content"].([]any)
				ref := trContent[0].(map[string]any)
				if got := ref["tool_name"]; got != "Monitor_ide" {
					t.Errorf("tool_name = %v, want Monitor_ide", got)
				}
			},
		},
		{
			name: "leaves unrenamed or decoy tool references untouched",
			reqJSON: `{
				"tools": [
					{"name": "ToolSearch", "input_schema": {"type": "object", "properties": {}}}
				],
				"messages": [
					{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "ToolSearch", "input": {}}]},
					{"role": "user", "content": [
						{"type": "tool_result", "tool_use_id": "t1", "content": [
							{"type": "tool_reference", "tool_name": "Bash"}
						]}
					]}
				]
			}`,
			wantCheck: func(t *testing.T, req map[string]any, toolMap map[string]string) {
				msgs := req["messages"].([]any)
				userMsg := msgs[1].(map[string]any)
				userContent := userMsg["content"].([]any)
				toolResult := userContent[0].(map[string]any)
				trContent := toolResult["content"].([]any)
				ref := trContent[0].(map[string]any)
				if got := ref["tool_name"]; got != "Bash" {
					t.Errorf("tool_name = %v, want Bash", got)
				}
			},
		},
		{
			name: "handles mixed content in tool_result.content and string content cleanly",
			reqJSON: `{
				"tools": [
					{"name": "ToolSearch", "input_schema": {"type": "object", "properties": {}}},
					{"name": "Monitor", "defer_loading": true, "input_schema": {"type": "object", "properties": {}}}
				],
				"messages": [
					{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "ToolSearch", "input": {}}]},
					{"role": "user", "content": [
						{"type": "tool_result", "tool_use_id": "t1", "content": [
							{"type": "text", "text": "Found 1 tool:"},
							{"type": "tool_reference", "tool_name": "Monitor"}
						]},
						{"type": "tool_result", "tool_use_id": "t2", "content": "plain string result"}
					]}
				]
			}`,
			wantCheck: func(t *testing.T, req map[string]any, toolMap map[string]string) {
				msgs := req["messages"].([]any)
				userMsg := msgs[1].(map[string]any)
				userContent := userMsg["content"].([]any)

				tr1 := userContent[0].(map[string]any)
				tr1Content := tr1["content"].([]any)
				textBlock := tr1Content[0].(map[string]any)
				if textBlock["text"] != "Found 1 tool:" {
					t.Errorf("textBlock text = %v, want 'Found 1 tool:'", textBlock["text"])
				}
				refBlock := tr1Content[1].(map[string]any)
				if refBlock["tool_name"] != "Monitor_ide" {
					t.Errorf("refBlock tool_name = %v, want Monitor_ide", refBlock["tool_name"])
				}

				tr2 := userContent[1].(map[string]any)
				if tr2["content"] != "plain string result" {
					t.Errorf("tr2 content = %v, want 'plain string result'", tr2["content"])
				}
			},
		},
		{
			name: "renames top-level tool_reference in message content and handles name field",
			reqJSON: `{
				"tools": [
					{"name": "Monitor", "input_schema": {"type": "object", "properties": {}}}
				],
				"messages": [
					{"role": "user", "content": [
						{"type": "tool_reference", "name": "Monitor"}
					]}
				]
			}`,
			wantCheck: func(t *testing.T, req map[string]any, toolMap map[string]string) {
				msgs := req["messages"].([]any)
				userMsg := msgs[0].(map[string]any)
				userContent := userMsg["content"].([]any)
				ref := userContent[0].(map[string]any)
				if got := ref["name"]; got != "Monitor_ide" {
					t.Errorf("name = %v, want Monitor_ide", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req map[string]any
			if err := json.Unmarshal([]byte(tt.reqJSON), &req); err != nil {
				t.Fatalf("failed to unmarshal test JSON: %v", err)
			}
			toolMap := cloakClaudeTools(req)
			tt.wantCheck(t, req, toolMap)
		})
	}
}

func TestAdoptRequiredClaudeCodeVersion(t *testing.T) {
	oldVer := ClaudeCLIVersion()
	defer func() {
		claudeVersionMu.Lock()
		adoptedClaudeVersion = ""
		claudeVersionMu.Unlock()
	}()

	// Non-matching error body does nothing
	if AdoptRequiredClaudeCodeVersion([]byte("rate_limit_error")) {
		t.Errorf("unexpected adopt on rate_limit_error")
	}

	// 400 rejection with claude_code_version_too_old adopts the newer version
	rejection := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"claude_code_version_too_old: version 2.1.350 or newer is required to access this model"}}`)
	if !AdoptRequiredClaudeCodeVersion(rejection) {
		t.Fatalf("expected version adopt")
	}

	if got := ClaudeCLIVersion(); got != "2.1.350" {
		t.Errorf("ClaudeCLIVersion = %q, want 2.1.350 (bumped from %s)", got, oldVer)
	}

	// Older version in rejection does not downgrade
	rejectionOlder := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"claude_code_version_too_old: version 2.0.0 or newer is required"}}`)
	if AdoptRequiredClaudeCodeVersion(rejectionOlder) {
		t.Errorf("unexpected downgrade from older rejection")
	}
	if got := ClaudeCLIVersion(); got != "2.1.350" {
		t.Errorf("version downgraded: %q", got)
	}
}

func TestSanitizeClaudeThinkingBody_AdaptiveUpdatesClamped(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive","display":"updates"},"messages":[{"role":"user","content":"hi"}]}`)
	out := executor.SanitizeClaudeThinkingBody(body)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	th, _ := m["thinking"].(map[string]any)
	if th["display"] != "summarized" {
		t.Fatalf("thinking.display = %v, want summarized", th["display"])
	}
}

// Claude Code declares Task*, Read, Bash and friends itself. Injecting the decoy
// declaration of a name the client already uses leaves two entries with one name
// in tools[], and the model picks the dead copy: the call comes back as
// "[Tool TaskUpdate is unavailable]" and the turn is wasted.
func TestCloakClaudeTools_SkipsDecoysTheClientDeclares(t *testing.T) {
	req := map[string]any{
		"tools": []any{
			map[string]any{"name": "TaskUpdate", "input_schema": map[string]any{"type": "object"}},
			map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object"}},
		},
	}

	if cloakClaudeTools(req) == nil {
		t.Fatal("client tools should still be cloaked")
	}

	names := map[string]int{}
	for _, tRaw := range req["tools"].([]any) {
		tool, _ := tRaw.(map[string]any)
		name, _ := tool["name"].(string)
		names[name]++
		if base := strings.TrimSuffix(name, claudeToolSuffix); base == "TaskUpdate" || base == "Bash" {
			if desc, _ := tool["description"].(string); desc == "This tool is currently unavailable." {
				t.Errorf("%q was injected as a decoy despite the client declaring it", name)
			}
		}
	}
	if names["TaskUpdate"] != 0 || names["Bash"] != 0 {
		t.Errorf("unsuffixed decoy of a client tool must not be declared, got %v", names)
	}
	if names["Read"] != 1 {
		t.Errorf("a decoy the client does not declare should still be injected, got %v", names)
	}
}

// A client that is already Claude Code keeps its tool names untouched: renaming
// them leaves the system prompt naming tools that only exist as unusable decoys.
func TestCloakClaudeTools_LeavesClaudeCodeToolsAlone(t *testing.T) {
	tools := []any{}
	for _, name := range []string{"Task", "TaskUpdate", "Bash", "Read", "Edit", "Write"} {
		tools = append(tools, map[string]any{"name": name, "input_schema": map[string]any{"type": "object"}})
	}
	req := map[string]any{"tools": tools}

	if got := cloakClaudeTools(req); got != nil {
		t.Fatalf("a Claude Code client must not be cloaked, got map %v", got)
	}
	declared := req["tools"].([]any)
	if len(declared) != len(tools) {
		t.Fatalf("tools were reshaped: %d in, %d out", len(tools), len(declared))
	}
	for i, tRaw := range declared {
		want, _ := tools[i].(map[string]any)["name"].(string)
		got, _ := tRaw.(map[string]any)["name"].(string)
		if got != want {
			t.Errorf("tool %d renamed to %q, want %q", i, got, want)
		}
	}
}
