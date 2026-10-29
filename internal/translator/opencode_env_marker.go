package translator

import (
	json "encoding/json/v2"
	"regexp"
	"strings"
)

// OpenCodeEnvMarker is the mandatory environment block OpenCode v2 free-tier
// checks in agent system prompts. The paths mirror the captured CLI harness
// preamble (opencode_free_harness.go) — the only env shape measured to pass
// the gate — so the marker and the preamble never disagree.
const OpenCodeEnvMarker = "<env>\n  Working directory: /tmp/ocprobe\n  Workspace root folder: /tmp/ocprobe\n</env>"

var (
	envBlockRegex   = regexp.MustCompile(`(?s)<env>(.*?)</env>`)
	envWorkdirRegex = regexp.MustCompile(`(?i)Working directory:[^\n]*`)
	envRootRegex    = regexp.MustCompile(`(?i)Workspace root folder:[^\n]*`)
)

func hasCompleteEnvBlock(val string) bool {
	match := envBlockRegex.FindStringSubmatch(val)
	if len(match) < 2 {
		return false
	}
	inner := match[1]
	return envWorkdirRegex.MatchString(inner) && envRootRegex.MatchString(inner)
}

func upgradeEnvBlock(text string) string {
	return envBlockRegex.ReplaceAllStringFunc(text, func(block string) string {
		match := envBlockRegex.FindStringSubmatch(block)
		if len(match) < 2 {
			return block
		}
		inner := match[1]
		hasWorkdir := envWorkdirRegex.MatchString(inner)
		hasRoot := envRootRegex.MatchString(inner)
		if hasWorkdir && hasRoot {
			return block
		}

		var upgraded string
		if !hasWorkdir && !hasRoot {
			upgraded = "\n  Working directory: /tmp/ocprobe\n  Workspace root folder: /tmp/ocprobe\n"
		} else if !hasRoot {
			if envWorkdirRegex.MatchString(inner) {
				upgraded = envWorkdirRegex.ReplaceAllStringFunc(inner, func(line string) string {
					return line + "\n  Workspace root folder: /tmp/ocprobe"
				})
			} else {
				upgraded = strings.TrimRight(inner, " \t\r\n") + "\n  Workspace root folder: /tmp/ocprobe\n"
			}
		} else if !hasWorkdir {
			if envRootRegex.MatchString(inner) {
				upgraded = envRootRegex.ReplaceAllStringFunc(inner, func(line string) string {
					return "  Working directory: /tmp/ocprobe\n" + line
				})
			} else {
				upgraded = "\n  Working directory: /tmp/ocprobe\n" + inner
			}
		}
		return "<env>" + upgraded + "</env>"
	})
}

// WithEnvMarker ensures a system prompt string contains the OpenCode environment block.
func WithEnvMarker(value string) string {
	if hasCompleteEnvBlock(value) {
		return value
	}
	if envBlockRegex.MatchString(value) {
		return upgradeEnvBlock(value)
	}
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		return trimmed + "\n\n" + OpenCodeEnvMarker
	}
	return OpenCodeEnvMarker
}

// ApplyFingerprintSystemPrompt ensures the OpenCode v2 <env> marker is injected into
// the system prompt, instructions, or first system message of the request body.
func ApplyFingerprintSystemPrompt(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	// 1. Anthropic / Claude Messages system property
	if sys, ok := req["system"]; ok {
		switch s := sys.(type) {
		case string:
			req["system"] = WithEnvMarker(s)
		case []any:
			marked := false
			for _, item := range s {
				if str, ok := item.(string); ok && hasCompleteEnvBlock(str) {
					marked = true
					break
				}
				if m, ok := item.(map[string]any); ok {
					if text, ok := m["text"].(string); ok && hasCompleteEnvBlock(text) {
						marked = true
						break
					}
				}
			}
			if !marked {
				upgradedAny := false
				newSys := make([]any, 0, len(s)+1)
				for _, item := range s {
					if str, ok := item.(string); ok && envBlockRegex.MatchString(str) {
						upgradedAny = true
						newSys = append(newSys, upgradeEnvBlock(str))
					} else if m, ok := item.(map[string]any); ok {
						if text, ok := m["text"].(string); ok && envBlockRegex.MatchString(text) {
							upgradedAny = true
							copied := make(map[string]any, len(m))
							for k, v := range m {
								copied[k] = v
							}
							copied["text"] = upgradeEnvBlock(text)
							newSys = append(newSys, copied)
						} else {
							newSys = append(newSys, item)
						}
					} else {
						newSys = append(newSys, item)
					}
				}
				if upgradedAny {
					req["system"] = newSys
				} else {
					req["system"] = append(s, map[string]any{"type": "text", "text": OpenCodeEnvMarker})
				}
			}
		}
		if b, err := json.Marshal(req, json.Deterministic(true)); err == nil {
			return b
		}
		return body
	}

	// 2. Responses API instructions property
	if inst, ok := req["instructions"].(string); ok {
		req["instructions"] = WithEnvMarker(inst)
		if b, err := json.Marshal(req, json.Deterministic(true)); err == nil {
			return b
		}
		return body
	}

	// 3. OpenAI Chat Completions messages property
	if messages, ok := req["messages"].([]any); ok && len(messages) > 0 {
		first, ok := messages[0].(map[string]any)
		if ok && first["role"] == "system" {
			if content, ok := first["content"].(string); ok {
				firstCopy := make(map[string]any, len(first))
				for k, v := range first {
					firstCopy[k] = v
				}
				firstCopy["content"] = WithEnvMarker(content)
				messages[0] = firstCopy
				req["messages"] = messages
			} else if contentArr, ok := first["content"].([]any); ok {
				marked := false
				for _, part := range contentArr {
					if pm, ok := part.(map[string]any); ok {
						if text, ok := pm["text"].(string); ok && hasCompleteEnvBlock(text) {
							marked = true
							break
						}
					}
				}
				if !marked {
					upgradedAny := false
					newContent := make([]any, 0, len(contentArr)+1)
					for _, part := range contentArr {
						if pm, ok := part.(map[string]any); ok {
							if text, ok := pm["text"].(string); ok && envBlockRegex.MatchString(text) {
								upgradedAny = true
								pmCopy := make(map[string]any, len(pm))
								for k, v := range pm {
									pmCopy[k] = v
								}
								pmCopy["text"] = upgradeEnvBlock(text)
								newContent = append(newContent, pmCopy)
							} else {
								newContent = append(newContent, part)
							}
						} else {
							newContent = append(newContent, part)
						}
					}
					firstCopy := make(map[string]any, len(first))
					for k, v := range first {
						firstCopy[k] = v
					}
					if upgradedAny {
						firstCopy["content"] = newContent
					} else {
						firstCopy["content"] = append(contentArr, map[string]any{"type": "text", "text": OpenCodeEnvMarker})
					}
					messages[0] = firstCopy
					req["messages"] = messages
				}
			}
		} else {
			// No system message at all: prepend one with OpenCodeEnvMarker
			newSysMsg := map[string]any{
				"role":    "system",
				"content": OpenCodeEnvMarker,
			}
			newMessages := make([]any, 0, len(messages)+1)
			newMessages = append(newMessages, newSysMsg)
			newMessages = append(newMessages, messages...)
			req["messages"] = newMessages
		}
		if b, err := json.Marshal(req, json.Deterministic(true)); err == nil {
			return b
		}
	}

	return body
}
