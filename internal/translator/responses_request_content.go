package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// and any other part is forwarded untouched.
func convertResponsesContent(raw jsontext.Value) (jsontext.Value, error) {
	if len(raw) == 0 {
		return raw, nil
	}

	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return raw, nil
	}

	var parts []map[string]any
	if err := json.Unmarshal(raw, &parts); err != nil {
		// Neither a string nor a part array: forward as-is rather than guess.
		return raw, nil
	}

	converted := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch partType, _ := part["type"].(string); partType {
		case responsesItemInputText, responsesItemOutputText:
			converted = append(converted, map[string]any{"type": openaiBlockText, "text": part["text"]})
		case responsesItemInputImage:
			url, _ := part["image_url"].(string)
			if url == "" {
				if fileID, ok := part["file_id"].(string); ok {
					url = fileID
				}
			}
			detail, _ := part["detail"].(string)
			if detail == "" {
				detail = "auto"
			}
			converted = append(converted, map[string]any{
				"type":      openaiBlockImageURL,
				"image_url": map[string]any{"url": url, "detail": detail},
			})
		default:
			converted = append(converted, part)
		}
	}

	out, err := json.Marshal(converted, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("translator.convertResponsesContent: %w", err)
	}
	return out, nil
}

// responsesReasoningText reads the visible text of a reasoning item. Only the
// summary counts: encrypted_content is continuity data, not displayable text.
func responsesReasoningText(item ResponsesInputItem) string {
	if len(item.Summary) > 0 {
		if txt := joinNonEmptyText(item.Summary); txt != "" {
			return txt
		}
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(item.Content, &parts); err == nil && len(parts) > 0 {
		var texts []string
		for _, p := range parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

// toolCallArguments renders the argument payload for a Chat tool call. A custom
// tool carries its freeform program in `input`, which is wrapped so the Chat
// side still receives a JSON arguments string.
func toolCallArguments(item ResponsesInputItem, itemType string) string {
	if itemType != responsesItemCustomToolCall {
		return item.Arguments
	}

	var asString string
	if err := json.Unmarshal(item.Input, &asString); err == nil {
		wrapped, err := json.Marshal(map[string]any{"input": asString})
		if err != nil {
			return item.Arguments
		}
		return string(wrapped)
	}
	if len(item.Input) == 0 {
		wrapped, _ := json.Marshal(map[string]any{"input": ""})
		return string(wrapped)
	}
	return string(item.Input)
}

// rawToJSONString renders a scalar-or-object field as a JSON string, leaving a
// string value untouched.
func rawToJSONString(raw jsontext.Value) any {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	return string(raw)
}

func joinNonEmptyText(parts []ResponsesSummary) string {
	var texts []string
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
