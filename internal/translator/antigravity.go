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

// WrapForAntigravity wraps a standard Gemini request in Antigravity API envelope.
func WrapForAntigravity(geminiBody []byte, projectID, modelName string) ([]byte, error) {
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

