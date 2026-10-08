package translator

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
)

// MaxToolNameLength is the maximum allowed length for a function/tool name
// across OpenAI and OpenAI-compatible APIs (regex: ^[a-zA-Z0-9_-]{1,64}$).
const MaxToolNameLength = 64

// FitToolNames ensures that every tool name across tool declarations, conversation
// history (assistant tool_calls, function_call, tool/function results, Claude tool_use),
// and tool_choice does not exceed MaxToolNameLength (64 characters).
//
// If any tool name exceeds 64 characters (e.g. MCP namespaced tools like
// `mcp__server_name__action_detail_something`), it is truncated deterministically
// to at most 64 characters (with a unique `_1`, `_2` suffix to avoid collisions).
//
// It returns the transformed request body and a toolNameMap mapping:
//
//	shortenedName -> originalLongName
//
// so the response path can restore the caller's original tool name before returning
// to the client. If no tool names exceed 64 characters, the original body is returned
// unchanged with a nil map.
func FitToolNames(body []byte) ([]byte, map[string]string) {
	if len(body) == 0 {
		return body, nil
	}

	// Quick check: if the body doesn't contain any tool-related tokens, skip unmarshaling.
	if !bytes.Contains(body, []byte(`"tools"`)) &&
		!bytes.Contains(body, []byte(`"functions"`)) &&
		!bytes.Contains(body, []byte(`"tool_calls"`)) &&
		!bytes.Contains(body, []byte(`"function_call"`)) &&
		!bytes.Contains(body, []byte(`"tool_use"`)) &&
		!bytes.Contains(body, []byte(`"functionCall"`)) &&
!bytes.Contains(body, []byte(`"functionResponse"`)) &&
		!bytes.Contains(body, []byte(`"tool_choice"`)) {
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, nil
	}

	toolMap := FitToolNamesInMap(req)
	if len(toolMap) == 0 {
		return body, nil
	}

	newBody, err := json.Marshal(req)
	if err != nil {
		return body, nil
	}
	return newBody, toolMap
}

// FitToolNamesInMap modifies tool names in-place on req and returns the
// shortenedName -> originalLongName mapping. Returns nil when nothing was changed.
func FitToolNamesInMap(req map[string]any) map[string]string {
	if req == nil {
		return nil
	}

	existing, longNames := collectToolNames(req)
	if len(longNames) == 0 {
		return nil
	}

	origToShort, shortToOrig := assignFittedToolNames(longNames, existing)
	replaceToolNamesInMap(req, origToShort)
	return shortToOrig
}

func collectToolNames(req map[string]any) (map[string]bool, []string) {
	existing := make(map[string]bool)
	seenLong := make(map[string]bool)
	var longNames []string

	record := func(n string) {
		if n == "" {
			return
		}
		if len(n) <= MaxToolNameLength {
			existing[n] = true
			return
		}
		if !seenLong[n] {
			seenLong[n] = true
			longNames = append(longNames, n)
		}
	}

	visitTools(req, record)
	visitFunctions(req, record)
	visitMessages(req, record)
	visitContents(req, record)
	visitToolChoice(req, record)
	visitInput(req, record)
	return existing, longNames
}

func assignFittedToolNames(longNames []string, existing map[string]bool) (map[string]string, map[string]string) {
	origToShort := make(map[string]string, len(longNames))
	shortToOrig := make(map[string]string, len(longNames))

	for _, orig := range longNames {
		idx := 1
		for {
			suffix := fmt.Sprintf("_%d", idx)
			maxPrefix := MaxToolNameLength - len(suffix)
			prefix := orig
			if len(prefix) > maxPrefix {
				prefix = prefix[:maxPrefix]
			}
			candidate := prefix + suffix
			if !existing[candidate] && shortToOrig[candidate] == "" {
				shortToOrig[candidate] = orig
				origToShort[orig] = candidate
				existing[candidate] = true
				break
			}
			idx++
		}
	}
	return origToShort, shortToOrig
}

func replaceToolNamesInMap(req map[string]any, origToShort map[string]string) {
	mutate := func(n string) string {
		if s, ok := origToShort[n]; ok {
			return s
		}
		return n
	}

	replaceInTools(req, mutate)
	replaceInFunctions(req, mutate)
	replaceInMessages(req, mutate)
	replaceInContents(req, mutate)
	replaceInToolChoice(req, mutate)
	replaceInInput(req, mutate)
}

func visitTools(req map[string]any, record func(string)) {
	tools, ok := req["tools"].([]any)
	if !ok {
		return
	}
	keep := func(n string) string {
		record(n)
		return n
	}
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if n, ok := tm["name"].(string); ok {
			record(n)
		}
		if fn, ok := tm["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				record(n)
			}
		}
		converseToolSpecName(tm, keep)
		if decls, ok := tm["functionDeclarations"].([]any); ok {
			for _, d := range decls {
				if dm, ok := d.(map[string]any); ok {
					if n, ok := dm["name"].(string); ok {
						record(n)
					}
				}
			}
		}
	}
}

func replaceInTools(req map[string]any, mutate func(string) string) {
	tools, ok := req["tools"].([]any)
	if !ok {
		return
	}
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if n, ok := tm["name"].(string); ok {
			tm["name"] = mutate(n)
		}
		if fn, ok := tm["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				fn["name"] = mutate(n)
			}
		}
		converseToolSpecName(tm, mutate)
		if decls, ok := tm["functionDeclarations"].([]any); ok {
			for _, d := range decls {
				if dm, ok := d.(map[string]any); ok {
					if n, ok := dm["name"].(string); ok {
						dm["name"] = mutate(n)
					}
				}
			}
		}
	}
}

func visitFunctions(req map[string]any, record func(string)) {
	functions, ok := req["functions"].([]any)
	if !ok {
		return
	}
	for _, f := range functions {
		if fm, ok := f.(map[string]any); ok {
			if n, ok := fm["name"].(string); ok {
				record(n)
			}
		}
	}
}

func replaceInFunctions(req map[string]any, mutate func(string) string) {
	functions, ok := req["functions"].([]any)
	if !ok {
		return
	}
	for _, f := range functions {
		if fm, ok := f.(map[string]any); ok {
			if n, ok := fm["name"].(string); ok {
				fm["name"] = mutate(n)
			}
		}
	}
}

func visitMessages(req map[string]any, record func(string)) {
	messages, ok := req["messages"].([]any)
	if !ok {
		return
	}
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if tc, ok := mm["tool_calls"].([]any); ok {
			for _, call := range tc {
				if cm, ok := call.(map[string]any); ok {
					if fn, ok := cm["function"].(map[string]any); ok {
						if n, ok := fn["name"].(string); ok {
							record(n)
						}
					}
				}
			}
		}
		if fc, ok := mm["function_call"].(map[string]any); ok {
			if n, ok := fc["name"].(string); ok {
				record(n)
			}
		}
		if role, _ := mm["role"].(string); role == "function" || role == "tool" {
			if n, ok := mm["name"].(string); ok {
				record(n)
			}
		}
		if content, ok := mm["content"].([]any); ok {
			for _, b := range content {
				if bm, ok := b.(map[string]any); ok {
					if t, _ := bm["type"].(string); t == "tool_use" {
						if n, ok := bm["name"].(string); ok {
							record(n)
						}
					}
				}
			}
		}
	}
}

func replaceInMessages(req map[string]any, mutate func(string) string) {
	messages, ok := req["messages"].([]any)
	if !ok {
		return
	}
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if tc, ok := mm["tool_calls"].([]any); ok {
			for _, call := range tc {
				if cm, ok := call.(map[string]any); ok {
					if fn, ok := cm["function"].(map[string]any); ok {
						if n, ok := fn["name"].(string); ok {
							fn["name"] = mutate(n)
						}
					}
				}
			}
		}
		if fc, ok := mm["function_call"].(map[string]any); ok {
			if n, ok := fc["name"].(string); ok {
				fc["name"] = mutate(n)
			}
		}
		if role, _ := mm["role"].(string); role == "function" || role == "tool" {
			if n, ok := mm["name"].(string); ok {
				mm["name"] = mutate(n)
			}
		}
		if content, ok := mm["content"].([]any); ok {
			for _, b := range content {
				if bm, ok := b.(map[string]any); ok {
					if t, _ := bm["type"].(string); t == "tool_use" {
						if n, ok := bm["name"].(string); ok {
							bm["name"] = mutate(n)
						}
					}
				}
			}
		}
	}
}

func visitContents(req map[string]any, record func(string)) {
	contents, ok := req["contents"].([]any)
	if !ok {
		return
	}
	for _, c := range contents {
		if cm, ok := c.(map[string]any); ok {
			if parts, ok := cm["parts"].([]any); ok {
				for _, p := range parts {
					if pm, ok := p.(map[string]any); ok {
						if fc, ok := pm["functionCall"].(map[string]any); ok {
							if n, ok := fc["name"].(string); ok {
								record(n)
							}
						}
						if fr, ok := pm["functionResponse"].(map[string]any); ok {
							if n, ok := fr["name"].(string); ok {
								record(n)
							}
						}
					}
				}
			}
		}
	}
}

func replaceInContents(req map[string]any, mutate func(string) string) {
	contents, ok := req["contents"].([]any)
	if !ok {
		return
	}
	for _, c := range contents {
		if cm, ok := c.(map[string]any); ok {
			if parts, ok := cm["parts"].([]any); ok {
				for _, p := range parts {
					if pm, ok := p.(map[string]any); ok {
						if fc, ok := pm["functionCall"].(map[string]any); ok {
							if n, ok := fc["name"].(string); ok {
								fc["name"] = mutate(n)
							}
						}
						if fr, ok := pm["functionResponse"].(map[string]any); ok {
							if n, ok := fr["name"].(string); ok {
								fr["name"] = mutate(n)
							}
						}
					}
				}
			}
		}
	}
}

func visitToolChoice(req map[string]any, record func(string)) {
	tc, ok := req["tool_choice"].(map[string]any)
	if !ok {
		return
	}
	if fn, ok := tc["function"].(map[string]any); ok {
		if n, ok := fn["name"].(string); ok {
			record(n)
		}
	}
	if n, ok := tc["name"].(string); ok {
		record(n)
	}
}

func replaceInToolChoice(req map[string]any, mutate func(string) string) {
	tc, ok := req["tool_choice"].(map[string]any)
	if !ok {
		return
	}
	if fn, ok := tc["function"].(map[string]any); ok {
		if n, ok := fn["name"].(string); ok {
			fn["name"] = mutate(n)
		}
	}
	if n, ok := tc["name"].(string); ok {
		tc["name"] = mutate(n)
	}
}

// visitInput walks the OpenAI Responses `input` array, the conversation history
// of a /v1/responses request. Every item shape that names a tool is recorded:
// function_call / custom_tool_call carry the name directly, and a nested
// message content block holds a tool_use or an assistant tool_call.
//
// Without this the Responses lane fitted the declaration but left the history
// calling the tool by its original long name, so the upstream saw a request
// that contradicted itself — and the 64-char fix it was meant to deliver never
// actually applied, because the long name still travelled in `input`.
func visitInput(req map[string]any, record func(string)) {
	forEachInputName(req, func(n string) string {
		record(n)
		return n
	})
}

// replaceInInput mirrors visitInput: every name it could have recorded is
// rewritten here, so the fitted request stays internally consistent.
func replaceInInput(req map[string]any, mutate func(string) string) {
	forEachInputName(req, mutate)
}

// forEachInputName applies fn to every tool name reachable from the Responses
// `input` array. One walker serves both directions so a name the collect pass
// records is always a name the replace pass rewrites — the asymmetry that let
// the declaration and the history drift apart.
func forEachInputName(req map[string]any, fn func(string) string) {
	items, _ := req["input"].([]any) // the API also accepts a bare string
	for _, item := range items {
		im, ok := item.(map[string]any)
		if !ok {
			continue
		}
		applyName(fn, im)
		applyName(fn, im["function"])

		if tc, ok := im["tool_calls"].([]any); ok {
			for _, call := range tc {
				if cm, ok := call.(map[string]any); ok {
					applyName(fn, cm["function"])
				}
			}
		}
		content, _ := im["content"].([]any)
		for _, block := range content {
			bm, ok := block.(map[string]any)
			if !ok {
				continue
			}
			applyName(fn, bm)
			applyName(fn, bm["function"])
		}
	}
}

// applyName rewrites holder["name"] in place when it holds a string. A nil or
// non-map holder is skipped, so the same call is safe for optional shapes.
func applyName(mutate func(string) string, holder any) {
	hm, ok := holder.(map[string]any)
	if !ok {
		return
	}
	if n, ok := hm["name"].(string); ok {
		hm["name"] = mutate(n)
	}
}
