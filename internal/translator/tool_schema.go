package translator

import (
	"regexp"
	"strings"
)

func hasLookaround(p string) bool {
	return strings.Contains(p, "(?=") || strings.Contains(p, "(?!") || strings.Contains(p, "(?<=") || strings.Contains(p, "(?<!")
}

// isValidRegex reports whether pattern is a valid regex constraint without lookarounds
// (lookarounds are rejected by OpenAI, DeepSeek, and strict JSON Schema validators).
func isValidRegex(pattern string) bool {
	if hasLookaround(pattern) {
		return false
	}
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

// NormalizeToolSchemasForProvider strips invalid pattern constraints (including regex lookarounds
// like (?=...), (?!...), (?<=...), (?<!...) which OpenAI, DeepSeek, and strict JSON Schema validators reject).
func NormalizeToolSchemasForProvider(provider string, tools []any) ([]any, int) {
	if provider == "anthropic" || len(tools) == 0 {
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
			fnName, _ := fn["name"].(string)
			if params, ok := fn["parameters"]; ok {
				p := stripPatterns(params, &removed)
				if fnName == "Agent" {
					p = stripAgentIsolation(p, &removed)
				}
				newFn["parameters"] = p
			}
			newTool["function"] = newFn
		} else if name, _ := toolMap["name"].(string); name == "Agent" {
			if schema, ok := toolMap["input_schema"]; ok {
				newTool["input_schema"] = stripAgentIsolation(schema, &removed)
			}
		}
		normalized[i] = newTool
	}
	return normalized, removed
}

// stripAgentIsolation removes the "isolation" property from the Agent tool schema
// for non-Anthropic providers (Codex, OpenAI, DeepSeek, etc.).
// Models outside Anthropic frequently over-interpret the "worktree" enum and defensive
// instructions, leading to recursive no-op/blocked probing loops.
func stripAgentIsolation(schema any, removed *int) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return schema
	}
	if _, has := props["isolation"]; !has {
		return schema
	}
	newProps := make(map[string]any, len(props)-1)
	for k, v := range props {
		if k != "isolation" {
			newProps[k] = v
		}
	}
	newM := make(map[string]any, len(m))
	for k, v := range m {
		newM[k] = v
	}
	newM["properties"] = newProps
	if removed != nil {
		*removed++
	}
	return newM
}
