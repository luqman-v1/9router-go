package translator

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/log"
)

// maxAntigravityOutputTokens caps generationConfig.maxOutputTokens; Google's
// Antigravity backend rejects larger values (parity with Next.js capabilities.js).
const maxAntigravityOutputTokens = 64000

// antigravityRequestBlacklist are fields Google generateContent rejects when
// present at the request root (thinking/reasoning fields set by upstream clients).
var antigravityRequestBlacklist = []string{
	"output_config",
	"thinking",
	"reasoning_effort",
	"reasoning",
	"enable_thinking",
	"thinking_budget",
	"thinkingConfig",
}

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
		var filtered []GeminiPart
		for _, p := range res.SystemInstruction.Parts {
			text := rewriteCompetingBranding(p.Text)
			// Drop empty system instruction parts - Gemini requires oneof data field
			if strings.TrimSpace(text) == "" && p.FunctionCall == nil && p.FunctionResponse == nil && p.InlineData == nil && p.FileData == nil && p.ThoughtSignature == "" {
				continue
			}
			p.Text = text
			filtered = append(filtered, p)
		}
		if len(filtered) == 0 {
			res.SystemInstruction = nil
		} else {
			res.SystemInstruction = &GeminiContent{
				Role:  res.SystemInstruction.Role,
				Parts: filtered,
			}
		}
	}
	contents := make([]GeminiContent, 0, len(res.Contents))
	for _, c := range res.Contents {
		var filtered []GeminiPart
		for _, p := range c.Parts {
			if p.Text != "" {
				text := rewriteCompetingBranding(p.Text)
				if strings.TrimSpace(text) == "" && p.FunctionCall == nil && p.FunctionResponse == nil && p.InlineData == nil && p.FileData == nil && p.ThoughtSignature == "" {
					continue
				}
				p.Text = text
			}
			// Keep non-text parts as-is, drop truly empty text parts
			if p.Text == "" && p.FunctionCall == nil && p.FunctionResponse == nil && p.InlineData == nil && p.FileData == nil && p.ThoughtSignature == "" && p.Thought == nil {
				continue
			}
			filtered = append(filtered, p)
		}
		if len(filtered) == 0 {
			continue
		}
		contents = append(contents, GeminiContent{Role: c.Role, Parts: filtered})
	}
	if len(contents) > 0 || len(res.Contents) == 0 {
		res.Contents = contents
	}
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

// antigravityUUIDFromSeed derives a deterministic RFC-4122-style UUID (v5-like)
// from a seed string, matching the Next.js uuidFromSeed helper so trajectory and
// conversation IDs are stable per session.
func antigravityUUIDFromSeed(seed string) string {
	h := sha256.Sum256([]byte(seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	hex := fmt.Sprintf("%x", b)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex[0:8], hex[8:12], hex[12:16], hex[16:20], hex[20:32])
}

// antigravityBuildRequestID builds the IDE request ID in the same shape as the
// Next.js reference: agent/<conversationId>/<ts>/<trajectoryId>/<step>.
func antigravityBuildRequestID(sessionID, model, requestType string, contentCount int) string {
	if sessionID == "" {
		sessionID = "anonymous"
	}
	conversationID := antigravityUUIDFromSeed("antigravity:conversation:" + sessionID)
	trajectoryID := antigravityUUIDFromSeed(fmt.Sprintf("antigravity:trajectory:%s:%s:%s", sessionID, model, requestType))
	step := contentCount*2 - 1
	if step < 1 {
		step = 1
	}
	return fmt.Sprintf("agent/%s/%d/%s/%d", conversationID, time.Now().UnixMilli(), trajectoryID, step)
}

// hardenAntigravityRequest strips blacklisted thinking/reasoning fields Google
// rejects and caps maxOutputTokens at maxAntigravityOutputTokens. It leaves the
// request byte-for-byte intact when nothing needs changing so thought signatures
// on functionCall/thought parts are never re-encoded (which would corrupt them).
func hardenAntigravityRequest(geminiBody []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(geminiBody, &m); err != nil {
		return geminiBody
	}

	changed := false
	for _, k := range antigravityRequestBlacklist {
		if _, ok := m[k]; ok {
			delete(m, k)
			changed = true
		}
	}

	gc, _ := m["generationConfig"].(map[string]any)
	if v, ok := gc["maxOutputTokens"].(float64); ok && v > maxAntigravityOutputTokens {
		gc["maxOutputTokens"] = float64(maxAntigravityOutputTokens)
		changed = true
	}

	if !changed {
		return geminiBody
	}
	out, err := json.Marshal(m)
	if err != nil {
		return geminiBody
	}
	return out
}

// StripThoughtSignatures removes all thoughtSignature / thought_signature keys
// from a JSON body (Antigravity wrapper or plain Gemini). Used as a fallback
// when the backend rejects a history signature as corrupted — stripping lets
// the model treat the turn as a fresh thought.
func StripThoughtSignatures(body []byte) []byte {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body
	}
	stripThoughtSignaturesRecursive(v)
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

func stripThoughtSignaturesRecursive(v any) {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "thoughtSignature")
		delete(x, "thought_signature")
		for _, val := range x {
			stripThoughtSignaturesRecursive(val)
		}
	case []any:
		for _, elem := range x {
			stripThoughtSignaturesRecursive(elem)
		}
	}
}

// ReplaceThoughtSignatures replaces every thoughtSignature / thought_signature
// value with the given replacement (typically DefaultThinkingSignature). Used
// when the backend rejects a history signature as corrupted/invalid — the
// default is a known-valid signature for the Antigravity backend.
func ReplaceThoughtSignatures(body []byte, replacement string) []byte {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return body
	}
	replaceThoughtSignaturesRecursive(v, replacement)
	out, err := json.Marshal(v)
	if err != nil {
		return body
	}
	return out
}

func replaceThoughtSignaturesRecursive(v any, replacement string) {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["thoughtSignature"]; ok {
			x["thoughtSignature"] = replacement
		}
		if _, ok := x["thought_signature"]; ok {
			x["thought_signature"] = replacement
		}
		for _, val := range x {
			replaceThoughtSignaturesRecursive(val, replacement)
		}
	case []any:
		for _, elem := range x {
			replaceThoughtSignaturesRecursive(elem, replacement)
		}
	}
}

// fixAntigravityContents mirrors the Next.js Antigravity executor's content
// fixup: role correction for functionResponse, stripping of thought-only parts
// that Gemini 3 rejects, and backfilling of thoughtSignature on functionCall
// parts (parity with open-sse/executors/antigravity.js).
func fixAntigravityContents(req *GeminiRequest) bool {
	if req == nil || len(req.Contents) == 0 {
		return false
	}
	changed := false
	fixed := make([]GeminiContent, len(req.Contents))
	for i, c := range req.Contents {
		role := c.Role
		hasFunctionResponse := false
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				hasFunctionResponse = true
				break
			}
		}
		if hasFunctionResponse {
			role = "user"
		}

		filtered := make([]GeminiPart, 0, len(c.Parts))
		for _, p := range c.Parts {
			if p.Thought != nil && *p.Thought && p.FunctionCall == nil {
				continue
			}
			if p.ThoughtSignature != "" && p.FunctionCall == nil && p.Text == "" {
				continue
			}
			filtered = append(filtered, p)
		}

		needsBackfill := false
		for _, p := range filtered {
			if p.FunctionCall != nil && p.ThoughtSignature == "" {
				needsBackfill = true
				break
			}
		}

		if needsBackfill {
			for idx, p := range filtered {
				if p.FunctionCall != nil && p.ThoughtSignature == "" {
					filtered[idx].ThoughtSignature = DefaultThinkingSignature
				}
			}
		}

		if role != c.Role || len(filtered) != len(c.Parts) || needsBackfill {
			changed = true
		}
		fixed[i] = GeminiContent{Role: role, Parts: filtered}
	}
	if changed {
		req.Contents = fixed
	}
	return changed
}

// WrapForAntigravity wraps a standard Gemini request in Antigravity API envelope.
func WrapForAntigravity(geminiBody []byte, projectID, modelName string) ([]byte, error) {
	modelName = NormalizeAntigravityModel(modelName)

	contentCount := 1
	var geminiReq GeminiRequest
	if err := json.Unmarshal(geminiBody, &geminiReq); err == nil {
		contentCount = len(geminiReq.Contents)
		fixAntigravityContents(&geminiReq)
		cleanedReq := StripCompetitivePrompts(&geminiReq)
		if len(cleanedReq.Tools) > 0 {
			cloaked, _ := CloakAntigravityRequest(cleanedReq, "")
			cleanedReq = cloaked
		}
		if cloakedBytes, err := json.Marshal(cleanedReq); err == nil {
			geminiBody = cloakedBytes
		}
	}
	geminiBody = hardenAntigravityRequest(geminiBody)

	wrapper := AntigravityRequest{
		Project:     projectID,
		Model:       modelName,
		UserAgent:   "antigravity",
		RequestType: "agent",
		RequestID:   antigravityBuildRequestID(projectID, modelName, "agent", contentCount),
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
		RequestID:   antigravityBuildRequestID(projectID, cleanModel, "image_gen", 1),
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
