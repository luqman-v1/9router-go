package semanticcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"9router/proxy/internal/fastjson"
	"9router/proxy/internal/translator"
)

// BuildCacheKey generates a deterministic, collision-resistant cache key
// incorporating tenant/session isolation, model, prompt text, tools, and temperature.
func BuildCacheKey(sessionID string, req *translator.OpenAIRequest) string {
	if req == nil {
		return ""
	}

	h := sha256.New()

	// Tenant / session isolation
	if sessionID != "" {
		h.Write([]byte("session:"))
		h.Write([]byte(sessionID))
		h.Write([]byte("\n"))
	}

	// Model
	h.Write([]byte("model:"))
	h.Write([]byte(req.Model))
	h.Write([]byte("\n"))

	// Messages
	for _, m := range req.Messages {
		h.Write([]byte(m.Role))
		h.Write([]byte(":"))
		switch v := m.Content.(type) {
		case string:
			h.Write([]byte(v))
		case []translator.OpenAIContentBlock:
			for _, block := range v {
				writeContentBlock(h, block.Type, block.Text, imageURLOf(block), fileDataOf(block))
			}
		case []any:
			for _, el := range v {
				bm, ok := el.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := bm["type"].(string)
				text, _ := bm["text"].(string)
				// Any other payload (image_url, file, input_audio, …) must reach
				// the hash too: two requests that differ only in their image bytes
				// would otherwise share a key and serve each other's responses.
				writeContentBlock(h, blockType, text, nestedString(bm, "image_url", "url"), nestedString(bm, "file", "file_data"))
			}
		}
		h.Write([]byte("\n"))
	}

	// Tools
	if len(req.Tools) > 0 {
		if tb, err := fastjson.Marshal(req.Tools); err == nil {
			h.Write([]byte("tools:"))
			h.Write(tb)
			h.Write([]byte("\n"))
		}
	}
	if req.ToolChoice != nil {
		if tcb, err := fastjson.Marshal(req.ToolChoice); err == nil {
			h.Write([]byte("tool_choice:"))
			h.Write(tcb)
			h.Write([]byte("\n"))
		}
	}

	// Temperature
	if req.Temperature != nil {
		h.Write([]byte(fmt.Sprintf("temp:%.4f\n", *req.Temperature)))
	}

	// Parameters that shape the completion. Any of these can change the answer
	// while leaving the prompt byte-identical, so a key that omits them serves
	// a stale body: a request asking for max_tokens 16 must not be answered with
	// the body produced for max_tokens 4096.
	writeIntParam(h, "max_tokens", req.MaxTokens)
	writeIntParam(h, "max_completion_tokens", req.MaxCompletionTokens)
	if req.ParallelToolCalls != nil {
		h.Write([]byte(fmt.Sprintf("parallel_tool_calls:%t\n", *req.ParallelToolCalls)))
	}
	if req.ReasoningEffort != "" {
		h.Write([]byte("reasoning_effort:"))
		h.Write([]byte(req.ReasoningEffort))
		h.Write([]byte("\n"))
	}

	return hex.EncodeToString(h.Sum(nil))
}

// writeIntParam folds an optional integer request parameter into the hash. An
// absent parameter is not written at all, so a request that omits the field
// keeps a different key from one that pins it to the same number.
func writeIntParam(h io.Writer, label string, v *int) {
	if v == nil {
		return
	}
	h.Write([]byte(label))
	h.Write([]byte(":"))
	h.Write([]byte(fmt.Sprintf("%d\n", *v)))
}

// writeContentBlock folds one content block into the cache-key hash. Every
// payload the block carries reaches the hash under a type-tagged label, so an
// image or file block is as discriminating as its text — hashing text alone
// makes two multimodal requests that differ only in their image bytes collide.
func writeContentBlock(h io.Writer, blockType, text, imageURL, fileData string) {
	h.Write([]byte("block:"))
	h.Write([]byte(blockType))
	h.Write([]byte(":"))
	switch blockType {
	case "text", "":
		h.Write([]byte(text))
	default:
		h.Write([]byte(text))
		h.Write([]byte("|img="))
		h.Write([]byte(imageURL))
		h.Write([]byte("|file="))
		h.Write([]byte(fileData))
	}
	h.Write([]byte("\n"))
}

// imageURLOf returns the inline image URL carried by a typed content block, or
// "" when it carries none.
func imageURLOf(block translator.OpenAIContentBlock) string {
	if block.ImageUrl == nil {
		return ""
	}
	return block.ImageUrl.URL
}

// fileDataOf returns the inline file payload carried by a typed content block,
// or "" when it carries none.
func fileDataOf(block translator.OpenAIContentBlock) string {
	if block.File == nil {
		return ""
	}
	return block.File.FileData
}

// nestedString reads obj[key][subkey] as a string, returning "" when either
// level is absent or not a string.
func nestedString(obj map[string]any, key, subkey string) string {
	inner, ok := obj[key].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := inner[subkey].(string)
	return s
}

// ExtractPromptText extracts a normalized text representation of the prompt.
func ExtractPromptText(req *translator.OpenAIRequest) string {
	if req == nil {
		return ""
	}
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Role)
		b.WriteByte(':')
		switch v := m.Content.(type) {
		case string:
			b.WriteString(v)
		case []translator.OpenAIContentBlock:
			for _, block := range v {
				if block.Type == "text" {
					b.WriteString(block.Text)
				}
			}
		case []any:
			for _, el := range v {
				if bm, ok := el.(map[string]any); ok {
					if bm["type"] == "text" {
						if t, ok := bm["text"].(string); ok {
							b.WriteString(t)
						}
					}
				}
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
