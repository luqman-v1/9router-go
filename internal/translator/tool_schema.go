package translator

import (
	"regexp"
)

// isValidRegex reports whether pattern is a valid Go regexp (proxy for JS RegExp validity).
func isValidRegex(pattern string) bool {
	_, err := regexp.Compile(pattern)
	return err == nil
}

// stripPatterns recursively strips invalid pattern regex constraints from a JSON Schema node.
// Properties is special-cased because its keys are arbitrary property names.
func stripPatterns(schema any, removed *int) any {
	switch v := schema.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = stripPatterns(item, removed)
		}
		return out
	case map[string]any:
		cleaned := make(map[string]any, len(v))
		for key, value := range v {
			if key == "pattern" {
				if s, ok := value.(string); ok {
					if isValidRegex(s) {
						cleaned[key] = value
					} else {
						*removed++
					}
					continue
				}
			}
			if key == "properties" {
				if props, ok := value.(map[string]any); ok {
					newProps := make(map[string]any, len(props))
					for propName, propSchema := range props {
						newProps[propName] = stripPatterns(propSchema, removed)
					}
					cleaned[key] = newProps
					continue
				}
				// Fall through for non-map properties (should not happen)
			}
			cleaned[key] = stripPatterns(value, removed)
		}
		return cleaned
	default:
		return schema
	}
}

// NormalizeToolSchemasForProvider strips invalid pattern constraints for OpenRouter.
// Only provider == "openrouter" is affected; other providers return tools unchanged.
// Port of open-sse/utils/toolSchemaCompatibility.js normalizeToolSchemasForProvider (PR #3665).
func NormalizeToolSchemasForProvider(provider string, tools []any) ([]any, int) {
	if provider != "openrouter" || len(tools) == 0 {
		return tools, 0
	}
	removed := 0
	normalized := make([]any, len(tools))
	for i, tool := range tools {
		toolMap, ok := tool.(map[string]any)
		if !ok {
			normalized[i] = tool
			continue
		}
		// Deep copy tool map
		newTool := make(map[string]any, len(toolMap))
		for k, v := range toolMap {
			newTool[k] = v
		}
		if fn, ok := toolMap["function"].(map[string]any); ok {
			newFn := make(map[string]any, len(fn))
			for k, v := range fn {
				newFn[k] = v
			}
			if params, ok := fn["parameters"]; ok {
				newFn["parameters"] = stripPatterns(params, &removed)
			}
			newTool["function"] = newFn
		}
		normalized[i] = newTool
	}
	return normalized, removed
}
