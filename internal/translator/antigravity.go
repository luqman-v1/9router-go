package translator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"9router/proxy/internal/log"
)

// AntigravityRequest is the wrapper format for Antigravity API.
type AntigravityRequest struct {
	Project     string          `json:"project"`
	Model       string          `json:"model"`
	UserAgent   string          `json:"userAgent"`
	RequestType string          `json:"requestType"`
	RequestID   string          `json:"requestId"`
	Request     json.RawMessage `json:"request"`
}

// AntigravityNativeToolNames are tool names preserved without suffix.
var AntigravityNativeToolNames = map[string]bool{
	"browser_subagent":                           true,
	"command_status":                             true,
	"find_by_name":                               true,
	"generate_image":                             true,
	"grep_search":                                true,
	"list_dir":                                   true,
	"list_resources":                             true,
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

// AntigravityDecoyTools are the 21 decoy tools matching official IDE defaults.
var AntigravityDecoyTools = []GeminiFunctionDecl{
	{Name: "browser_subagent", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "command_status", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "find_by_name", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "generate_image", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "grep_search", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "list_dir", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "list_resources", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "mcp_sequential-thinking_sequentialthinking", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "multi_replace_file_content", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "notify_user", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "read_resource", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "read_terminal", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "read_url_content", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "replace_file_content", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "run_command", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "search_web", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "send_command_input", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "task_boundary", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "view_content_chunk", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "view_file", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
	{Name: "write_to_file", Description: "This tool is currently unavailable.", Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}},
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
	if strings.HasSuffix(name, "_ide") {
		return strings.TrimSuffix(name, "_ide")
	}
	return name
}

// AntigravityModelSynonyms maps client/UI model names to internal Google Antigravity backend model IDs.
var AntigravityModelSynonyms = map[string]string{
	"gemini-default":             "gemini-3.5-flash-low",
	"gemini-3.5-flash-high":      "gemini-3-flash-agent",
	"gemini-3.5-flash-medium":    "gemini-3.5-flash-low",
	"gemini-3.5-flash-extra-low": "gemini-3.5-flash-extra-low",
	"gemini-3.1-pro-high":        "gemini-pro-agent",
	"gemini-3-pro-high":          "gemini-pro-agent",
	"gemini-3-pro-low":           "gemini-3.1-pro-low",
}

// NormalizeAntigravityModel maps known aliases/synonyms to Antigravity internal backend model names.
func NormalizeAntigravityModel(model string) string {
	m := strings.ToLower(model)
	if canonical, ok := AntigravityModelSynonyms[m]; ok {
		return canonical
	}
	return model
}

// WrapForAntigravity wraps a standard Gemini request in Antigravity API envelope.
func WrapForAntigravity(geminiBody []byte, projectID, modelName string) ([]byte, error) {
	modelName = NormalizeAntigravityModel(modelName)

	var geminiReq GeminiRequest
	if err := json.Unmarshal(geminiBody, &geminiReq); err == nil && len(geminiReq.Tools) > 0 {
		cloaked, _ := CloakAntigravityRequest(&geminiReq, "")
		if cloakedBytes, err := json.Marshal(cloaked); err == nil {
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
		Response json.RawMessage `json:"response"`
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
	dashIdx := strings.LastIndex(model, "-")
	if dashIdx != -1 && dashIdx < len(model)-1 {
		suffix := model[dashIdx+1:]
		if xIdx := strings.Index(suffix, "x"); xIdx != -1 {
			var w, h int
			if _, err := fmt.Sscanf(suffix, "%dx%d", &w, &h); err == nil && w > 0 && h > 0 {
				cleanModel = model[:dashIdx]
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


