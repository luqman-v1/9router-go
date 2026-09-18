package translator

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"strings"
)

// Tool-name handling for the OpenCode Zen free-tier gate.
//
// The upstream gate fingerprints its official client through the case of the
// file-search tool quartet (bash, glob, grep, read) carried in the request body.
// Claude Code CLI sends the same built-in tools capitalised
// (Bash, Glob, Grep, Read), so the fingerprint never matches and the gate
// rejects every request coming from that harness.
//
// Measured directly against the upstream (2026-09-18):
//
//	capitalised only        -> HTTP 403 FreeTierError
//	capitalised + lowercase -> HTTP 500 server_error (upstream sees duplicates)
//	lowercase only          -> HTTP 200
//
// A case-variant therefore has to be RENAMED to the canonical lowercase form and
// not added alongside it: appending "bash" next to the caller's "Bash" yields two
// tools sharing a name and turns the 403 into a 500.
//
// Renaming alone satisfies the gate, but the client must still receive the tool
// name it declared itself, so the helpers below also restore the original
// spelling on the response.
//
// Ported from the equivalent fix in decolua/9router (opencodeFingerprint.js).

// OpenCodeFingerprintTools holds the canonical names the upstream gate looks for.
var OpenCodeFingerprintTools = []string{"bash", "glob", "grep", "read"}

// FingerprintToolKey returns the canonical lowercase name when name is a quartet
// member, or "" when it is not.
func FingerprintToolKey(name string) string {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, t := range OpenCodeFingerprintTools {
		if lower == t {
			return t
		}
	}
	return ""
}

// toolNameOf reads a tool name from either shape: "name" at the top level
// (Responses shape) or nested under "function" (Chat Completions shape).
func toolNameOf(tool any) string {
	m, ok := tool.(map[string]any)
	if !ok {
		return ""
	}
	if n, ok := m["name"].(string); ok && strings.TrimSpace(n) != "" {
		return strings.TrimSpace(n)
	}
	if fn, ok := m["function"].(map[string]any); ok {
		if n, ok := fn["name"].(string); ok {
			return strings.TrimSpace(n)
		}
	}
	return ""
}

// setToolName writes name back into whichever shape the tool uses.
func setToolName(tool any, name string) {
	m, ok := tool.(map[string]any)
	if !ok {
		return
	}
	if _, hasTop := m["name"]; hasTop {
		m["name"] = name
		return
	}
	if fn, ok := m["function"].(map[string]any); ok {
		fn["name"] = name
	}
}

// ConcealFingerprintTools renames quartet case-variants to the canonical
// lowercase form, drops duplicate names (the upstream rejects a body carrying two
// tools with the same name), and appends the quartet members that are genuinely
// absent as no-op declarations.
//
// It returns the resulting body together with a map of sent-name -> original-name
// for the response side. When nothing needed to change, the original body is
// returned unchanged with an empty map.
func ConcealFingerprintTools(body []byte) ([]byte, map[string]string) {
	noop := map[string]string{}
	if len(body) == 0 {
		return body, noop
	}

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, noop
	}

	// A body with no tools array still has to receive the quartet: the gate
	// rejects tool-less requests too (measured 403, 2026-09-18).
	var tools []any
	if raw, ok := m["tools"].([]any); ok {
		tools = raw
	}

	toolNameMap := map[string]string{}
	seen := map[string]bool{}
	kept := make([]any, 0, len(tools)+len(OpenCodeFingerprintTools))

	for _, tool := range tools {
		current := toolNameOf(tool)
		if current == "" {
			kept = append(kept, tool)
			continue
		}
		key := FingerprintToolKey(current)
		finalName := current
		if key != "" {
			finalName = key
		}
		dedupeKey := strings.ToLower(finalName)
		if seen[dedupeKey] {
			continue
		}
		seen[dedupeKey] = true
		if key != "" && key != current {
			toolNameMap[key] = current
			setToolName(tool, key)
		}
		kept = append(kept, tool)
	}

	// Injected declarations mirror the shape already in use: the Responses API
	// puts "name" at the top level, Chat Completions wraps it in "function".
	flat := true
	for _, tool := range kept {
		if tm, ok := tool.(map[string]any); ok {
			if _, has := tm["function"]; has {
				flat = false
			}
			break
		}
	}

	for _, name := range OpenCodeFingerprintTools {
		if seen[name] {
			continue
		}
		entry := map[string]any{
			"type":        "function",
			"description": "OpenCode built-in " + name + " tool",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		}
		if flat {
			entry["name"] = name
		} else {
			entry["function"] = map[string]any{
				"name":        name,
				"description": "OpenCode built-in " + name + " tool",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			}
		}
		kept = append(kept, entry)
		seen[name] = true
	}
	m["tools"] = kept

	// Point an explicit tool_choice at the renamed tool.
	if choice, ok := m["tool_choice"].(map[string]any); ok {
		if n, ok := choice["name"].(string); ok {
			if key := FingerprintToolKey(n); key != "" && toolNameMap[key] != "" {
				choice["name"] = key
			}
		}
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body, noop
	}
	return out, toolNameMap
}

// RestoreToolNames restores the caller's original tool names on a JSON response
// payload. It covers Claude content_block_start (tool_use), Chat Completions
// choices[].{delta,message}.tool_calls[].function.name, and Responses output[]
// items of type function_call / custom_tool_call.
func RestoreToolNames(payload []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(payload) == 0 {
		return payload
	}

	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return payload
	}

	changed := false

	// Claude: content_block_start with content_block.type == "tool_use"
	if block, ok := m["content_block"].(map[string]any); ok && block["type"] == "tool_use" {
		if n, ok := block["name"].(string); ok {
			if orig, found := toolNameMap[n]; found {
				block["name"] = orig
				changed = true
			}
		}
	}

	// OpenAI: choices[].delta.tool_calls[] and choices[].message.tool_calls[]
	if choices, ok := m["choices"].([]any); ok {
		for _, c := range choices {
			choice, ok := c.(map[string]any)
			if !ok {
				continue
			}
			for _, holder := range []string{"delta", "message"} {
				h, ok := choice[holder].(map[string]any)
				if !ok {
					continue
				}
				calls, ok := h["tool_calls"].([]any)
				if !ok {
					continue
				}
				for _, call := range calls {
					cm, ok := call.(map[string]any)
					if !ok {
						continue
					}
					fn, ok := cm["function"].(map[string]any)
					if !ok {
						continue
					}
					n, ok := fn["name"].(string)
					if !ok || n == "" {
						continue
					}
					if orig, found := toolNameMap[n]; found {
						fn["name"] = orig
						changed = true
					}
				}
			}
		}
	}

	// Responses: output[] items of type function_call / custom_tool_call
	if output, ok := m["output"].([]any); ok {
		for _, item := range output {
			im, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t, _ := im["type"].(string)
			if t != "function_call" && t != "custom_tool_call" {
				continue
			}
			if n, ok := im["name"].(string); ok {
				if orig, found := toolNameMap[n]; found {
					im["name"] = orig
					changed = true
				}
			}
		}
	}

	if !changed {
		return payload
	}
	out, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return out
}

// RestoreToolNamesInSSE applies RestoreToolNames to an SSE frame while keeping
// the "data: " prefix and the original line framing intact. Lines that are not
// JSON data (for example "event: ..." or "[DONE]") pass through untouched.
func RestoreToolNamesInSSE(chunk []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(chunk) == 0 {
		return chunk
	}
	if !bytes.Contains(chunk, []byte("data:")) {
		return chunk
	}

	var out bytes.Buffer
	out.Grow(len(chunk))
	remaining := chunk
	changedAny := false

	for len(remaining) > 0 {
		nl := bytes.IndexByte(remaining, '\n')
		var line []byte
		if nl < 0 {
			line = remaining
			remaining = nil
		} else {
			line = remaining[:nl+1]
			remaining = remaining[nl+1:]
		}

		body := bytes.TrimRight(line, "\r\n")
		if bytes.HasPrefix(body, []byte("data:")) {
			jsonPart := bytes.TrimSpace(body[len("data:"):])
			if len(jsonPart) > 0 && !bytes.Equal(jsonPart, []byte("[DONE]")) {
				restored := RestoreToolNames(jsonPart, toolNameMap)
				if !bytes.Equal(restored, jsonPart) {
					out.WriteString("data: ")
					out.Write(restored)
					out.WriteByte('\n')
					changedAny = true
					continue
				}
			}
		}
		out.Write(line)
	}

	if !changedAny {
		return chunk
	}
	return out.Bytes()
}

// RestoreToolNamesInPayload picks the right shape: an SSE frame when the chunk
// carries "data:" lines, otherwise a single JSON body for a non-streaming
// response.
func RestoreToolNamesInPayload(chunk []byte, toolNameMap map[string]string) []byte {
	if len(toolNameMap) == 0 || len(chunk) == 0 {
		return chunk
	}
	if bytes.Contains(chunk, []byte("data:")) {
		return RestoreToolNamesInSSE(chunk, toolNameMap)
	}
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return chunk
	}
	return RestoreToolNames(trimmed, toolNameMap)
}

// contextKeyToolNameMap is the context key holding the rename map. Response paths
// only receive a context (not the Request), so the map travels there.
type contextKeyToolNameMap struct{}

// WithToolNameMap stores the renamed-tool map on the context.
func WithToolNameMap(ctx context.Context, toolNameMap map[string]string) context.Context {
	if len(toolNameMap) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKeyToolNameMap{}, toolNameMap)
}

// ToolNameMapFromContext reads back the map stored by WithToolNameMap.
func ToolNameMapFromContext(ctx context.Context) map[string]string {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(contextKeyToolNameMap{}).(map[string]string)
	return m
}
