package translator_test

import (
	json "encoding/json/v2"
	"regexp"
	"strings"
	"testing"

	"9router/proxy/internal/translator"
)

func TestCloakAntigravityRequest_WebSearchGetsRenamed(t *testing.T) {
	// web_search (Claude's server tool type) is NOT in AntigravityNativeToolNames
	// — only search_web (Gemini's native) is. This test locks in the distinction:
	// web_search must be renamed to web_search_ide, while search_web stays as-is.
	req := &translator.GeminiRequest{
		Contents: []translator.GeminiContent{
			{
				Role: "user",
				Parts: []translator.GeminiPart{
					{Text: "cari sesuatu"},
				},
			},
		},
		Tools: []translator.GeminiTool{
			{
				FunctionDeclarations: []translator.GeminiFunctionDecl{
					{
						Name:        "web_search",
						Description: "Search the internet",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"query": map[string]any{
									"type":      "string",
									"minLength": 1,
									"maxLength": 4000,
								},
							},
							"required": []string{"query"},
						},
					},
					{
						Name:        "search_web",
						Description: "Gemini web search",
						Parameters: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"query": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
	}

	cloaked, toolMap := translator.CloakAntigravityRequest(req, "")
	if cloaked == nil {
		t.Fatal("expected cloaked request, got nil")
	}

	// web_search → web_search_ide (not native)
	if orig, ok := toolMap["web_search_ide"]; !ok || orig != "web_search" {
		t.Errorf("expected toolMap[web_search_ide] = web_search, got %s/%v", orig, ok)
	}

	// search_web — native, no rename
	if _, ok := toolMap["search_web"]; ok {
		t.Errorf("search_web is native and should NOT be in toolMap")
	}

	// Verify the renamed declaration has cleaned schema (no minLength/maxLength)
	foundWS := false
	foundSW := false
	for _, tg := range cloaked.Tools {
		for _, decl := range tg.FunctionDeclarations {
			switch decl.Name {
			case "web_search_ide":
				foundWS = true
				params, ok := decl.Parameters.(map[string]any)
				if !ok {
					t.Fatalf("web_search_ide parameters not an object: %T", decl.Parameters)
				}
				props, _ := params["properties"].(map[string]any)
				query, _ := props["query"].(map[string]any)
				if _, has := query["minLength"]; has {
					t.Errorf("web_search_ide query schema still has minLength (should be cleaned)")
				}
				if _, has := query["maxLength"]; has {
					t.Errorf("web_search_ide query schema still has maxLength (should be cleaned)")
				}
			case "search_web":
				foundSW = true
				// search_web is native so the client's declaration passes through
				// unchanged (the decoy "unavailable" version is deduped away).
				if decl.Description != "Gemini web search" {
					t.Errorf("search_web should keep client description, got %q", decl.Description)
				}
			}
		}
	}
	if !foundWS {
		t.Error("web_search_ide not found in cloaked tools")
	}
	if !foundSW {
		t.Error("search_web decoy not found in cloaked tools")
	}
}

func TestCloakAntigravityRequest_RenamesAndInjectsDecoys(t *testing.T) {
	req := &translator.GeminiRequest{
		Contents: []translator.GeminiContent{
			{
				Role: "model",
				Parts: []translator.GeminiPart{
					{FunctionCall: &translator.GeminiFunctionCall{Name: "execute_code", Args: map[string]any{"code": "ls"}}},
				},
			},
			{
				Role: "user",
				Parts: []translator.GeminiPart{
					{FunctionResponse: &translator.GeminiFunctionResp{Name: "execute_code"}},
				},
			},
		},
		Tools: []translator.GeminiTool{
			{
				FunctionDeclarations: []translator.GeminiFunctionDecl{
					{Name: "execute_code", Description: "Run code"},
					{Name: "run_command", Description: "Native tool"},
				},
			},
		},
	}

	cloaked, toolMap := translator.CloakAntigravityRequest(req, "")
	if cloaked == nil {
		t.Fatal("expected cloaked request, got nil")
	}

	// execute_code should be renamed to execute_code_ide
	if toolMap["execute_code_ide"] != "execute_code" {
		t.Errorf("expected toolMap[execute_code_ide] = execute_code, got %s", toolMap["execute_code_ide"])
	}

	// Check function call in history was renamed
	if cloaked.Contents[0].Parts[0].FunctionCall.Name != "execute_code_ide" {
		t.Errorf("expected contents functionCall renamed to execute_code_ide, got %s", cloaked.Contents[0].Parts[0].FunctionCall.Name)
	}
	if cloaked.Contents[1].Parts[0].FunctionResponse.Name != "execute_code_ide" {
		t.Errorf("expected contents functionResponse renamed to execute_code_ide, got %s", cloaked.Contents[1].Parts[0].FunctionResponse.Name)
	}

	// Check 21 decoy tools injected
	if len(cloaked.Tools) == 0 || len(cloaked.Tools[0].FunctionDeclarations) < 20 {
		t.Errorf("expected >= 20 function declarations including decoys, got %d", len(cloaked.Tools[0].FunctionDeclarations))
	}
}

func TestUncloakToolName(t *testing.T) {
	toolMap := map[string]string{
		"execute_code_ide": "execute_code",
		"custom_tool_ide":  "custom_tool",
	}

	if un := translator.UncloakToolName("execute_code_ide", toolMap); un != "execute_code" {
		t.Errorf("expected execute_code, got %s", un)
	}
	if un := translator.UncloakToolName("run_command", toolMap); un != "run_command" {
		t.Errorf("expected run_command unchanged, got %s", un)
	}
	if un := translator.UncloakToolName("other_ide", nil); un != "other" {
		t.Errorf("expected other (suffix stripped), got %s", un)
	}
}

func TestAntigravityImageModelAndConfig(t *testing.T) {
	if !translator.IsAntigravityImageModel("gemini-3.1-flash-image") {
		t.Error("expected gemini-3.1-flash-image to be image model")
	}
	if !translator.IsAntigravityImageModel("imagen-3.0-generate-002") {
		t.Error("expected imagen-3.0-generate-002 to be image model")
	}
	if translator.IsAntigravityImageModel("gemini-3-flash") {
		t.Error("expected gemini-3-flash NOT to be image model")
	}

	clean, ratio := translator.ParseImageConfig("gemini-3.1-flash-image-16x9")
	if clean != "gemini-3.1-flash-image" || ratio != "16:9" {
		t.Errorf("expected (gemini-3.1-flash-image, 16:9), got (%s, %s)", clean, ratio)
	}

	clean2, ratio2 := translator.ParseImageConfig("gemini-3.1-flash-image-1024x768")
	if clean2 != "gemini-3.1-flash-image" || ratio2 != "4:3" {
		t.Errorf("expected (gemini-3.1-flash-image, 4:3), got (%s, %s)", clean2, ratio2)
	}
}

func TestWrapForAntigravity_OmitsAgentRequestType(t *testing.T) {
	// Port of decolua/9router#3986: requestType="agent" triggers a false
	// upstream 429 bucket on OMP harness payloads, so it must be omitted.
	geminiBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	wrapped, err := translator.WrapForAntigravity(geminiBody, "proj-1", "gemini-3.8-flash-medium")
	if err != nil {
		t.Fatalf("WrapForAntigravity failed: %v", err)
	}
	if strings.Contains(string(wrapped), `"requestType"`) {
		t.Errorf("expected no requestType field in envelope, got %s", string(wrapped))
	}
	var req translator.AntigravityRequest
	if err := json.Unmarshal(wrapped, &req); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	if req.RequestType != "" {
		t.Errorf("expected empty RequestType, got %q", req.RequestType)
	}
	if !antigravityRequestIDRe.MatchString(req.RequestID) {
		t.Errorf("request ID %q does not match UUID format", req.RequestID)
	}
}

func TestWrapAntigravityImageRequest(t *testing.T) {
	reqBytes, err := translator.WrapAntigravityImageRequest("A cute cat", "", "proj-123", "gemini-3.1-flash-image", "16:9")
	if err != nil {
		t.Fatalf("WrapAntigravityImageRequest failed: %v", err)
	}
	if len(reqBytes) == 0 {
		t.Fatal("expected non-empty request bytes")
	}

	var req translator.AntigravityRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		t.Fatalf("unmarshal wrapper failed: %v", err)
	}
	if req.RequestType != "image_gen" {
		t.Errorf("expected requestType image_gen, got %s", req.RequestType)
	}
	if req.Project != "proj-123" {
		t.Errorf("expected project proj-123, got %s", req.Project)
	}
}

func TestNormalizeAntigravityModel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Upstream decolua/9router#24034f69 retired the 3.5 family: these
		// aliases now resolve into the 3.8 tiers rather than gemini-3-flash-agent.
		{"gemini-3.5-flash-high", "gemini-3.8-flash-high"},
		{"gemini-3.5-flash-medium", "gemini-3.8-flash-medium"},
		{"gemini-3.5-flash-extra-low", "gemini-3.8-flash-low"},
		{"gemini-3.1-pro-high", "gemini-pro-agent"},
		{"gemini-3-pro-high", "gemini-pro-agent"},
		{"gemini-3-pro-low", "gemini-3.1-pro-low"},
		{"gemini-default", "gemini-3.8-flash-medium"},
		{"gemini-3.7-flash-high", "gemini-3.7-flash-tiered"},
		{"gemini-3.7-flash", "gemini-3.7-flash-tiered"},
		{"gemini-3.7-flash-low", "gemini-3.7-flash-tiered"},
		{"gemini-3.6-flash-high", "gemini-3.6-flash-tiered"},
		{"gemini-3.7-flash-tiered(high)", "gemini-3.7-flash-tiered"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"gemini-3-flash-agent", "gemini-3.8-flash-high"},
		// The 3.8 family serves one backend id per tier.
		{"gemini-3.8-flash-high", "gemini-3.8-flash-high"},
		{"gemini-3.8-flash", "gemini-3.8-flash-medium"},
	}

	for _, tt := range tests {
		got := translator.NormalizeAntigravityModel(tt.input)
		if got != tt.expected {
			t.Errorf("NormalizeAntigravityModel(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestAntigravityDecoyTools_NonEmptyProperties(t *testing.T) {
	for _, dt := range translator.AntigravityDecoyTools {
		params, ok := dt.Parameters.(map[string]any)
		if !ok {
			t.Fatalf("decoy tool %s has invalid parameters type", dt.Name)
		}
		props, ok := params["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Errorf("decoy tool %s has empty properties; Gemini will reject with 'Invalid tool parameters'", dt.Name)
		}
	}
}

func TestTranslateOpenAIToGemini_ClaudeCodeToolResponseMapping(t *testing.T) {
	body := []byte(`{
		"model": "antigravity/gemini-3.5-flash-high",
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "toolu_01ABC123",
						"type": "function",
						"function": {
							"name": "plugin:claude-mem:mcp-search",
							"arguments": "{\"query\":\"test\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "toolu_01ABC123",
				"content": "{\"results\":[]}"
			}
		],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "plugin:claude-mem:mcp-search",
					"description": "search memory",
					"parameters": {
						"type": "object",
						"properties": {
							"query": { "type": "string" }
						},
						"required": ["query"]
					}
				}
			}
		]
	}`)

	geminiJSON, err := translator.TranslateOpenAIToGemini(body)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGemini failed: %v", err)
	}

	var req translator.GeminiRequest
	if err := json.Unmarshal(geminiJSON, &req); err != nil {
		t.Fatalf("unmarshal gemini request failed: %v", err)
	}

	// NormalizeGeminiContents brackets the conversation with user turns, so the
	// response part is found wherever it landed rather than at a fixed index.
	var respPart *translator.GeminiFunctionResp
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				respPart = p.FunctionResponse
			}
		}
	}
	if respPart == nil {
		t.Fatalf("expected a functionResponse part in %d contents", len(req.Contents))
	}
	// Tool response part must have exact name "plugin:claude-mem:mcp-search"
	if respPart.Name != "plugin:claude-mem:mcp-search" {
		t.Errorf("expected functionResponse name 'plugin:claude-mem:mcp-search', got %q", respPart.Name)
	}
}

func TestStripCompetitivePrompts(t *testing.T) {
	req := &translator.GeminiRequest{
		SystemInstruction: &translator.GeminiContent{
			Role: "user",
			Parts: []translator.GeminiPart{
				{Text: "You are a Claude agent, built on Anthropic's Claude Agent SDK. Solve this task."},
			},
		},
		Contents: []translator.GeminiContent{
			{
				Role: "user",
				Parts: []translator.GeminiPart{
					{Text: "You are a Claude agent, built on Anthropic's Claude Agent SDK. Do something."},
				},
			},
		},
	}

	stripped := translator.StripCompetitivePrompts(req)
	if strings.Contains(stripped.SystemInstruction.Parts[0].Text, "Anthropic's Claude Agent SDK") {
		t.Errorf("expected competitive prompt removed from systemInstruction, got %s", stripped.SystemInstruction.Parts[0].Text)
	}
	if strings.Contains(stripped.Contents[0].Parts[0].Text, "Anthropic's Claude Agent SDK") {
		t.Errorf("expected competitive prompt removed from contents, got %s", stripped.Contents[0].Parts[0].Text)
	}
}
func TestStripCompetitivePrompts_NormalizesHarnessTagsInSystemOnly(t *testing.T) {
	req := &translator.GeminiRequest{
		SystemInstruction: &translator.GeminiContent{
			Role: "user",
			Parts: []translator.GeminiPart{
				{Text: "<system-conventions>\nRFC 2119 body stays.</system-conventions> <system-directive>dir stays</system-directive> <critical>crit stays</critical> Oh My Pi coding harness Oh My Pi omp Live"},
			},
		},
		Contents: []translator.GeminiContent{
			{
				Role: "user",
				Parts: []translator.GeminiPart{
					{Text: "<system-conventions> caller text untouched Oh My Pi"},
				},
			},
		},
	}

	stripped := translator.StripCompetitivePrompts(req)
	sys := stripped.SystemInstruction.Parts[0].Text
	for _, want := range []string{"<conventions>", "RFC 2119 body stays", "<instructions>dir stays", "<important>crit stays", "AI coding assistant"} {
		if !strings.Contains(sys, want) {
			t.Errorf("expected system text to contain %q, got %q", want, sys)
		}
	}
	for _, banned := range []string{"system-conventions", "system-directive", "critical", "Oh My Pi"} {
		if strings.Contains(sys, banned) {
			t.Errorf("expected system text to drop %q, got %q", banned, sys)
		}
	}
	kept := stripped.Contents[0].Parts[0].Text
	if !strings.Contains(kept, "<system-conventions>") || !strings.Contains(kept, "Oh My Pi") {
		t.Errorf("expected caller content preserved, got %q", kept)
	}
}

func TestStripCompetitivePrompts_HermesAndBillingHeader(t *testing.T) {
	req := &translator.GeminiRequest{
		SystemInstruction: &translator.GeminiContent{
			Role: "user",
			Parts: []translator.GeminiPart{
				{Text: "x-anthropic-billing-header: cc_version=2.1.275.f15; cc_entrypoint=cli;\n\nYou are Hermes Agent, an intelligent AI assistant created by Nous Research. Help user."},
			},
		},
	}

	stripped := translator.StripCompetitivePrompts(req)
	sys := stripped.SystemInstruction.Parts[0].Text
	if strings.Contains(sys, "x-anthropic-billing-header") {
		t.Errorf("expected billing header stripped, got %q", sys)
	}
	if strings.Contains(sys, "created by Nous Research") {
		t.Errorf("expected Nous Research stripped from Hermes identity, got %q", sys)
	}
	if !strings.Contains(sys, "You are Hermes Agent. You are an intelligent AI assistant.") {
		t.Errorf("expected sanitized Hermes identity, got %q", sys)
	}
}

func TestWrapForAntigravity_NormalizesTriggerSystemPrompt(t *testing.T) {
	geminiBody := []byte(`{"system_instruction":{"role":"user","parts":[{"text":"<system-conventions>\nRFC 2119: MUST, REQUIRED, SHOULD, RECOMMENDED, MAY, OPTIONAL."}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	wrapped, err := translator.WrapForAntigravity(geminiBody, "proj-1", "gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("WrapForAntigravity failed: %v", err)
	}
	inner := unwrapInnerRequest(t, wrapped)
	si, _ := inner["system_instruction"].(map[string]any)
	if si == nil {
		t.Fatal("system_instruction missing from wrapped request")
	}
	parts, _ := si["parts"].([]any)
	if len(parts) == 0 {
		t.Fatal("system_instruction.parts missing")
	}
	part, _ := parts[0].(map[string]any)
	text, _ := part["text"].(string)
	if strings.Contains(text, "system-conventions") {
		t.Errorf("expected trigger tag normalized, got %q", text)
	}
	if !strings.Contains(text, "<conventions>") || !strings.Contains(text, "RFC 2119") {
		t.Errorf("expected neutral tag with intact body, got %q", text)
	}
}

// antigravityBackendModels is the set of ids the Antigravity backend actually
// serves. A synonym or an upstreamModelId row pointing outside it is a 404 in
// production, and both tables are hand-maintained, so the invariant is checked
// rather than trusted.
var antigravityBackendModels = map[string]bool{
	"gemini-pro-agent":         true,
	"gemini-3.1-pro-low":       true,
	"gemini-3.8-flash-high":    true,
	"gemini-3.8-flash-medium":  true,
	"gemini-3.8-flash-low":     true,
	"gemini-3.7-flash-tiered":  true,
	"gemini-3.6-flash-tiered":  true,
	"claude-sonnet-5-5-high":   true,
	"claude-sonnet-5-5-medium": true,
	"claude-sonnet-5-5-low":    true,
	"claude-opus-5-5-high":     true,
	"claude-opus-5-5-medium":   true,
	"claude-opus-5-5-low":      true,
	"claude-sonnet-4-6":        true,
	"claude-opus-4-6-thinking": true,
	"gpt-oss-120b-medium":      true,
	"gemini-3.1-flash-image":   true,
}

func TestAntigravityModelIDsResolveToServedBackends(t *testing.T) {
	for alias, target := range translator.AntigravityModelSynonyms {
		if !antigravityBackendModels[target] {
			t.Errorf("Antigravity synonym %q maps to invalid upstream model %q (will cause 404)", alias, target)
		}
	}
}

// TestAntigravityUpstreamModel pins the wire id. Google serves none of the
// parenthesised ids the registry carries — probed live 2026-10-09, every
// "id(tier)" form is 404 NOT_FOUND while its bare base is 200 — so the effort
// notation must never survive this function. A test that asserts a suffix here
// is asserting a 404.
func TestAntigravityUpstreamModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Claude 5.5: the bare catalog id is the thinking tier, and the
		// registry's own "(high)" preset is 9router notation, not wire.
		{"claude-sonnet-5-5", "claude-sonnet-5-5-high"},
		{"claude-sonnet-5-5-high", "claude-sonnet-5-5-high"},
		{"claude-sonnet-5-5-medium", "claude-sonnet-5-5-medium"},
		{"claude-sonnet-5-5-low", "claude-sonnet-5-5-low"},
		{"claude-opus-5-5", "claude-opus-5-5-high"},
		{"claude-opus-5-5-low", "claude-opus-5-5-low"},
		// A client-supplied effort narrows the id to its own base rather than
		// being appended on top of the id's preset.
		{"claude-sonnet-5-5-medium(low)", "claude-sonnet-5-5-medium"},
		{"claude-sonnet-5-5(high)", "claude-sonnet-5-5-high"},
		// The tiered Gemini families.
		{"gemini-3.8-flash", "gemini-3.8-flash-medium"},
		{"gemini-3.8-flash-low", "gemini-3.8-flash-low"},
		{"gemini-3.8-flash-high", "gemini-3.8-flash-high"},
		{"gemini-3.7-flash-high", "gemini-3.7-flash-tiered"},
		{"gemini-3.6-flash-medium", "gemini-3.6-flash-tiered"},
		{"gemini-3.1-pro-high", "gemini-pro-agent"},
		{"gemini-3.1-pro-low", "gemini-3.1-pro-low"},
		// A retired id resolves through its synonym family first, so the 3.5
		// high tier lands on the 3.8 high backend rather than on medium.
		{"gemini-3.5-flash-high", "gemini-3.8-flash-high"},
		// An unknown id passes through untouched.
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"some-model-this-port-never-saw", "some-model-this-port-never-saw"},
	}

	for _, tt := range tests {
		if got := translator.AntigravityUpstreamModel(tt.input); got != tt.want {
			t.Errorf("AntigravityUpstreamModel(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestAntigravityUpstreamModel_NeverEmitsEffortSuffix is the invariant the live
// 404 turned on: whatever the registry row says, the id handed to Cloud Code
// must be bare. Google rejects "gemini-3.8-flash-low(low)" outright.
func TestAntigravityUpstreamModel_NeverEmitsEffortSuffix(t *testing.T) {
	for alias := range translator.AntigravityModelSynonyms {
		got := translator.AntigravityUpstreamModel(alias)
		if strings.Contains(got, "(") {
			t.Errorf("AntigravityUpstreamModel(%q) = %q, which carries the effort suffix and 404s on the wire", alias, got)
		}
		if !antigravityBackendModels[got] {
			t.Errorf("AntigravityUpstreamModel(%q) = %q, which is not a served backend", alias, got)
		}
	}
}

// TestAntigravityUpstreamModel_ServesABackend closes the loop: every id
// AntigravityUpstreamModel can emit must name a backend the service serves.
func TestAntigravityUpstreamModel_ServesABackend(t *testing.T) {
	for _, model := range []string{
		"gemini-3.8-flash", "gemini-3.7-flash-high", "gemini-3.6-flash-low",
		"gemini-3.1-pro-low", "claude-sonnet-5-5", "claude-opus-5-5-low",
	} {
		got := translator.AntigravityUpstreamModel(model)
		if !antigravityBackendModels[got] {
			t.Errorf("AntigravityUpstreamModel(%q) = %q, which is not a served backend", model, got)
		}
	}
}

func unwrapInnerRequest(t *testing.T, wrapped []byte) map[string]any {
	t.Helper()
	var req translator.AntigravityRequest
	if err := json.Unmarshal(wrapped, &req); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	var inner map[string]any
	if err := json.Unmarshal([]byte(req.Request), &inner); err != nil {
		t.Fatalf("unmarshal inner request: %v", err)
	}
	return inner
}

var antigravityRequestIDRe = regexp.MustCompile(`^agent/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/\d+/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/\d+$`)

func TestWrapForAntigravity_CapAndRequestID(t *testing.T) {
	geminiBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":100000}}`)
	wrapped, err := translator.WrapForAntigravity(geminiBody, "proj-1", "gemini-3.7-flash-high")
	if err != nil {
		t.Fatalf("WrapForAntigravity failed: %v", err)
	}

	var req translator.AntigravityRequest
	if err := json.Unmarshal(wrapped, &req); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	// The wire id is the family backend: the "(high)" the registry carries is
	// 9router's thinking notation, not a Google model id, and 404s on the wire.
	if req.Model != "gemini-3.7-flash-tiered" {
		t.Errorf("expected model gemini-3.7-flash-tiered, got %s", req.Model)
	}
	if !antigravityRequestIDRe.MatchString(req.RequestID) {
		t.Errorf("request ID %q does not match UUID format", req.RequestID)
	}

	inner := unwrapInnerRequest(t, wrapped)
	gc, _ := inner["generationConfig"].(map[string]any)
	if gc == nil {
		t.Fatal("generationConfig missing")
	}
	if v, _ := gc["maxOutputTokens"].(float64); v != 64000 {
		t.Errorf("expected maxOutputTokens capped to 64000, got %v", gc["maxOutputTokens"])
	}
	// The tier must NOT be injected as a thinking config: gemini-3.7-flash-tiered
	// is always-thinking, and injecting thinkingConfig triggers strict thought
	// signature validation that rejects the backfilled default signature.
	if _, ok := gc["thinkingConfig"]; ok {
		t.Errorf("expected no thinkingConfig injection, got %v", gc["thinkingConfig"])
	}
}

func TestWrapForAntigravity_StripsThinkingFields(t *testing.T) {
	geminiBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"thinking":{"type":"enabled"},"reasoning_effort":"high","output_config":{},"enable_thinking":true}`)
	wrapped, err := translator.WrapForAntigravity(geminiBody, "proj-1", "gemini-3-flash")
	if err != nil {
		t.Fatalf("WrapForAntigravity failed: %v", err)
	}

	inner := unwrapInnerRequest(t, wrapped)
	for _, k := range []string{"thinking", "reasoning_effort", "output_config", "enable_thinking"} {
		if _, ok := inner[k]; ok {
			t.Errorf("expected %q stripped, still present", k)
		}
	}
}
