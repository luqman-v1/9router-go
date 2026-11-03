package translator

import (
	"strings"
)

// applyTools rewrites the Responses tool declarations into Chat function
// declarations, plus any additional_tools the input carried.
func (c *responsesRequestConv) applyTools(envelope map[string]any) {
	declared, _ := envelope["tools"].([]any)
	all := make([]any, 0, len(declared)+len(c.additionalTools))
	all = append(all, declared...)
	for _, t := range c.additionalTools {
		all = append(all, t)
	}
	if len(all) == 0 {
		return
	}

	converted := make([]any, 0, len(all))
	for _, raw := range all {
		c.unpackTool(raw, &converted)
	}
	envelope["tools"] = converted
}

func (c *responsesRequestConv) unpackTool(raw any, out *[]any) {
	tool, ok := raw.(map[string]any)
	if !ok {
		return
	}
	toolType, _ := tool["type"].(string)
	if toolType == "namespace" {
		for _, key := range []string{"tools", "functions", "items"} {
			if subList, ok := tool[key].([]any); ok {
				for _, sub := range subList {
					c.unpackTool(sub, out)
				}
			}
		}
		return
	}
	if decl := convertResponsesTool(tool, c); decl != nil {
		*out = append(*out, decl)
	}
}

// convertResponsesTool maps one Responses tool declaration to a Chat function
// declaration, returning nil for hosted tools that carry no name and so cannot
// be expressed as a function. Sending those through reaches providers such as
// Gemini, which validate function names strictly.
func convertResponsesTool(tool map[string]any, c *responsesRequestConv) map[string]any {
	// Already in Chat Completions form: { type:"function", function:{...} }.
	if _, ok := tool["function"]; ok {
		return tool
	}

	name, _ := tool["name"].(string)
	name = strings.TrimPrefix(name, "functions.")
	if strings.TrimSpace(name) == "" {
		return nil
	}

	// Chat Completions has no freeform custom-tool declaration, so a custom tool
	// is exposed as a function taking one raw `input` string. Its name is kept in
	// translator-only metadata so the response conversion can recognise it again.
	if toolType, _ := tool["type"].(string); toolType == responsesToolTypeCustom {
		c.markCustomTool(name)
		format, _ := tool["format"].(map[string]any)
		syntax, _ := format["syntax"].(string)
		definition, _ := format["definition"].(string)
		formatHint := strings.Join(nonEmpty(syntax, definition), "\n")
		description, _ := tool["description"].(string)

		return functionDeclaration(name, strings.Join(nonEmpty(description, formatHint), "\n\n"), map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": "Raw freeform input for this custom tool",
				},
			},
			"required":             []any{"input"},
			"additionalProperties": false,
		}, nil)
	}

	description, _ := tool["description"].(string)
	return functionDeclaration(name, description, normalizeToolParameters(tool["parameters"]), tool["strict"])
}

func functionDeclaration(name, description string, parameters map[string]any, strict any) map[string]any {
	fn := map[string]any{
		"name":       name,
		"parameters": parameters,
	}
	if description != "" {
		fn["description"] = description
	}
	if strict != nil {
		fn["strict"] = strict
	}
	return map[string]any{"type": openaiBlockFunction, "function": fn}
}

// normalizeToolParameters guarantees an object schema always carries a
// properties field, which the Codex Responses API requires.
func normalizeToolParameters(raw any) map[string]any {
	params, ok := raw.(map[string]any)
	if !ok || params == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if schemaType, _ := params["type"].(string); schemaType == "object" {
		if _, has := params["properties"]; !has {
			params["properties"] = map[string]any{}
		}
	}
	return params
}
