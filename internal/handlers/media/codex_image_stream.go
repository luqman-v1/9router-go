package media

import (
	"bufio"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"math"
	"strconv"
	"strings"
)

// parseCodexImageStream reads the Responses event stream and returns the
// generated image plus the usage the upstream reported, mirroring parseStream
// in open-sse/handlers/imageProviders/codex.js. The stream is consumed frame
// by frame rather than buffered whole: a Responses turn can emit many partial
// images before the final one, and nothing here needs them once the result
// arrives. When the upstream emits a failure instead of an image, the error
// message it carried is returned so the caller surfaces it rather than a
// generic entitlement message.
func parseCodexImageStream(r io.Reader) (imageB64 string, usage *codexImageUsage, streamErr string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	event := ""
	data := strings.Builder{}
	flush := func() {
		name, payload := event, data.String()
		event = ""
		data.Reset()
		if name == "" || strings.TrimSpace(payload) == "" {
			return
		}

		switch name {
		case "response.output_item.done":
			item := codexResponseItem(payload)
			if item["type"] == "image_generation_call" {
				if result, _ := item["result"].(string); result != "" {
					imageB64 = result
				}
			}
		case "response.completed", "response.done":
			if parsed := normalizeCodexImageUsage(codexResponseUsage(payload)); parsed != nil {
				usage = parsed
			}
		case "response.failed", "error":
			if msg := codexStreamErrorMessage(payload); msg != "" {
				streamErr = msg
			}
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			flush()
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			trimmed := strings.TrimSpace(line[len("data:"):])
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(trimmed)
		case strings.TrimSpace(line) == "":
			flush()
		}
	}
	flush()
	return imageB64, usage, streamErr
}

// codexStreamErrorMessage digs the error message out of a failure frame. A
// Responses error can sit at the top level (`data.error.message`), under the
// response envelope (`data.response.error.message` / `data.response.error`), or
// on an item (`data.item.error.message`), so each shape is checked in turn.
func codexStreamErrorMessage(payload string) string {
	var frame struct {
		Error    jsontext.Value `json:"error"`
		Response jsontext.Value `json:"response"`
		Item     jsontext.Value `json:"item"`
	}
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return ""
	}
	if msg := codexErrorMessage(frame.Error); msg != "" {
		return msg
	}
	if len(frame.Response) > 0 {
		var nested struct {
			Error jsontext.Value `json:"error"`
		}
		if json.Unmarshal(frame.Response, &nested) == nil {
			if msg := codexErrorMessage(nested.Error); msg != "" {
				return msg
			}
		}
	}
	if len(frame.Item) > 0 {
		var nested struct {
			Error jsontext.Value `json:"error"`
		}
		if json.Unmarshal(frame.Item, &nested) == nil {
			if msg := codexErrorMessage(nested.Error); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// codexErrorMessage extracts a message from an error object, accepting either
// the structured {message} form or a bare string.
func codexErrorMessage(raw jsontext.Value) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// codexResponseItem decodes the frame body and returns the item it carries,
// whether the event nests it or carries it flat.
func codexResponseItem(payload string) map[string]any {
	var frame struct {
		Item     map[string]any `json:"item"`
		Response jsontext.Value `json:"response"`
	}
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return nil
	}
	if len(frame.Item) > 0 {
		return frame.Item
	}
	if len(frame.Response) == 0 {
		return nil
	}
	var nested struct {
		Item map[string]any `json:"item"`
	}
	if err := json.Unmarshal(frame.Response, &nested); err != nil {
		return nil
	}
	return nested.Item
}

// codexResponseUsage returns the usage object of a completion frame, whether it
// sits under response or at the top level.
func codexResponseUsage(payload string) any {
	var frame struct {
		Usage    any            `json:"usage"`
		Response jsontext.Value `json:"response"`
	}
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return nil
	}
	if frame.Usage != nil {
		return frame.Usage
	}
	if len(frame.Response) == 0 {
		return nil
	}
	var nested struct {
		Usage any `json:"usage"`
	}
	if err := json.Unmarshal(frame.Response, &nested); err != nil {
		return nil
	}
	return nested.Usage
}

// normalizeCodexImageUsage validates the upstream counters. A missing,
// non-integer or negative input/output count discards the whole usage object
// rather than recording a half-true row, and the optional cached and reasoning
// counts are carried only when they are usable integers (upstream
// normalizeResponsesUsage).
func normalizeCodexImageUsage(raw any) *codexImageUsage {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil
	}

	input, ok := codexTokenCount(obj, "input_tokens", "prompt_tokens")
	if !ok {
		return nil
	}
	output, ok := codexTokenCount(obj, "output_tokens", "completion_tokens")
	if !ok {
		return nil
	}

	usage := &codexImageUsage{
		PromptTokens:     input,
		CompletionTokens: output,
		TotalTokens:      input + output,
	}
	if total, ok := codexTokenCount(obj, "total_tokens"); ok {
		usage.TotalTokens = total
	}
	if cached := codexTokenDetails(obj, []string{"input_tokens_details", "prompt_tokens_details"}, "cached_tokens"); cached != nil {
		usage.CachedTokens = cached
	}
	if reasoning := codexTokenDetails(obj, []string{"output_tokens_details", "completion_tokens_details"}, "reasoning_tokens"); reasoning != nil {
		usage.ReasoningTokens = reasoning
	}
	return usage
}

// codexTokenCount reads one counter under any of the given keys. A decoded
// JSON number arrives as float64, so a fractional or negative value is
// rejected rather than truncated: recording 1.5 tokens as 1 would bill the turn
// for a figure the upstream never reported.
func codexTokenCount(obj map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		raw, present := obj[key]
		if !present || raw == nil {
			continue
		}
		n, ok := codexJSONInt(raw)
		if !ok {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// codexJSONInt accepts the shapes a decoded counter can take: a JSON number, a
// numeric string, or the verbatim jsontext form a partial re-marshal leaves
// behind.
func codexJSONInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case float64:
		if value < 0 || value != math.Trunc(value) || value > math.MaxInt32 {
			return 0, false
		}
		return int(value), true
	case int:
		if value < 0 {
			return 0, false
		}
		return value, true
	case int64:
		if value < 0 {
			return 0, false
		}
		return int(value), true
	case jsontext.Value:
		return codexJSONIntFromString(string(value))
	case string:
		return codexJSONIntFromString(value)
	default:
		return 0, false
	}
}

func codexJSONIntFromString(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// codexTokenDetails reads a nested counter, returning nil when the upstream
// omitted it or sent something unusable.
func codexTokenDetails(obj map[string]any, containers []string, key string) *int {
	for _, container := range containers {
		nested, ok := obj[container].(map[string]any)
		if !ok {
			continue
		}
		raw, present := nested[key]
		if !present || raw == nil {
			continue
		}
		n, ok := codexJSONInt(raw)
		if !ok {
			return nil
		}
		return &n
	}
	return nil
}
