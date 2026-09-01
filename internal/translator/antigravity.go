package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/log"
)

// AntigravityRequest is the wrapper format for Antigravity API.
type AntigravityRequest struct {
	Project     string         `json:"project"`
	Model       string         `json:"model"`
	UserAgent   string         `json:"userAgent"`
	RequestType string         `json:"requestType"`
	RequestID   string         `json:"requestId"`
	Request     jsontext.Value `json:"request"`
}

// AntigravityNativeToolNames are tool names preserved without suffix.
var AntigravityNativeToolNames = map[string]bool{
	"browser_subagent": true,
	"command_status":   true,
	"find_by_name":     true,
	"generate_image":   true,
	"grep_search":      true,
	"list_dir":         true,
	"list_resources":   true,
	"mcp_sequential-thinking_sequentialthinking": true,
	"multi_replace_file_content":                 true,
	"notify_user":                                true,
	"read_resource":                              true,
	"read_terminal":                              true,
	"read_url_content":                           true,
	"replace_file_content":                       true,
	"run_command":                                true,
	"search_web":                                 true,
	"send_command_input":                         true,
	"task_boundary":                              true,
	"view_content_chunk":                         true,
	"view_file":                                  true,
	"write_to_file":                              true,
}

// AntigravityDecoyPlaceholderParams provides a valid schema for tools with no input parameters.
var AntigravityDecoyPlaceholderParams = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"reason": map[string]any{
			"type":        "string",
			"description": "Brief explanation of why you are calling this tool",
		},
	},
	"required": []string{"reason"},
}

// AntigravityDecoyTools are the 21 decoy tools matching official IDE defaults.
var AntigravityDecoyTools = []GeminiFunctionDecl{
	{Name: "browser_subagent", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "command_status", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "find_by_name", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "generate_image", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "grep_search", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "list_dir", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "list_resources", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "mcp_sequential-thinking_sequentialthinking", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "multi_replace_file_content", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "notify_user", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "read_resource", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "read_terminal", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "read_url_content", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "replace_file_content", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "run_command", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "search_web", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "send_command_input", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "task_boundary", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "view_content_chunk", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "view_file", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
	{Name: "write_to_file", Description: "This tool is currently unavailable.", Parameters: AntigravityDecoyPlaceholderParams},
}

// CloakAntigravityRequest cloaks tool names with `_ide` suffix and appends decoy tools.
func CloakAntigravityRequest(req *GeminiRequest, clientTool string) (*GeminiRequest, map[string]string) {
	if req == nil {
		return nil, nil
	}

	toolNameMap := make(map[string]string)
	if len(req.Tools) == 0 {
		return req, toolNameMap
	}

	isCopilot := clientTool == "github-copilot"
	var clientDecls []GeminiFunctionDecl
	decoyNames := make(map[string]bool, len(AntigravityDecoyTools))
	for _, dt := range AntigravityDecoyTools {
		decoyNames[dt.Name] = true
	}

	for _, toolGroup := range req.Tools {
		for _, fn := range toolGroup.FunctionDeclarations {
			if isCopilot && (AntigravityNativeToolNames[fn.Name] || decoyNames[fn.Name]) {
				continue
			}
			if AntigravityNativeToolNames[fn.Name] {
				clientDecls = append(clientDecls, fn)
				continue
			}

			suffixedName := fn.Name + "_ide"
			toolNameMap[suffixedName] = fn.Name
			fn.Name = suffixedName
			clientDecls = append(clientDecls, fn)
		}
	}

	// Merge client declarations first, then decoy tools (deduplicating)
	seen := make(map[string]bool)
	var allDecls []GeminiFunctionDecl
	for _, decl := range append(clientDecls, AntigravityDecoyTools...) {
		if decl.Name == "" || seen[decl.Name] {
			continue
		}
		seen[decl.Name] = true
		if decl.Parameters != nil {
			if b, err := json.Marshal(decl.Parameters); err == nil {
				cleaned := CleanParametersSchema(b)
				var cleanedParams any
				if err := json.Unmarshal(cleaned, &cleanedParams); err == nil {
					decl.Parameters = cleanedParams
				}
			}
		}
		allDecls = append(allDecls, decl)
	}

	// Update contents message history with renamed tools
	cloakedContents := make([]GeminiContent, len(req.Contents))
	for i, c := range req.Contents {
		cloakedParts := make([]GeminiPart, len(c.Parts))
		for j, p := range c.Parts {
			partCopy := p
			if p.FunctionCall != nil && !AntigravityNativeToolNames[p.FunctionCall.Name] {
				fc := *p.FunctionCall
				fc.Name = fc.Name + "_ide"
				partCopy.FunctionCall = &fc
			}
			if p.FunctionResponse != nil && !AntigravityNativeToolNames[p.FunctionResponse.Name] {
				fr := *p.FunctionResponse
				fr.Name = fr.Name + "_ide"
				partCopy.FunctionResponse = &fr
			}
			cloakedParts[j] = partCopy
		}
		cloakedContents[i] = GeminiContent{
			Role:  c.Role,
			Parts: cloakedParts,
		}
	}

	res := *req
	res.Tools = []GeminiTool{{FunctionDeclarations: allDecls}}
	res.ToolConfig = map[string]any{
		"functionCallingConfig": map[string]any{
			"mode": "VALIDATED",
		},
	}
	res.Contents = cloakedContents

	return &res, toolNameMap
}

// UncloakToolName restores the original tool name from a cloaked name.
func UncloakToolName(name string, toolMap map[string]string) string {
	if toolMap != nil {
		if orig, ok := toolMap[name]; ok {
			return orig
		}
	}
	name = strings.TrimPrefix(name, "proxy_")
	if strings.HasSuffix(name, "_ide") {
		return strings.TrimSuffix(name, "_ide")
	}
	return name
}

// Competitive prompt phrases that cause Antigravity to reject requests with 429.
var competitivePromptBlacklist = []string{
	"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
	"You are a Claude agent, built on Anthropic's Claude Agent SDK",
	"Anthropic's Claude Agent SDK",
}

var opencodeRegex = regexp.MustCompile(`(?i)\bopencode\b`)

func rewriteCompetingBranding(text string) string {
	for _, phrase := range competitivePromptBlacklist {
		text = strings.ReplaceAll(text, phrase, "")
	}
	text = opencodeRegex.ReplaceAllStringFunc(text, func(m string) string {
		switch m {
		case "OpenCode":
			return "Antigravity"
		case "OPENCODE":
			return "ANTIGRAVITY"
		default:
			return "antigravity"
		}
	})
	return strings.TrimSpace(text)
}

// StripCompetitivePrompts removes competitor identity strings and rewrites competing client branding.
func StripCompetitivePrompts(req *GeminiRequest) *GeminiRequest {
	if req == nil {
		return nil
	}
	res := *req
	if res.SystemInstruction != nil {
		parts := make([]GeminiPart, len(res.SystemInstruction.Parts))
		for i, p := range res.SystemInstruction.Parts {
			p.Text = rewriteCompetingBranding(p.Text)
			parts[i] = p
		}
		res.SystemInstruction = &GeminiContent{
			Role:  res.SystemInstruction.Role,
			Parts: parts,
		}
	}
	contents := make([]GeminiContent, len(res.Contents))
	for i, c := range res.Contents {
		parts := make([]GeminiPart, len(c.Parts))
		for j, p := range c.Parts {
			if p.Text != "" {
				p.Text = rewriteCompetingBranding(p.Text)
			}
			parts[j] = p
		}
		contents[i] = GeminiContent{Role: c.Role, Parts: parts}
	}
	res.Contents = contents
	return &res
}

// AntigravityModelSynonyms maps client/UI model names to internal Google Antigravity backend model IDs.
// Upstream Google Antigravity uses "gemini-3.7-flash-tiered" / "gemini-3.6-flash-tiered" as backend model names.
var AntigravityModelSynonyms = map[string]string{
	"gemini-default":             "gemini-3-flash-agent",
	"gemini-3.5-flash":           "gemini-3-flash-agent",
	"gemini-3.5-flash-high":      "gemini-3-flash-agent",
	"gemini-3.5-flash-medium":    "gemini-3-flash-agent",
	"gemini-3.5-flash-low":       "gemini-3-flash-agent",
	"gemini-3.5-flash-extra-low": "gemini-3-flash-agent",
	"gemini-3.5-flash-agent":     "gemini-3-flash-agent",
	"gemini-3.1-pro-high":        "gemini-pro-agent",
	"gemini-3.1-pro":             "gemini-pro-agent",
	"gemini-3-pro-high":          "gemini-pro-agent",
	"gemini-3-pro-low":           "gemini-3.1-pro-low",
	// 3.7 flash tiered models -> backend model: gemini-3.7-flash-tiered
	"gemini-3.7-flash":           "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-high":      "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-agent":     "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-medium":    "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-low":       "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-extra-low": "gemini-3.7-flash-tiered",
	"gemini-3.7-flash-thinking":  "gemini-3.7-flash-tiered",
	// 3.6 flash tiered models -> backend model: gemini-3.6-flash-tiered
	"gemini-3.6-flash":           "gemini-3.6-flash-tiered",
	"gemini-3.6-flash-high":      "gemini-3.6-flash-tiered",
	"gemini-3.6-flash-medium":    "gemini-3.6-flash-tiered",
	"gemini-3.6-flash-low":       "gemini-3.6-flash-tiered",
}

// NormalizeAntigravityModel maps known aliases/synonyms to Antigravity internal backend model names.
func NormalizeAntigravityModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	// Strip trailing thinking suffix like (high), (medium), (low) if present
	if idx := strings.Index(m, "("); idx != -1 && strings.HasSuffix(m, ")") {
		m = strings.TrimSpace(m[:idx])
	}
	if canonical, ok := AntigravityModelSynonyms[m]; ok {
		return canonical
	}
	return m
}

// WrapForAntigravity wraps a standard Gemini request in Antigravity API envelope.
func WrapForAntigravity(geminiBody []byte, projectID, modelName string) ([]byte, error) {
	modelName = NormalizeAntigravityModel(modelName)

	var geminiReq GeminiRequest
	if err := json.Unmarshal(geminiBody, &geminiReq); err == nil {
		cleanedReq := StripCompetitivePrompts(&geminiReq)
		if len(cleanedReq.Tools) > 0 {
			cloaked, _ := CloakAntigravityRequest(cleanedReq, "")
			cleanedReq = cloaked
		}
		if cloakedBytes, err := json.Marshal(cleanedReq); err == nil {
			geminiBody = cloakedBytes
		}
	}

	wrapper := AntigravityRequest{
		Project:     projectID,
		Model:       modelName,
		UserAgent:   "antigravity",
		RequestType: "agent",
		RequestID:   fmt.Sprintf("agent/%s/%d/%s/%d", projectID, time.Now().UnixMilli(), modelName, 1),
		Request:     geminiBody,
	}
	out, err := json.Marshal(wrapper)
	if err != nil {
		return nil, fmt.Errorf("marshal antigravity wrapper: %w", err)
	}
	return out, nil
}

// UnwrapAntigravityResponse extracts the inner Gemini response from antigravity envelope.
func UnwrapAntigravityResponse(raw []byte) []byte {
	var envelope struct {
		Response jsontext.Value `json:"response"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		log.Warn("translator", "unmarshal envelope failed", "error", err)
		return raw // passthrough on failure
	}
	if len(envelope.Response) == 0 {
		log.Warn("translator", "empty envelope response")
		return raw // passthrough on failure
	}
	return []byte(envelope.Response)
}

// IsAntigravityImageModel checks if the model name is an image generation model.
func IsAntigravityImageModel(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "image") || strings.Contains(m, "imagen")
}

// gcd computes greatest common divisor for resolution reduction.
func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// ParseImageConfig extracts the base model name and aspect ratio from model suffixes.
func ParseImageConfig(model string) (cleanModel, aspectRatio string) {
	aspectRatio = "1:1"
	cleanModel = model

	// Look for suffix like -16x9, -4x3, -1x1, -1024x768
	if before, suffix, ok := strings.CutLast(model, "-"); ok && suffix != "" {
		if xIdx := strings.Index(suffix, "x"); xIdx != -1 {
			var w, h int
			if _, err := fmt.Sscanf(suffix, "%dx%d", &w, &h); err == nil && w > 0 && h > 0 {
				cleanModel = before
				if w <= 16 && h <= 16 {
					aspectRatio = fmt.Sprintf("%d:%d", w, h)
				} else {
					d := gcd(w, h)
					aspectRatio = fmt.Sprintf("%d:%d", w/d, h/d)
				}
			}
		}
	}
	return cleanModel, aspectRatio
}

// WrapAntigravityImageRequest builds an Antigravity request envelope for image generation.
func WrapAntigravityImageRequest(prompt, base64Input, projectID, cleanModel, aspectRatio string) ([]byte, error) {
	parts := []GeminiPart{}
	if base64Input != "" {
		parts = append(parts, GeminiPart{
			InlineData: &GeminiInlineData{
				MimeType: "image/png",
				Data:     base64Input,
			},
		})
	}
	if prompt != "" {
		parts = append(parts, GeminiPart{
			Text: prompt,
		})
	}

	contents := []GeminiContent{
		{
			Role:  "user",
			Parts: parts,
		},
	}

	sessionID := fmt.Sprintf("img-%d", time.Now().UnixNano())
	genConfig := map[string]any{
		"temperature":     1.0,
		"topP":            0.95,
		"topK":            40,
		"maxOutputTokens": 8192,
		"imageConfig": map[string]string{
			"aspectRatio": aspectRatio,
		},
	}

	reqPayload := map[string]any{
		"contents":         contents,
		"generationConfig": genConfig,
		"sessionId":        sessionID,
	}

	reqJSON, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal image request: %w", err)
	}

	wrapper := AntigravityRequest{
		Project:     projectID,
		Model:       cleanModel,
		UserAgent:   "antigravity",
		RequestType: "image_gen",
		RequestID:   fmt.Sprintf("agent/%s/%d/%s/1", projectID, time.Now().UnixMilli(), cleanModel),
		Request:     reqJSON,
	}

	return json.Marshal(wrapper)
}

// FormatAntigravityImageResponse converts a Gemini response containing inlineData to OpenAI images response format.
func FormatAntigravityImageResponse(rawGeminiResp []byte, prompt string) ([]byte, error) {
	unwrapped := UnwrapAntigravityResponse(rawGeminiResp)
	var resp GeminiResponse
	if err := json.Unmarshal(unwrapped, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal gemini image response: %w", err)
	}

	var images []map[string]string
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.InlineData != nil && part.InlineData.Data != "" {
				images = append(images, map[string]string{
					"b64_json": part.InlineData.Data,
				})
			}
		}
	}

	if len(images) == 0 {
		images = append(images, map[string]string{
			"b64_json":       "",
			"revised_prompt": prompt,
		})
	}

	result := map[string]any{
		"created": time.Now().Unix(),
		"data":    images,
	}

	return json.Marshal(result)
}

// decloakContentBlockStart is the shared implementation that restores the
// original tool name on a parsed Claude content_block_start event with
// content_block.type == "tool_use". It never mutates the input map. The
// boolean reports whether a tool name was actually rewritten.
func decloakContentBlockStart(event map[string]any, toolNameMap map[string]string) (map[string]any, bool) {
	if len(toolNameMap) == 0 || event == nil {
		return event, false
	}
	if event["type"] != "content_block_start" {
		return event, false
	}
	block, ok := event["content_block"].(map[string]any)
	if !ok || block == nil || block["type"] != "tool_use" {
		return event, false
	}
	name, ok := block["name"].(string)
	if !ok || name == "" {
		return event, false
	}
	original, found := toolNameMap[name]
	if !found {
		return event, false
	}
	blockCopy := make(map[string]any, len(block))
	for k, v := range block {
		blockCopy[k] = v
	}
	blockCopy["name"] = original
	eventCopy := make(map[string]any, len(event))
	for k, v := range event {
		eventCopy[k] = v
	}
	eventCopy["content_block"] = blockCopy
	return eventCopy, true
}

// DecloakStreamChunk restores original tool names in streamed Claude SSE event chunks.
// Specifically handles "content_block_start" events with content_block.type == "tool_use".
func DecloakStreamChunk(chunkBytes []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(chunkBytes) == 0 {
		return chunkBytes
	}

	var raw map[string]any
	if err := json.Unmarshal(chunkBytes, &raw); err != nil {
		return chunkBytes
	}

	out, changed := decloakContentBlockStart(raw, toolNameMap)
	if !changed {
		return chunkBytes
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return chunkBytes
	}
	return encoded
}

// DecloakClaudeStreamEvent restores the original tool name on parsed Claude content_block_start event.
func DecloakClaudeStreamEvent(event map[string]any, toolNameMap map[string]string) map[string]any {
	out, _ := decloakContentBlockStart(event, toolNameMap)
	return out
}
