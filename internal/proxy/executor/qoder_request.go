package executor

import (
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"

	"9router/proxy/internal/providers"
)

// Qoder chat payload — port of open-sse/executors/qoder.js: extractText,
// lastUserText, stableHash, stableChatRecordId, truncate, normalizeMessages,
// normalizeContent, buildQoderRequestBody.
//
// Forwarding the client's OpenAI-shaped body to agent_chat_generation does not
// work: Qoder routes to a node called `agent_router`, that node finds no flow
// for an OpenAI request, and answers
//   400 [FAIL]node:agent_router msg:None flow nodes found for router agent_router
// on every model. The payload below is what the endpoint actually expects.

// qoderDefaultMaxTokens is the ceiling upstream starts from before narrowing
// to the model's own max_output_tokens and the client's request.
const qoderDefaultMaxTokens = 32_768

// qoderSystemRole is the OpenAI role that carries the system prompt, which
// Qoder rejects inside `messages`.
const qoderSystemRole = "system"

// content-block discriminators (open-sse/translator/schema/blocks.js).
const (
	qoderBlockText     = "text"
	qoderBlockImageURL = "image_url"
	qoderBlockImage    = "image"
	qoderBlockFile     = "file"
	qoderBlockDocument = "document"
)

// qoderFileStub is what a document block becomes. Inlining 30MB of PDF into
// agent_chat_generation is what upstream refuses to do too: Qoder reads
// documents through its own file API.
func qoderFileStub(name string) string {
	if name == "" {
		name = "file"
	}
	return "[file omitted: " + name + " — Qoder reads documents via its file API, not inlined bytes]"
}

// qoderTextOf flattens a message content to plain text. Image blocks
// contribute nothing, matching upstream's extractText.
func qoderTextOf(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case nil:
		return ""
	case []any:
		var parts []string
		for _, item := range value {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] == qoderBlockText {
				if text, ok := block["text"].(string); ok {
					parts = append(parts, text)
					continue
				}
			}
			if text, ok := block["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprintf("%v", value)
	}
}

// qoderLastUserText returns the final user turn's text, which Qoder mirrors
// into chat_context.originalContent and chat_context.text.
func qoderLastUserText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok || msg["role"] != "user" {
			continue
		}
		if content, ok := msg["content"].(string); ok {
			return content
		}
		return qoderTextOf(msg["content"])
	}
	return ""
}

// qoderStableHash is upstream's stableHash: a sha256 over the prefix and each
// part NUL-separated, truncated to 16 hex chars.
func qoderStableHash(prefix string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// qoderStableChatRecordId derives chat_record_id (and request_set_id) from the
// conversation, so the same turn keeps the same record and a changed one gets
// a fresh id. Port of upstream's stableChatRecordId.
func qoderStableChatRecordId(model string, messages []any, tools []any, maxTokens int) string {
	h := sha256.New()
	h.Write([]byte("qoder-record\x00"))
	h.Write([]byte(model))
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role, ok := msg["role"].(string); ok && role != "" {
			h.Write([]byte{0})
			h.Write([]byte(role))
		}
		switch content := msg["content"].(type) {
		case string:
			if content != "" {
				h.Write([]byte{0})
				h.Write([]byte(content))
			}
		case []any:
			// Include image refs so the same prompt with a different image
			// gets a distinct chat_record_id.
			encoded, err := json.Marshal(content)
			if err != nil {
				continue
			}
			h.Write([]byte{0})
			h.Write(encoded)
		}
	}
	if tools != nil {
		if encoded, err := json.Marshal(tools); err == nil {
			h.Write([]byte{0})
			h.Write(encoded)
		}
	}
	h.Write([]byte(fmt.Sprintf("\x00mt=%d", maxTokens)))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// qoderTruncate shortens a string for the business.name label, upstream's
// truncate().
func qoderTruncate(s string, n int) string {
	if s == "" {
		return ""
	}
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// qoderNormalizeContent shapes one message's content for Qoder.
//
// Text-only content is flattened to a plain string (Qoder's historical shape).
// When images are present the content stays an array and image blocks are kept
// as OpenAI-style image_url parts. The legacy top-level image_urls and
// chat_context.imageUrls slots stay null: qodercli leaves them null too.
func qoderNormalizeContent(content any) any {
	switch value := content.(type) {
	case string:
		return value
	case nil:
		return ""
	case []any:
		return qoderNormalizeBlocks(value)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// qoderNormalizeBlocks is the multipart half of qoderNormalizeContent.
func qoderNormalizeBlocks(content []any) any {
	var blocks []any
	var textParts []string
	hasImage := false

	pushText := func(text string) {
		if text == "" {
			return
		}
		// Text before the first image block is collected separately and
		// prepended once, so a text→image turn keeps its reading order.
		if hasImage || len(blocks) > 0 {
			blocks = append(blocks, map[string]any{"type": qoderBlockText, "text": text})
			return
		}
		textParts = append(textParts, text)
	}

	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		// OpenAI image_url and Claude image blocks both become one canonical
		// image_url part.
		if url := qoderImageURLOf(block); url != "" {
			blocks = append(blocks, qoderImageBlock(url))
			hasImage = true
			continue
		}
		if url := qoderClaudeImageURL(block); url != "" {
			blocks = append(blocks, qoderImageBlock(url))
			hasImage = true
			continue
		}
		switch block["type"] {
		case qoderBlockFile:
			pushText(qoderFileStub(qoderFileName(block)))
		case qoderBlockDocument:
			name, _ := block["title"].(string)
			pushText(qoderFileStub(name))
		default:
			if text, ok := block["text"].(string); ok && text != "" {
				pushText(text)
			}
		}
	}

	if !hasImage {
		return strings.Join(textParts, "\n")
	}
	if len(textParts) > 0 {
		blocks = append([]any{map[string]any{"type": qoderBlockText, "text": strings.Join(textParts, "\n")}}, blocks...)
	}
	return blocks
}

// qoderImageBlock is the one image shape Qoder accepts, in both OpenAI and
// Claude form.
func qoderImageBlock(url string) map[string]any {
	return map[string]any{
		"type":      qoderBlockImageURL,
		"image_url": map[string]any{"url": url},
	}
}

// qoderImageURLOf reads an OpenAI image_url block's URL, tolerating both the
// string and the {url:…} form.
func qoderImageURLOf(block map[string]any) string {
	if block["type"] != qoderBlockImageURL {
		return ""
	}
	switch value := block["image_url"].(type) {
	case string:
		return value
	case map[string]any:
		if url, ok := value["url"].(string); ok {
			return url
		}
	}
	return ""
}

// qoderClaudeImageURL converts a Claude {type:"image",source:{…}} block to an
// OpenAI image_url URL. Claude base64 becomes a data URI; a plain URL is used
// as-is.
func qoderClaudeImageURL(block map[string]any) string {
	if block["type"] != qoderBlockImage || block["source"] == nil {
		return ""
	}
	source, ok := block["source"].(map[string]any)
	if !ok {
		return ""
	}
	if data, ok := source["data"].(string); ok && data != "" && source["type"] == "base64" {
		mime, _ := source["media_type"].(string)
		if mime == "" {
			mime = "image/png"
		}
		return "data:" + mime + ";base64," + data
	}
	if url, ok := source["url"].(string); ok {
		return url
	}
	return ""
}

// qoderFileName picks a filename out of an OpenAI file block.
func qoderFileName(block map[string]any) string {
	file, ok := block["file"].(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"filename", "name"} {
		if name, ok := file[key].(string); ok && name != "" {
			return name
		}
	}
	return ""
}

// qoderModelKey is the model id Qoder knows the model by: the wire id with any
// Qoder qualifier stripped, so `qd/qfmodel`, `qoder/qfmodel` and `qfmodel`
// all resolve to the same key.
//
// The chat router normally hands the executor an already-resolved bare id, but
// a client can send the aliased form and an unknown prefix must not be mistaken
// for part of the model name — that would look up a model_config key the
// catalogue has never heard of.
func qoderModelKey(model string) string {
	key := strings.TrimSpace(model)
	if prefix, rest, found := strings.Cut(key, "/"); found {
		switch providers.ResolveAlias(prefix) {
		case "qoder", "qoder-cn":
			key = rest
		}
	}
	return key
}

// qoderNormalizedMessages hoists role:system out of the array (Qoder rejects
// system in messages) and flattens multipart content, except image blocks.
func qoderNormalizedMessages(messages []any) ([]any, string) {
	if len(messages) == 0 {
		return []any{}, ""
	}
	var systemParts []string
	out := make([]any, 0, len(messages))
	for _, item := range messages {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if msg["role"] == qoderSystemRole {
			if text := qoderTextOf(msg["content"]); text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}
		cloned := make(map[string]any, len(msg))
		for k, v := range msg {
			cloned[k] = v
		}
		cloned["content"] = qoderNormalizeContent(msg["content"])
		out = append(out, cloned)
	}
	return out, strings.Join(systemParts, "\n\n")
}

// qoderResolveMaxTokens mirrors upstream's clamp: start from the model's own
// output ceiling (falling back to 32768) and never exceed what the client
// asked for.
func qoderResolveMaxTokens(modelConfig map[string]any, body map[string]any) int {
	maxTokens := qoderDefaultMaxTokens
	if ceiling := qoderPositiveInt(modelConfig["max_output_tokens"]); ceiling > 0 {
		maxTokens = ceiling
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		if requested := qoderPositiveInt(body[key]); requested > 0 && requested < maxTokens {
			maxTokens = requested
		}
	}
	return maxTokens
}

// qoderPositiveInt reads a decoded JSON number that must be a positive
// integer, returning 0 for anything else (absent, null, string, negative).
func qoderPositiveInt(v any) int {
	value, ok := v.(float64)
	if !ok || value <= 0 {
		return 0
	}
	return int(value)
}

// qoderBuiltChat is the outcome of building the request body.
type qoderBuiltChat struct {
	QoderKey    string
	Payload     map[string]any
	ModelConfig map[string]any
}

// buildQoderChatRequest maps an OpenAI chat body onto the payload Qoder
// expects. modelConfig is the live catalog entry for the model, which the
// caller must resolve first (it is not derivable from the request).
func buildQoderChatRequest(body []byte, modelConfig map[string]any, creds QoderCosyCreds, now int64) (qoderBuiltChat, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return qoderBuiltChat{}, fmt.Errorf("parse body: %w", err)
	}

	model, _ := request["model"].(string)
	qoderKey := qoderModelKey(model)
	if qoderKey == "" {
		return qoderBuiltChat{}, fmt.Errorf("qoder: request carries no model")
	}
	if modelConfig == nil {
		return qoderBuiltChat{}, fmt.Errorf("qoder: model_config for %q not yet known (run a model list fetch or check upstream connectivity)", qoderKey)
	}

	incoming, _ := request["messages"].([]any)
	messages, systemText := qoderNormalizedMessages(incoming)
	tools, _ := request["tools"].([]any)

	maxTokens := qoderResolveMaxTokens(modelConfig, request)
	lastUser := qoderLastUserText(messages)
	sessionID := qoderStableHash("qoder-session", creds.UserID, qoderKey)
	recordID := qoderStableChatRecordId(qoderKey, messages, tools, maxTokens)

	payload := map[string]any{
		"request_id":     uuid.NewString(),
		"request_set_id": recordID,
		"chat_record_id": recordID,
		"session_id":     sessionID,
		// Qoder only ever streams; a non-streaming client request is folded
		// back into one chat.completion after the fact.
		"stream":           true,
		"chat_task":        "FREE_INPUT",
		"is_reply":         true,
		"is_retry":         false,
		"source":           1,
		"version":          "3",
		"session_type":     "qodercli",
		"agent_id":         "agent_common",
		"task_id":          "common",
		"code_language":    "",
		"chat_prompt":      "",
		"image_urls":       nil,
		"aliyun_user_type": "",
		"system":           systemText,
		"messages":         messages,
		"tools":            orEmptySlice(tools),
		"parameters":       map[string]any{"max_tokens": maxTokens},
		"chat_context": map[string]any{
			"chatPrompt": "",
			"imageUrls":  nil,
			"extra": map[string]any{
				"context": []any{},
				"modelConfig": map[string]any{
					"key":          qoderKey,
					"is_reasoning": modelConfig["is_reasoning"] == true,
				},
				"originalContent": lastUser,
			},
			"features": []any{},
			"text":     lastUser,
		},
		"model_config": modelConfig,
		"business": map[string]any{
			"product":  "cli",
			"version":  "1.0.0",
			"type":     "agent",
			"stage":    "start",
			"id":       uuid.NewString(),
			"name":     qoderTruncate(lastUser, 30),
			"begin_at": now,
		},
	}

	if tier := resolveQoderContextTier(modelConfig, systemText, messages, tools, os.Getenv(QoderContextTierEnv)); tier != nil {
		applyQoderContextTier(payload, tier.Tier)
	}

	return qoderBuiltChat{QoderKey: qoderKey, Payload: payload, ModelConfig: modelConfig}, nil
}

// orEmptySlice keeps a nil tools array marshalling as [] rather than null.
func orEmptySlice(items []any) []any {
	if items == nil {
		return []any{}
	}
	return items
}
