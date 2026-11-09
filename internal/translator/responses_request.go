package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
)

// Responses input/output item types used by the request direction. The
// streaming-response types live in responses_state.go.
const (
	responsesItemFunctionCallOutput   = "function_call_output"
	responsesItemCustomToolCallOutput = "custom_tool_call_output"
	responsesItemAdditionalTools      = "additional_tools"
	responsesItemInputText            = "input_text"
	responsesItemInputImage           = "input_image"
	responsesToolTypeCustom           = "custom"

	openaiBlockText     = "text"
	openaiBlockImageURL = "image_url"
	openaiBlockFunction = "function"

	roleSystem = "system"
	roleUser   = "user"
	roleTool   = "tool"

	responsesEnvelopeKeyInput        = "input"
	responsesEnvelopeKeyInstructions = "instructions"

	// responsesEmptyInputText stands in for a blank prompt: providers reject a
	// request that carries no user turn at all.
	responsesEmptyInputText = "..."
)

// ResponsesSummary is one part of a reasoning item's summary.
type ResponsesSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ResponsesInputItem is one entry of a Responses API `input` array. Only the
// fields the Chat conversion reads are modelled; the rest are dropped, matching
// upstream, which rebuilds `messages` from scratch while spreading the body.
type ResponsesInputItem struct {
	Type             string             `json:"type"`
	Role             string             `json:"role"`
	Content          jsontext.Value     `json:"content"`
	Name             string             `json:"name"`
	CallID           string             `json:"call_id"`
	Arguments        string             `json:"arguments"`
	Input            jsontext.Value     `json:"input"`
	Output           jsontext.Value     `json:"output"`
	Summary          []ResponsesSummary `json:"summary"`
	EncryptedContent string             `json:"encrypted_content"`
	Tools            []map[string]any   `json:"tools"`
}

// ResponsesRequestResult is the outcome of converting a Responses request.
type ResponsesRequestResult struct {
	// Body is the Chat Completions request to send upstream. It is the original
	// body untouched when the request carried nothing to convert.
	Body []byte
	// CustomToolNames lists the tools declared as freeform custom tools, so the
	// response conversion emits custom_tool_call_input rather than JSON args.
	CustomToolNames []string
	// Converted reports whether the body was actually rewritten. When false the
	// upstream speaks Responses natively and must not receive a Chat body.
	Converted bool
}

// ResponsesToChatRequest converts a Responses API request body into Chat
// Completions, porting openaiResponsesToOpenAIRequest upstream. Fields the Chat
// API does not know about are stripped so they cannot leak upstream.
func ResponsesToChatRequest(body []byte) (*ResponsesRequestResult, error) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("translator.ResponsesToChatRequest: %w", err)
	}

	rawInput, ok := envelope[responsesEnvelopeKeyInput]
	if !ok || rawInput == nil {
		return &ResponsesRequestResult{Body: body}, nil
	}

	items, err := normalizeResponsesInput(rawInput)
	if err != nil || items == nil {
		return &ResponsesRequestResult{Body: body}, nil
	}

	conv := newResponsesRequestConv()
	if instructions, ok := envelope[responsesEnvelopeKeyInstructions]; ok && instructions != "" {
		conv.messages = append(conv.messages, map[string]any{
			"role":    roleSystem,
			"content": instructions,
		})
	}

	conv.consumeItems(items)
	conv.flushAssistant()
	conv.flushToolResults()

	conv.applyTools(envelope)
	stripResponsesOnlyFields(envelope)

	envelope["messages"] = conv.messages
	// _customToolNames is translator-only upstream (chatCore reads it off the
	// translated body and deletes it before sending), so it travels in the
	// result instead of leaking into the body the provider receives.
	out, err := json.Marshal(envelope, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("translator.ResponsesToChatRequest: %w", err)
	}
	return &ResponsesRequestResult{
		Body:            out,
		CustomToolNames: conv.customToolNameIn,
		Converted:       true,
	}, nil
}

// normalizeResponsesInput coerces the many shapes `input` may take into an item
// list. It returns nil when the value is neither a string nor an array, which
// leaves the original body in place.
func normalizeResponsesInput(raw any) ([]ResponsesInputItem, error) {
	switch input := raw.(type) {
	case string:
		text := input
		if strings.TrimSpace(text) == "" {
			text = responsesEmptyInputText
		}
		return []ResponsesInputItem{userTextItem(text)}, nil

	case []any:
		// An empty input[] would yield messages:[] which every provider rejects.
		if len(input) == 0 {
			return []ResponsesInputItem{userTextItem(responsesEmptyInputText)}, nil
		}
		encoded, err := json.Marshal(input, json.Deterministic(true))
		if err != nil {
			return nil, fmt.Errorf("translator.normalizeResponsesInput: %w", err)
		}
		var items []ResponsesInputItem
		if err := json.Unmarshal(encoded, &items); err != nil {
			return nil, fmt.Errorf("translator.normalizeResponsesInput: %w", err)
		}
		return items, nil
	}
	return nil, nil
}

func userTextItem(text string) ResponsesInputItem {
	encoded, _ := json.Marshal([]any{
		map[string]any{"type": responsesItemInputText, "text": text},
	})
	return ResponsesInputItem{
		Type:    responsesItemMessage,
		Role:    roleUser,
		Content: encoded,
	}
}

// stripResponsesOnlyFields removes the Responses-specific keys and maps
// max_output_tokens onto max_tokens, so the upstream never sees a field it does
// not understand.
func stripResponsesOnlyFields(envelope map[string]any) {
	if v, ok := envelope["max_output_tokens"]; ok {
		if _, has := envelope["max_tokens"]; !has {
			envelope["max_tokens"] = v
		}
		delete(envelope, "max_output_tokens")
	}

	for _, key := range []string{
		responsesEnvelopeKeyInput,
		responsesEnvelopeKeyInstructions,
		"include",
		"prompt_cache_key",
		"store",
		"client_metadata",
	} {
		delete(envelope, key)
	}

	if reasoning, ok := envelope["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok {
			envelope["reasoning_effort"] = effort
		}
	}
	delete(envelope, "reasoning")
}
