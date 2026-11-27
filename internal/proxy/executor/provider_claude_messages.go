package executor

import (
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"strings"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

func EnsureClaudeMessages(body []byte, model string) []byte {
	return ensureMessagesMaxTokens(body, model)
}

// EnsureClaudeMessagesFor is EnsureClaudeMessages for a Claude-shaped lane
// that is not Anthropic itself (minimax-code, opencode-zen, ...): provider
// selects the capability row that decides the thinking format.
func EnsureClaudeMessagesFor(body []byte, provider, model string) []byte {
	return ensureMessagesFor(body, provider, model)
}

// ensureMessagesMaxTokens converts an incoming request (OpenAI or Claude) into a spec-compliant
// Claude Messages API request payload:
// - Guarantees positive integer max_tokens (fallback from max_completion_tokens or default 4096)
// - Converts OpenAI tools [{type: "function", function: {name, description, parameters}}] to Claude [{name, description, input_schema}]
// - Converts OpenAI tool_choice to Claude format
// - Extracts role: "system" from messages into top-level system prompt
// - Converts assistant tool_calls to tool_use blocks and role: "tool" to user tool_result blocks
// - Merges consecutive same-role messages to uphold Claude alternating role invariant
// - Strips OpenAI-only fields like stream_options, store, max_completion_tokens, reasoning_effort
func ensureMessagesMaxTokens(body []byte, model string) []byte {
	return ensureMessagesFor(body, "claude", model)
}

func ensureMessagesFor(body []byte, provider, model string) []byte {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return body
	}
	if model != "" {
		reqMap["model"] = model
	}

	// 1. max_tokens
	maxTokensVal := 0
	if mt, ok := reqMap["max_tokens"]; ok && mt != nil {
		switch v := mt.(type) {
		case float64:
			maxTokensVal = int(v)
		case int:
			maxTokensVal = v
		case int64:
			maxTokensVal = int(v)
		}
	}
	if maxTokensVal <= 0 {
		if mct, ok := reqMap["max_completion_tokens"]; ok && mct != nil {
			switch v := mct.(type) {
			case float64:
				maxTokensVal = int(v)
			case int:
				maxTokensVal = v
			case int64:
				maxTokensVal = int(v)
			}
		}
	}
	if maxTokensVal <= 0 {
		maxTokensVal = 4096
	}
	reqMap["max_tokens"] = maxTokensVal
	delete(reqMap, "max_completion_tokens")

	// 2. tools: convert OpenAI tools to Claude {name, description, input_schema}
	if tools, ok := reqMap["tools"].([]any); ok && len(tools) > 0 {
		reqMap["tools"] = convertOpenAIToolsToClaude(tools)
	}

	// 3. tool_choice: convert OpenAI tool_choice to Claude format
	if tc, ok := reqMap["tool_choice"]; ok && tc != nil {
		if convertedTC := convertToolChoiceToClaude(tc); convertedTC != nil {
			reqMap["tool_choice"] = convertedTC
		} else {
			delete(reqMap, "tool_choice")
		}
	}

	// 3b. parallel_tool_calls is OpenAI's single-tool-call policy; Claude
	// spells it as a flag on tool_choice. Without tools there is nothing to
	// restrict, so the flag would only add noise (#4581). Either way the
	// OpenAI-only key is stripped, as step 5 does for other foreign fields.
	if ptc, hasPTC := reqMap["parallel_tool_calls"]; hasPTC {
		if ptc == false && hasClaudeTools(reqMap) {
			reqMap["tool_choice"] = withDisabledParallelToolUse(reqMap["tool_choice"])
		}
		delete(reqMap, "parallel_tool_calls")
	}

	// 4. messages & system
	if msgs, ok := reqMap["messages"].([]any); ok && len(msgs) > 0 {
		extractedSys, claudeMsgs := convertOpenAIMessagesToClaude(msgs)
		reqMap["messages"] = cleanupConvertedClaudeMessages(body, claudeMsgs)
		if extractedSys != "" {
			if existingSys, ok := reqMap["system"].(string); ok && existingSys != "" {
				reqMap["system"] = existingSys + "\n\n" + extractedSys
			} else if reqMap["system"] == nil {
				reqMap["system"] = extractedSys
			}
		}
	}

	// 5. Clean OpenAI-specific fields that strict Claude API rejects.
	// Claude returns thinking text only with thinking.display "summarized", a
	// field OpenAI has no equivalent for, so the client's ask cannot survive
	// translation on its own — capture it into the Claude field first.
	// (upstream thinkingUnified captureThinking)
	applyClaudeThinkingDisplay(reqMap)
	applyClaudeThinkingType(reqMap, provider, model)
	SanitizeClaudeThinking(reqMap)
	applyClaudeThinkingOffAndToolChoice(reqMap, provider, model)
	enforceClaudeThinkingSampling(reqMap)
	delete(reqMap, "stream_options")
	delete(reqMap, "store")
	delete(reqMap, "reasoning_effort")
	delete(reqMap, "n")

	// MiniMax exposes a Claude-compatible endpoint but rejects Anthropic's extended
	// output_config parameter with a generic 400 "invalid params" (upstream #820).
	if provider == "minimax" || provider == "minimax-cn" {
		delete(reqMap, "output_config")
	}
	// 6. Carry an OpenAI response_format onto the Claude request. It is an
	// OpenAI-only field the strict Claude API rejects, and dropping it silently
	// turns a structured-output ask into free text. Runs last, after every
	// tool/tool_choice/thinking normalization above, so its decision (forced
	// tool vs system-prompt instruction) is made on the final request state.
	translator.CarryClaudeStructuredOutput(reqMap)

	updated, err := marshalStable(reqMap)
	if err != nil {
		return body
	}
	return updated
}

// enforceClaudeThinkingSampling ensures that when Claude thinking is enabled,
// temperature is pinned to 1.0 only if a sampling parameter (temperature or top_p)
// was present in the request. If neither was sent, temperature remains unset so newer
// models don't reject the request with "temperature is deprecated" (OmniRoute #18639).
func enforceClaudeThinkingSampling(reqMap map[string]any) {
	thinking, _ := reqMap["thinking"].(map[string]any)
	if thinking == nil {
		return
	}
	tType, _ := thinking["type"].(string)
	if tType != "enabled" && tType != "adaptive" {
		return
	}

	_, hasTemp := reqMap["temperature"]
	_, hasTopP := reqMap["top_p"]

	if hasTemp || hasTopP {
		reqMap["temperature"] = 1.0
		delete(reqMap, "top_p")
	}
}

// applyClaudeThinkingOffAndToolChoice ports the Anthropic per-model rules that
// upstream applies in formats/claude.js prepareClaudeRequest (decolua/9router
// f3e1f716). Runs on every Claude-bound body so an OpenAI client, a native
// passthrough and the provider-level "off" override are all covered.
//
//   - thinkingCanDisable:false: a model that cannot switch thinking off
//     (Opus 5.5) must never see thinking.type "disabled" — upstream clamps the
//     request to minimal effort, drops the thinking block and sets
//     output_config.effort "low" (thinkingUnified.js applyFormat claude-adaptive).
//   - thinkingOffType: a model that rejects thinking "disabled" (Sonnet 5.5) takes
//     thinkingOffType ("between_tools") in its place, and between_tools only
//     accepts effort up to high.
//   - forcedToolChoice: a model that rejects forced tool_choice pinning
//     ("any" or a named tool) gets tool_choice downgraded to "auto". "required"
//     is left untouched, matching upstream, which only downgrades "any"/"tool".
func applyClaudeThinkingOffAndToolChoice(reqMap map[string]any, provider, model string) {
	caps := providers.GetCapabilitiesForModel(provider, model)
	thinking, _ := reqMap["thinking"].(map[string]any)
	disabled := thinking != nil && thinking["type"] == "disabled"

	if disabled && !caps.CanDisableThinking() {
		// thinking can't be switched off: clamp to minimal, drop the block,
		// and carry the effort on output_config (upstream claude-adaptive).
		delete(reqMap, "thinking")
		outCfg, _ := reqMap["output_config"].(map[string]any)
		if outCfg == nil {
			outCfg = map[string]any{}
			reqMap["output_config"] = outCfg
		}
		if _, set := outCfg["effort"]; !set {
			outCfg["effort"] = "low"
		}
		disabled = false
	}

	if disabled && caps.ThinkingOffType != "" {
		reqMap["thinking"] = map[string]any{"type": caps.ThinkingOffType}
		if oc, _ := reqMap["output_config"].(map[string]any); oc != nil {
			switch oc["effort"] {
			case "xhigh", "max":
				oc["effort"] = "high"
			}
		}
	}

	if !caps.CanForceToolChoice() {
		if tc, _ := reqMap["tool_choice"].(map[string]any); tc != nil {
			switch tc["type"] {
			case "any", "tool":
				graded := map[string]any{"type": "auto"}
				if v, ok := tc["disable_parallel_tool_use"]; ok {
					graded["disable_parallel_tool_use"] = v
				}
				reqMap["tool_choice"] = graded
			}
		}
	}
}

func convertOpenAIToolsToClaude(tools []any) []any {
	if len(tools) == 0 {
		return tools
	}
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			out = append(out, t)
			continue
		}
		if _, hasName := m["name"]; hasName {
			if _, hasSchema := m["input_schema"]; hasSchema {
				out = append(out, t)
				continue
			}
		}
		if fn, ok := m["function"].(map[string]any); ok {
			cTool := make(map[string]any)
			if name, ok := fn["name"].(string); ok {
				cTool["name"] = name
			}
			if desc, ok := fn["description"].(string); ok && desc != "" {
				cTool["description"] = desc
			}
			if params, ok := fn["parameters"]; ok && params != nil {
				cTool["input_schema"] = params
			} else {
				cTool["input_schema"] = map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				}
			}
			if cc, ok := m["cache_control"]; ok {
				cTool["cache_control"] = cc
			}
			if strict, ok := toolStrict(m, fn); ok {
				cTool["strict"] = strict
			}
			out = append(out, cTool)
			continue
		}
		if params, ok := m["parameters"]; ok {
			cTool := make(map[string]any, len(m))
			for k, v := range m {
				if k != "type" && k != "parameters" {
					cTool[k] = v
				}
			}
			cTool["input_schema"] = params
			out = append(out, cTool)
			continue
		}
		out = append(out, t)
	}
	return out
}

// toolStrict resolves an explicit tool strict mode, preferring the flat
// Responses/Codex shape and falling back to the Chat-Completions `function`
// shape (parity with decolua/9router#4543/#4573). An absent or non-boolean
// strict stays absent — the flag must never be invented.
func toolStrict(tool, fn map[string]any) (bool, bool) {
	if strict, ok := tool["strict"].(bool); ok {
		return strict, true
	}
	if fn == nil {
		return false, false
	}
	strict, ok := fn["strict"].(bool)
	return strict, ok
}

func convertToolChoiceToClaude(tc any) any {
	if tc == nil {
		return nil
	}
	switch v := tc.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			// Claude has a native "none"; dropping the key instead would hand
			// the model the tool permission the client withheld (#4577).
			return map[string]any{"type": "none"}
		default:
			return map[string]any{"type": "auto"}
		}
	case map[string]any:
		if tType, _ := v["type"].(string); tType == "function" {
			if fn, ok := v["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					return map[string]any{"type": "tool", "name": name}
				}
			}
		}
		return v
	default:
		return tc
	}
}

// hasClaudeTools reports whether the body still declares at least one tool.
// A policy about calling tools is meaningless without any.
func hasClaudeTools(reqMap map[string]any) bool {
	tools, ok := reqMap["tools"].([]any)
	return ok && len(tools) > 0
}

// withDisabledParallelToolUse returns the Claude tool_choice carrying
// disable_parallel_tool_use. An explicit choice keeps its type and name — the
// restriction is added, never substituted — and only a missing choice is
// filled in with Claude's own default, "auto" (parity #4581).
func withDisabledParallelToolUse(tc any) map[string]any {
	choice, ok := tc.(map[string]any)
	if !ok {
		choice = map[string]any{"type": "auto"}
	}
	choice["disable_parallel_tool_use"] = true
	return choice
}

// sanitizeToolUseID returns a tool id valid for the Anthropic Messages API
// (must match ^[a-zA-Z0-9_-]+$). Ids from other upstreams (e.g. Gemini
// function-call history translated to OpenAI format) may contain other
// characters; those are rewritten deterministically as "toolu_<sha256>" so
// the same id always maps to the same replacement, keeping tool_use and
// tool_result blocks paired. mapping must be shared across the whole request.
func sanitizeToolUseID(id string, mapping map[string]string) string {
	if id == "" {
		return id
	}
	if mapped, ok := mapping[id]; ok {
		return mapped
	}
	valid := true
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		valid = false
		break
	}
	if valid {
		mapping[id] = id
		return id
	}
	sum := sha256.Sum256([]byte(id))
	newID := "toolu_" + hex.EncodeToString(sum[:])[:24]
	mapping[id] = newID
	return newID
}

// cleanupConvertedClaudeMessages applies the Messages-API content rule to a
// conversation this package built itself.
//
// The OpenAI to Claude conversion passes the client content arrays through
// unchanged, so a turn the client left empty — or a turn that only held parts
// this conversion drops — reaches a Claude-shaped lane as an empty content
// array, and the provider 400s the whole request. The Claude-client path never
// had that hole: SanitizeClaudePassthrough drops those turns. This calls the
// same rule from the same place (translator.DropNonContentfulClaudeMessages)
// instead of restating it, so the two paths cannot drift apart.
//
// intentionalPrefill is read from the body the client sent: an OpenAI caller
// that ends on an assistant turn asked for a prefill, and the trailing-turn
// repair must not overwrite it (translator.ClaudeIntentionalPrefill).
//
// A conversation the filter empties completely keeps its turns. Dropping the
// last one would trade an invalid empty-content turn for an equally invalid
// empty messages array, and neither shape can be answered — this way the
// caller that emptied the turn still gets the reason the provider gives.
func cleanupConvertedClaudeMessages(clientBody []byte, claudeMsgs []any) []any {
	cleaned, dropped := translator.DropNonContentfulClaudeMessages(
		claudeMsgs, translator.ClaudeIntentionalPrefill(clientBody))
	if !dropped || len(cleaned) == 0 {
		return claudeMsgs
	}
	return cleaned
}

func convertOpenAIMessagesToClaude(messages []any) (systemText string, claudeMessages []any) {
	// Anthropic requires tool_use.id / tool_use_id to match ^[a-zA-Z0-9_-]+$.
	// History from other upstreams (e.g. Gemini) can contain ids with other
	// characters; rewrite them deterministically, with a single mapping shared
	// across the whole conversation so tool_use and tool_result stay paired.
	toolIDMap := map[string]string{}
	var systemParts []string
	intermediate := make([]map[string]any, 0, len(messages))

	for _, m := range messages {
		msgMap, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgMap["role"].(string)

		// Anthropic allows only user/assistant/system; OpenAI's newer
		// "developer" role is the same thing as system.
		if role == "system" || role == "developer" {
			// Upstream #5739: client billing headers vary per Claude Code turn and
			// must not enter the cached system prompt. Anthropic only ignores the
			// header when it is the separate system[0] block supplied by cloaking.
			switch c := msgMap["content"].(type) {
			case string:
				text := translator.StripAnthropicBillingHeader(c)
				if strings.TrimSpace(text) != "" {
					systemParts = append(systemParts, text)
				}
			case []any:
				for _, block := range c {
					if bMap, ok := block.(map[string]any); ok {
						if text, ok := bMap["text"].(string); ok {
							text = translator.StripAnthropicBillingHeader(text)
							if strings.TrimSpace(text) != "" {
								systemParts = append(systemParts, text)
							}
						}
					}
				}
			}
			continue
		}

		if role == "tool" {
			toolCallID, _ := msgMap["tool_call_id"].(string)
			toolCallID = sanitizeToolUseID(toolCallID, toolIDMap)
			contentVal := convertClaudeImageParts(msgMap["content"])
			// is_error is optional to Anthropic but REQUIRED by some gateways
			// that re-serialize the block through a strict Rust struct — Zed
			// answers "missing field `is_error`" to a request Anthropic itself
			// accepts. Emitting it always keeps one body valid on both.
			isError := translator.ToolResultIsError(msgMap)
			intermediate = append(intermediate, map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": toolCallID,
						"content":     contentVal,
						"is_error":    isError,
					},
				},
			})
			continue
		}

		if role == "assistant" {
			if toolCalls, hasTC := msgMap["tool_calls"].([]any); hasTC && len(toolCalls) > 0 {
				var contentBlocks []any
				if cStr, ok := msgMap["content"].(string); ok && cStr != "" {
					contentBlocks = append(contentBlocks, map[string]any{
						"type": "text",
						"text": cStr,
					})
				} else if cArr, ok := msgMap["content"].([]any); ok && len(cArr) > 0 {
					contentBlocks = append(contentBlocks, convertClaudeImageParts(cArr).([]any)...)
				}
				for _, tc := range toolCalls {
					tcMap, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					id, _ := tcMap["id"].(string)
					id = sanitizeToolUseID(id, toolIDMap)
					fn, _ := tcMap["function"].(map[string]any)
					name := ""
					var inputMap any = map[string]any{}
					if fn != nil {
						name, _ = fn["name"].(string)
						if argsStr, ok := fn["arguments"].(string); ok && strings.TrimSpace(argsStr) != "" {
							var parsed any
							if err := json.Unmarshal([]byte(argsStr), &parsed); err == nil && parsed != nil {
								inputMap = parsed
							}
						}
					}
					contentBlocks = append(contentBlocks, map[string]any{
						"type":  "tool_use",
						"id":    id,
						"name":  name,
						"input": inputMap,
					})
				}
				// The rebuilt turn carries only what the Messages API accepts: the
				// client's remaining keys are its OpenAI shape, not Claude's.
				intermediate = append(intermediate, map[string]any{
					"role":    "assistant",
					"content": contentBlocks,
				})
				continue
			}
		}

		// Nothing to reshape on this turn, but it still arrives in the client's
		// OpenAI shape. Replayed assistant turns are the ones that matter: an
		// OpenAI client echoes back the `reasoning_content` this proxy streamed
		// to it, and Anthropic rejects the field as an extra input, poisoning
		// every later turn of that conversation.
		intermediate = append(intermediate, claudeMessage(msgMap))
	}

	intermediate = dropImageEmptiedMessages(intermediate)

	var merged []any
	for _, m := range intermediate {
		if len(merged) == 0 {
			merged = append(merged, m)
			continue
		}
		prev := merged[len(merged)-1].(map[string]any)
		if prev["role"] == m["role"] {
			prev["content"] = combineClaudeContent(prev["content"], m["content"])
		} else {
			merged = append(merged, m)
		}
	}

	systemText = strings.Join(systemParts, "\n\n")
	return systemText, merged
}

// claudeMessageAllowedKeys are the only keys a Messages API message object
// accepts. Anthropic answers anything else with
// "messages.<n>.<key>: Extra inputs are not permitted" and refuses the request.
var claudeMessageAllowedKeys = map[string]bool{
	"role":    true,
	"content": true,
}

// claudeMessage returns msgMap reduced to the keys the Messages API accepts.
func claudeMessage(msgMap map[string]any) map[string]any {
	clean := make(map[string]any, len(claudeMessageAllowedKeys))
	for k, v := range msgMap {
		if claudeMessageAllowedKeys[k] {
			clean[k] = v
		}
	}
	if content, has := clean["content"]; has {
		converted := convertClaudeImageParts(content)
		clean["content"] = converted
		if emptiedByImageDrop(content, converted) {
			clean[imageEmptiedMarker] = true
		}
	}
	return clean
}

func combineClaudeContent(c1, c2 any) any {
	return append(normalizeClaudeBlocks(c1), normalizeClaudeBlocks(c2)...)
}

func normalizeClaudeBlocks(c any) []any {
	if c == nil {
		return []any{}
	}
	switch v := c.(type) {
	case string:
		if v == "" {
			return []any{}
		}
		return []any{map[string]any{"type": "text", "text": v}}
	case []any:
		return v
	case map[string]any:
		return []any{v}
	default:
		return []any{map[string]any{"type": "text", "text": fmt.Sprint(v)}}
	}
}

// isOpencodeResponsesModel ports the upstream free-executor lane rule
// (open-sse/executors/opencode.js isResponsesModel): the two curated free ids
// plus any Muse Spark model (upstream isMuseSparkModel:
// /^muse[-_]?spark(?:$|[-_:.\s])/i), so future free Spark ids keep the
// Responses lane without a code change. Paid lanes must not share this
// predicate — opencode-go routes on the catalog (goTargetFormat).
