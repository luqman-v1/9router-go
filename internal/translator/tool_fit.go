package translator

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// FitToolNamesForProvider is like FitToolNames but takes the provider id so
// sha256-based aliasing is used for providers that enforce a 64-char limit
// (openai, azure, azure-openai, azure-ai). OmniRoute #20258 spec:
// `${safePrefix}_${sha256(name)[:12]}`, at most MaxToolNameLength chars.
func FitToolNamesForProvider(body []byte, provider string) ([]byte, map[string]string) {
	if len(body) == 0 {
		return body, nil
	}

	if !bytes.Contains(body, []byte(`"tools"`)) &&
		!bytes.Contains(body, []byte(`"functions"`)) &&
		!bytes.Contains(body, []byte(`"tool_calls"`)) &&
		!bytes.Contains(body, []byte(`"function_call"`)) &&
		!bytes.Contains(body, []byte(`"tool_use"`)) &&
		!bytes.Contains(body, []byte(`"functionCall"`)) &&
		!bytes.Contains(body, []byte(`"functionResponse"`)) &&
		!bytes.Contains(body, []byte(`"tool_choice"`)) {
		return body, nil
	}

	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body, nil
	}

	toolMap := FitToolNamesInMap(req, provider)
	if len(toolMap) == 0 {
		return body, nil
	}

	newBody, err := json.Marshal(req, json.Deterministic(true))
	if err != nil {
		return body, nil
	}
	return newBody, toolMap
}

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

	toolMap := FitToolNamesInMap(req, "")
	if len(toolMap) == 0 {
		return body, nil
	}

	newBody, err := json.Marshal(req, json.Deterministic(true))
	if err != nil {
		return body, nil
	}
	return newBody, toolMap
}

// FitToolNamesInMap modifies tool names in-place on req and returns the
// shortenedName -> originalLongName mapping. Returns nil when nothing was changed.
func FitToolNamesInMap(req map[string]any, provider string) map[string]string {
	if req == nil {
		return nil
	}

	hash := UsesToolNameHash(provider)
	existing, longNames := collectToolNames(req, hash)
	if len(longNames) == 0 {
		return nil
	}

	assign := assignFittedToolNames
	if hash {
		assign = assignHashedToolNames
	}
	origToShort, shortToOrig := assign(longNames, existing)
	replaceToolNamesInMap(req, origToShort)
	return shortToOrig
}

// UsesToolNameHash reports whether provider enforces the 64-character tool
// name limit and expects deterministic sha256-based aliasing (openai, azure,
// azure-openai, azure-ai). Port of OmniRoute #20258's resolveToolNameMaxLength
// built-in table: openai declares toolNameMaxLength 64 in the registry;
// azure-openai and azure-ai have no registry entry and get the built-in 64.
func UsesToolNameHash(provider string) bool {
	return toolNameMaxLength(provider) > 0
}

// toolNameMaxLength is the single source of truth for which providers enforce
// the 64-char tool name limit. Both the fallback handler and the executor
// consult it, so the set cannot drift between them.
func toolNameMaxLength(provider string) int {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "azure", "azure-openai", "azure-ai":
		return MaxToolNameLength
	}
	return 0
}

// toolNameHashLen is the number of hex chars of sha256(original) that
// terminate every hash alias (OmniRoute #20258: sha256(name).slice(0, 12)).
const toolNameHashLen = 12

// sanitizeToolName replaces every char outside OpenAI's function-name class
// ([A-Za-z0-9_-]) with "_", matching upstream's replace(/[^A-Za-z0-9_-]/g, "_").
func sanitizeToolName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			return r
		}
		return '_'
	}, name)
}

// toolNamePrefix truncates a sanitized name to the prefix budget the alias
// format leaves: MaxToolNameLength minus the hash, minus the separator.
// The min guard is what keeps the slice inside len(safe) — the bare-hash
// fallback alias is shorter than the budget, so a bare [:51] panicked.
func toolNamePrefix(safe string) string {
	maxPrefixLen := MaxToolNameLength - toolNameHashLen - 1
	return safe[:min(len(safe), maxPrefixLen)]
}

// toolNameHash is the first toolNameHashLen hex chars of sha256(original).
func toolNameHash(orig string) string {
	sum := sha256.Sum256([]byte(orig))
	return hex.EncodeToString(sum[:])[:toolNameHashLen]
}

// toolNameAlias is the upstream alias shape `${safePrefix}_${hash12}`: the
// sanitized original truncated to the 51-char prefix budget, then the 12-char
// hash of the original.
func toolNameAlias(orig string) string {
	return toolNamePrefix(sanitizeToolName(orig)) + "_" + toolNameHash(orig)
}

// assignHashedToolNames generates the deterministic, collision-safe alias of
// OmniRoute #20258's normalizeOpenAIToolNames: `${safePrefix}_${hash12}`.
// safe is the original name with invalid chars replaced by "_", hash12 is the
// first 12 hex chars of sha256(original), and safePrefix is safe truncated to
// MaxToolNameLength-len(hash12)-1 (51) chars, so every alias is <= 64 chars.
//
// It also repairs names that fit the length limit but carry a char OpenAI
// rejects (a dot, a slash, a space): upstream aliases those too, because the
// constraint is the charset, not just the length.
//
// A collision (two originals sharing a prefix, or an alias shadowing a name
// already declared) falls back to the bare hash, then to a hash truncated with
// a numeric suffix — every slice is clamped to len, so a short fallback alias
// cannot panic the way the previous alias[:60] did.
func assignHashedToolNames(longNames []string, existing map[string]bool) (map[string]string, map[string]string) {
	origToShort := make(map[string]string, len(longNames))
	shortToOrig := make(map[string]string, len(longNames))

	for _, orig := range longNames {
		hash := toolNameHash(orig)
		alias := toolNameAlias(orig)
		if existing[alias] || shortToOrig[alias] != "" {
			// The prefix collided: drop it and keep the hash, which the
			// original determines and a different original cannot share.
			alias = hash
		}
		for idx := 1; existing[alias] || shortToOrig[alias] != ""; idx++ {
			// The hash collided too. Keep the ceiling by shortening the hash,
			// never by appending past MaxToolNameLength.
			suffix := fmt.Sprintf("_%d", idx)
			alias = hash[:min(len(hash), MaxToolNameLength-len(suffix))] + suffix
		}

		shortToOrig[alias] = orig
		origToShort[orig] = alias
		existing[alias] = true
	}
	return origToShort, shortToOrig
}

// collectToolNames gathers every tool name the body declares, in first-seen
// order, plus the set of names already present unchanged.
//
// hash is true for the providers whose upstream enforces the OpenAI function
// name constraint (regex ^[a-zA-Z0-9_-]{1,64}$). For those, a name that fits
// the length limit but carries an invalid char (a dot, a slash, a space) has to
// be aliased too — OmniRoute #20258 normalizes on the charset, not only on the
// length. For every other provider the constraint is length alone, so short
// names are left exactly as the client sent them.
func collectToolNames(req map[string]any, hash bool) (map[string]bool, []string) {
	existing := make(map[string]bool)
	seen := make(map[string]bool)
	var fitted []string

	record := func(n string) {
		if n == "" {
			return
		}
		needsAlias := len(n) > MaxToolNameLength || (hash && !isOpenAICompatibleToolName(n))
		if !needsAlias {
			existing[n] = true
			return
		}
		if !seen[n] {
			seen[n] = true
			fitted = append(fitted, n)
		}
	}

	visitTools(req, record)
	visitFunctions(req, record)
	visitMessages(req, record)
	visitContents(req, record)
	visitToolChoice(req, record)
	visitInput(req, record)
	return existing, fitted
}

// isOpenAICompatibleToolName reports whether name already satisfies OpenAI's
// function-name constraint, so no alias is needed.
func isOpenAICompatibleToolName(name string) bool {
	return len(name) <= MaxToolNameLength && sanitizeToolName(name) == name
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
