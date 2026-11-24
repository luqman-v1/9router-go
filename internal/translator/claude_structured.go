package translator

import (
	"bytes"
	"encoding/json"
)

// StructuredOutputTool is the forced tool that carries a JSON schema to
// Claude-dialect upstreams when an OpenAI client asks for structured output via
// response_format. The name is reserved by the gateway: the Claude→OpenAI
// response converters rewrite any tool call with this name back to plain text,
// so the client never sees the mechanism. A client that declares a tool with
// this exact name gets the system-prompt fallback instead, so its own tool
// stays callable (parity with KeiRouter transform/structured_output.go, which
// uses the same reserved-name gate).
const StructuredOutputTool = "9router_structured_output"

// claudeResponseFormat is the canonical OpenAI structured-output request:
// {"type":"json_object"} or {"type":"json_schema","json_schema":{name,schema,strict}}.
type claudeResponseFormat struct {
	Type       string `json:"type"`
	JSONSchema *struct {
		Name   string          `json:"name,omitempty"`
		Schema json.RawMessage `json:"schema,omitempty"`
	} `json:"json_schema,omitempty"`
}

// decodeClaudeResponseFormat reads an OpenAI response_format value into the
// canonical form. It reports false when the value is absent, unparseable, or a
// type this gateway does not carry (e.g. a provider-specific shape).
func decodeClaudeResponseFormat(raw any) (claudeResponseFormat, bool) {
	switch v := raw.(type) {
	case string:
		if v == "json_object" {
			return claudeResponseFormat{Type: "json_object"}, true
		}
		return claudeResponseFormat{}, false
	case []byte:
		return parseClaudeResponseFormatBytes(v)
	case json.RawMessage:
		return parseClaudeResponseFormatBytes(v)
	default:
		b, err := json.Marshal(raw)
		if err != nil {
			return claudeResponseFormat{}, false
		}
		return parseClaudeResponseFormatBytes(b)
	}
}

func parseClaudeResponseFormatBytes(b []byte) (claudeResponseFormat, bool) {
	if len(bytes.TrimSpace(b)) == 0 {
		return claudeResponseFormat{}, false
	}
	var rf claudeResponseFormat
	if json.Unmarshal(b, &rf) != nil {
		return rf, false
	}
	return rf, rf.Type == "json_object" || rf.Type == "json_schema"
}

// schema returns the JSON schema for a json_schema format, or nil for plain
// JSON mode.
func (rf claudeResponseFormat) schema() json.RawMessage {
	if rf.Type != "json_schema" || rf.JSONSchema == nil || len(rf.JSONSchema.Schema) == 0 {
		return nil
	}
	return rf.JSONSchema.Schema
}

// structuredOutputInstruction asks for JSON in the system prompt, for the cases
// where a forced tool cannot carry the schema (the client declared its own
// tools, thinking is on and would reject a pinned tool_choice, or the reserved
// tool name is taken).
func structuredOutputInstruction(rf claudeResponseFormat) string {
	if s := rf.schema(); s != nil {
		return "Respond with a single JSON value that matches this JSON schema. Output only the JSON, without prose or code fences.\n\n" + string(s)
	}
	return "Respond with a single valid JSON object. Output only the JSON, without prose or code fences."
}

// claudeThinkingEnabled reports whether the converted request has extended
// thinking on. A pinned tool_choice is rejected while thinking is enabled, so
// the structured-output carry must fall back to a system-prompt instruction.
func claudeThinkingEnabled(reqMap map[string]any) bool {
	thinking, ok := reqMap["thinking"].(map[string]any)
	if !ok {
		return false
	}
	t, _ := thinking["type"].(string)
	return t == "enabled" || t == "adaptive"
}

func hasClaudeToolsMap(reqMap map[string]any) bool {
	tools, ok := reqMap["tools"].([]any)
	return ok && len(tools) > 0
}

// declaresStructuredOutputTool reports whether the client already declares a
// tool with the reserved gateway name.
func declaresStructuredOutputTool(reqMap map[string]any) bool {
	tools, ok := reqMap["tools"].([]any)
	if !ok {
		return false
	}
	for _, tRaw := range tools {
		t, ok := tRaw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := t["name"].(string); name == StructuredOutputTool {
			return true
		}
	}
	return false
}

// CarryClaudeStructuredOutput ports an OpenAI response_format onto a Claude
// Messages request built in place. Without it the field is dropped on
// conversion (it is not a Claude field) and a client asking for JSON gets free
// text silently.
//
// A json_schema with no client tools, no thinking, and a free reserved name
// becomes a forced structured-output tool — the schema as its input_schema,
// tool_choice pinned to it — so the model is constrained to the schema.
// Otherwise the ask is carried as a system-prompt instruction. The OpenAI-only
// response_format key is removed in every case so the strict Claude API does
// not 400 on it.
// Parity with KeiRouter transform/structured_output.go + transform/anthropic.go.
func CarryClaudeStructuredOutput(reqMap map[string]any) {
	raw, ok := reqMap["response_format"]
	if !ok || raw == nil {
		return
	}
	rf, decoded := decodeClaudeResponseFormat(raw)
	if !decoded {
		// Not a structured format this gateway carries: drop the OpenAI-only
		// field so the strict Claude API does not reject the request.
		delete(reqMap, "response_format")
		return
	}
	schema := rf.schema()
	if schema != nil && !hasClaudeToolsMap(reqMap) && !claudeThinkingEnabled(reqMap) &&
		!declaresStructuredOutputTool(reqMap) {
		tools, _ := reqMap["tools"].([]any)
		tools = append(tools, map[string]any{
			"name":         StructuredOutputTool,
			"description":  "Return the final answer as this JSON object.",
			"input_schema": schema,
		})
		reqMap["tools"] = tools
		reqMap["tool_choice"] = map[string]any{"type": "tool", "name": StructuredOutputTool}
	} else {
		appendClaudeSystemText(reqMap, structuredOutputInstruction(rf))
	}
	delete(reqMap, "response_format")
}
