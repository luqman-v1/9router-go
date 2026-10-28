package translator

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Port of open-sse/translator/request/openai-to-kiro.js: OpenAI Chat
// Completions -> Kiro/CodeWhisperer `conversationState` envelope.
//
// The kiro.dev gateway answers 400 {"message":"Improperly formed
// request.","reason":"REQUEST_BODY_INVALID"} for any body that is not a
// conversationState envelope — a top-level `systemPrompt` is also rejected, so
// the system text travels inside the first user turn's content.
//
// Session replay, thinking budgets and tool-spec name canonicalization from the
// JS version are NOT ported: this covers text/image conversations plus tool
// calls and tool results, which is what the dashboard and CLI clients send.

const kiroDefaultMaxTokens = 32000

// kiroEmptyUserText mirrors kiroEmptyUserContent in kiroConversation.js: an
// empty user turn would make the model answer "nothing to continue", so a
// neutral placeholder is injected (decolua/9router #4109).
const (
	kiroToolResultsPlaceholder = "Tool results provided."
	kiroEmptyUserPlaceholder   = "continue"
	kiroAssistantPlaceholder   = "..."
)

// KiroTranslateOptions carries the per-connection material the envelope needs.
type KiroTranslateOptions struct {
	// Model is the upstream model id (provider prefix already stripped).
	Model string
	// ProfileArn is the connection's resolved CodeWhisperer profile ARN.
	ProfileArn string
	// ContentPrefix is prepended to the first user turn (system prompt, time
	// context). Empty means only the timestamp is added.
	ContentPrefix string
}

// OpenAIToKiro converts an OpenAI chat-completions body into the Kiro
// conversationState envelope. It returns an error when the conversation cannot
// be shaped into a valid envelope, so callers fail locally instead of spending
// an upstream call that is guaranteed to 400.
func OpenAIToKiro(body []byte, opts KiroTranslateOptions) ([]byte, error) {
	var in struct {
		Messages    []map[string]any `json:"messages"`
		Tools       []map[string]any `json:"tools"`
		Temperature *float64         `json:"temperature"`
		TopP        *float64         `json:"top_p"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("kiro: parse openai body: %w", err)
	}

	history, current, err := kiroConvertMessages(in.Messages, opts.Model)
	if err != nil {
		return nil, err
	}

	// history excludes currentMessage upstream; the gateway takes the last
	// user turn separately in currentMessage.
	kiroStampModelIDs(history, opts.Model)

	conversationID := newKiroConversationID()
	timestamp := time.Now().UTC().Format(time.RFC3339)
	prefix := strings.TrimSpace(strings.Join([]string{
		opts.ContentPrefix,
		"[Context: Current time is " + timestamp + "]",
	}, "\n\n"))
	currentContent := kiroApplyContentPrefix(current, prefix)

	userInput := map[string]any{
		"content": currentContent,
		"modelId": opts.Model,
		"origin":  "AI_EDITOR",
	}
	if imgs, ok := current["images"].([]any); ok && len(imgs) > 0 {
		userInput["images"] = imgs
	}
	if ctx, ok := current["userInputMessageContext"].(map[string]any); ok && len(ctx) > 0 {
		userInput["userInputMessageContext"] = ctx
	}
	// Kiro has no top-level `tools` array: the catalogue travels on the last
	// user turn. Without it the model invents tools in plain text instead of
	// emitting toolUseEvent, and clients never get tool_calls.
	if specs, _ := kiroNormalizeToolSpecs(in.Tools); len(specs) > 0 {
		userCtx, _ := userInput["userInputMessageContext"].(map[string]any)
		if userCtx == nil {
			userCtx = map[string]any{}
			userInput["userInputMessageContext"] = userCtx
		}
		userCtx["tools"] = specs
	}

	payload := map[string]any{
		"conversationState": map[string]any{
			"chatTriggerType": "MANUAL",
			"conversationId":  conversationID,
			"currentMessage": map[string]any{
				"userInputMessage": userInput,
			},
			"history": history,
		},
	}
	if opts.ProfileArn != "" {
		payload["profileArn"] = opts.ProfileArn
	}

	inference := map[string]any{"maxTokens": kiroDefaultMaxTokens}
	if in.Temperature != nil {
		inference["temperature"] = *in.Temperature
	}
	if in.TopP != nil {
		inference["topP"] = *in.TopP
	}
	payload["inferenceConfig"] = inference

	out, err := json.Marshal(payload, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("kiro: marshal envelope: %w", err)
	}
	return out, nil
}

// kiroConvertMessages maps OpenAI messages onto Kiro turns. system/tool become
// user turns, consecutive same-role turns merge, and the trailing user turn is
// returned separately as the current message.
func kiroConvertMessages(messages []map[string]any, model string) ([]map[string]any, map[string]any, error) {
	var history []map[string]any
	currentRole := ""
	var pendingUser, pendingAssistant []string
	var pendingToolResults, pendingImages []any
	flush := func() {
		switch currentRole {
		case "user":
			content := strings.TrimSpace(strings.Join(pendingUser, "\n\n"))
			if content == "" {
				if len(pendingToolResults) > 0 {
					content = kiroToolResultsPlaceholder
				} else {
					content = kiroEmptyUserPlaceholder
				}
			}
			turn := map[string]any{
				"userInputMessage": map[string]any{
					"content": content,
					"modelId": "",
				},
			}
			if len(pendingImages) > 0 {
				turn["userInputMessage"].(map[string]any)["images"] = pendingImages
			}
			if len(pendingToolResults) > 0 {
				turn["userInputMessage"].(map[string]any)["userInputMessageContext"] = map[string]any{
					"toolResults": pendingToolResults,
				}
			}
			history = append(history, turn)
		case "assistant":
			content := strings.TrimSpace(strings.Join(pendingAssistant, "\n\n"))
			if content == "" {
				content = kiroAssistantPlaceholder
			}
			history = append(history, map[string]any{
				"assistantResponseMessage": map[string]any{"content": content},
			})
		}
		pendingUser = nil
		pendingAssistant = nil
		pendingToolResults = nil
		pendingImages = nil
	}

	for _, msg := range messages {
		if msg == nil {
			continue
		}
		rawRole, _ := msg["role"].(string)
		wasSystem := rawRole == "system" || rawRole == "developer"
		role := rawRole
		if role == "system" || role == "tool" {
			role = "user"
		}

		if currentRole != "" && role != currentRole {
			flush()
		}
		currentRole = role

		switch role {
		case "user":
			content, images, toolResults := kiroUserContent(msg)
			pendingImages = append(pendingImages, images...)
			pendingToolResults = append(pendingToolResults, toolResults...)
			if content != "" {
				if wasSystem {
					content = "<instructions>\n" + content + "\n</instructions>"
				}
				pendingUser = append(pendingUser, content)
			}
		case "assistant":
			text := kiroAssistantText(msg)
			toolUses := kiroAssistantToolUses(msg)
			if text != "" {
				pendingAssistant = append(pendingAssistant, text)
			}
			if len(toolUses) > 0 {
				flush()
				if n := len(history); n > 0 {
					if arm, ok := history[n-1]["assistantResponseMessage"].(map[string]any); ok {
						arm["toolUses"] = toolUses
					}
				}
				currentRole = ""
			}
		}
	}
	if currentRole != "" {
		flush()
	}

	// The last user turn is the current message; the rest stays in history.
	// Return the inner userInputMessage (callers read content/images/context off it).
	var current map[string]any
	for i := len(history) - 1; i >= 0; i-- {
		if uim, ok := history[i]["userInputMessage"].(map[string]any); ok {
			current = uim
			history = append(history[:i:i], history[i+1:]...)
			break
		}
	}
	if current == nil {
		current = map[string]any{"content": "", "modelId": model}
	}
	return mergeKiroConsecutiveUsers(history), current, nil
}

// kiroUserContent flattens one user/tool message into text, Kiro image blocks
// and tool-result blocks.
func kiroUserContent(msg map[string]any) (string, []any, []any) {
	var textParts []string
	var images, toolResults []any

	appendToolResult := func(id string, isErr bool, content string) {
		if id == "" {
			return
		}
		status := "success"
		if isErr {
			status = "error"
		}
		toolResults = append(toolResults, map[string]any{
			"toolUseId": id,
			"status":    status,
			"content":   []any{map[string]any{"text": content}},
		})
	}

	switch content := msg["content"].(type) {
	case string:
		if msg["tool_call_id"] != nil {
			isErr, _ := msg["is_error"].(bool)
			appendToolResult(fmt.Sprint(msg["tool_call_id"]), isErr, content)
			return "", images, toolResults
		}
		return content, images, toolResults
	case []any:
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch blockType, _ := block["type"].(string); blockType {
			case "text":
				if s, ok := block["text"].(string); ok {
					textParts = append(textParts, s)
				}
			case "image_url":
				url := ""
				if iu, ok := block["image_url"].(map[string]any); ok {
					url, _ = iu["url"].(string)
				}
				if img := kiroImageFromDataURI(url); img != nil {
					images = append(images, img)
				} else if url != "" {
					textParts = append(textParts, "[Image: "+url+"]")
				}
			case "image":
				// Claude shape: source.type=base64, source.media_type, source.data
				src, ok := block["source"].(map[string]any)
				if !ok {
					continue
				}
				if t, _ := src["type"].(string); t != "base64" {
					continue
				}
				data, _ := src["data"].(string)
				media := "image/png"
				if mt, ok := src["media_type"].(string); ok && mt != "" {
					media = mt
				}
				if data != "" {
					images = append(images, kiroImageBlock(media, data))
				}
			case "tool_result":
				text := ""
				switch inner := block["content"].(type) {
				case string:
					text = inner
				case []any:
					var b strings.Builder
					for _, ir := range inner {
						if m, ok := ir.(map[string]any); ok {
							if s, ok := m["text"].(string); ok {
								b.WriteString(s)
							}
						}
					}
					text = b.String()
				}
				isErr, _ := block["is_error"].(bool)
				id, _ := block["tool_use_id"].(string)
				appendToolResult(id, isErr, text)
			}
		}
	}
	return strings.Join(textParts, "\n"), images, toolResults
}

func kiroAssistantText(msg map[string]any) string {
	switch content := msg["content"].(type) {
	case string:
		return strings.TrimSpace(content)
	case []any:
		var parts []string
		for _, raw := range content {
			if block, ok := raw.(map[string]any); ok {
				if t, _ := block["type"].(string); t == "text" {
					if s, ok := block["text"].(string); ok {
						parts = append(parts, s)
					}
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

// kiroAssistantToolUses normalizes OpenAI tool_calls (and Claude tool_use
// blocks) into Kiro toolUses.
func kiroAssistantToolUses(msg map[string]any) []any {
	var out []any
	add := func(id, name string, input any) {
		if name == "" {
			return
		}
		if id == "" {
			id = newKiroUUID()
		}
		if input == nil {
			input = map[string]any{}
		}
		out = append(out, map[string]any{
			"toolUseId": id,
			"name":      name,
			"input":     input,
		})
	}

	if raw, ok := msg["tool_calls"].([]any); ok {
		for _, item := range raw {
			tc, ok := item.(map[string]any)
			if !ok {
				continue
			}
			id, _ := tc["id"].(string)
			fn, ok := tc["function"].(map[string]any)
			if !ok {
				continue
			}
			name, _ := fn["name"].(string)
			add(id, name, kiroParseToolInput(fn["arguments"]))
		}
	}
	if raw, ok := msg["content"].([]any); ok {
		for _, item := range raw {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := block["type"].(string); t != "tool_use" {
				continue
			}
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			add(id, name, block["input"])
		}
	}
	return out
}

// kiroParseToolInput decodes a JSON-encoded arguments string, tolerating the
// malformed payloads real clients emit.
func kiroParseToolInput(raw any) any {
	s, ok := raw.(string)
	if !ok {
		if raw == nil {
			return map[string]any{}
		}
		return raw
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return map[string]any{}
	}
	return parsed
}

func kiroImageFromDataURI(uri string) any {
	if !strings.HasPrefix(uri, "data:") {
		return nil
	}
	rest := strings.TrimPrefix(uri, "data:")
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return nil
	}
	media := rest[:comma]
	if i := strings.IndexByte(media, ';'); i >= 0 {
		media = media[:i]
	}
	if media == "" {
		media = "image/png"
	}
	return kiroImageBlock(media, rest[comma+1:])
}

func kiroImageBlock(mediaType, base64Data string) map[string]any {
	format := mediaType
	if i := strings.IndexByte(format, '/'); i >= 0 {
		format = format[i+1:]
	}
	return map[string]any{
		"format": format,
		"source": map[string]any{"bytes": base64Data},
	}
}

// kiroStampModelIDs backfills modelId on history user turns (Kiro rejects
// turns without it) and drops empty tool contexts.
func kiroStampModelIDs(history []map[string]any, model string) {
	for _, turn := range history {
		uim, ok := turn["userInputMessage"].(map[string]any)
		if !ok {
			continue
		}
		if s, _ := uim["modelId"].(string); s == "" {
			uim["modelId"] = model
		}
		if ctx, ok := uim["userInputMessageContext"].(map[string]any); ok && len(ctx) == 0 {
			delete(uim, "userInputMessageContext")
		}
	}
}

// kiroApplyContentPrefix prepends the system/time context to the current user
// turn, because a top-level systemPrompt is rejected by the gateway.
func kiroApplyContentPrefix(current map[string]any, prefix string) string {
	if prefix == "" {
		if s, ok := current["content"].(string); ok {
			return s
		}
		return ""
	}
	existing, _ := current["content"].(string)
	return prefix + "\n\n" + existing
}

// mergeKiroConsecutiveUsers folds adjacent user turns into one; Kiro requires
// strict user/assistant alternation. Tool results and images are carried over
// so nothing is silently dropped.
func mergeKiroConsecutiveUsers(history []map[string]any) []map[string]any {
	var merged []map[string]any
	for _, turn := range history {
		cur, isUser := turn["userInputMessage"].(map[string]any)
		if !isUser || len(merged) == 0 {
			merged = append(merged, turn)
			continue
		}
		prevUser, prevIsUser := merged[len(merged)-1]["userInputMessage"].(map[string]any)
		if !prevIsUser {
			merged = append(merged, turn)
			continue
		}
		a, _ := prevUser["content"].(string)
		b, _ := cur["content"].(string)
		prevUser["content"] = a + "\n\n" + b

		prevCtx, _ := prevUser["userInputMessageContext"].(map[string]any)
		curCtx, _ := cur["userInputMessageContext"].(map[string]any)
		if curCtx != nil {
			if prevCtx == nil {
				prevUser["userInputMessageContext"] = curCtx
			} else {
				for _, key := range []string{"toolResults", "tools"} {
					extra, _ := curCtx[key].([]any)
					if len(extra) == 0 {
						continue
					}
					existing, _ := prevCtx[key].([]any)
					prevCtx[key] = append(existing, extra...)
				}
			}
		}
		curImgs, _ := cur["images"].([]any)
		if len(curImgs) > 0 {
			prevImgs, _ := prevUser["images"].([]any)
			prevUser["images"] = append(prevImgs, curImgs...)
		}
	}
	return merged
}

func newKiroUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:16])
}

func newKiroConversationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
