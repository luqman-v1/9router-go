package translator

import (
	json "encoding/json/v2"
	"fmt"
)

// ChatResponseToResponses turns a non-streaming Chat Completions body into the
// Response object a /v1/responses client expects, porting upstream
// convertResponsesStreamToJson, which aggregates the same events from a stream
// and reads id/created_at off response.created, the items off
// response.output_item.done, and the usage off response.completed.
//
// The response is produced by replaying a single synthetic chunk through the
// streaming translator rather than by a second, hand-written builder: the item
// shapes, their ordering and the usage details are then identical on both paths
// by construction, and a client that reads the streaming events reads this too.
func ChatResponseToResponses(body []byte) ([]byte, error) {
	var chat OpenAIResponse
	if err := json.Unmarshal(body, &chat); err != nil {
		return nil, fmt.Errorf("translator.ChatResponseToResponses: %w", err)
	}

	state := InitResponsesState(chat.Model, false)
	events := TranslateOpenAIToResponses(syntheticChunk(&chat), state)
	events = append(events, FlushResponses(state)...)

	response := completedResponse(events)
	if response == nil {
		return nil, fmt.Errorf("translator.ChatResponseToResponses: no response.completed produced")
	}
	out, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("translator.ChatResponseToResponses: %w", err)
	}
	return out, nil
}

// syntheticChunk folds a whole Chat Completions answer into the one chunk shape
// the streaming translator consumes. Only the first choice is kept: the
// Responses API emits a single output stream, and a multi-choice Chat reply
// has no faithful representation there.
func syntheticChunk(chat *OpenAIResponse) *OpenAIChunk {
	chunk := &OpenAIChunk{ID: chat.ID, Model: chat.Model, Usage: chat.Usage}
	if len(chat.Choices) == 0 {
		return chunk
	}
	choice := chat.Choices[0]
	chunk.Choices = []OpenAIChoice{{
		Index: choice.Index,
		Delta: OpenAIDelta{
			Role:             choice.Message.Role,
			Content:          choice.Message.Content,
			ReasoningContent: choice.Message.ReasoningContent,
			Reasoning:        choice.Message.Reasoning,
			ToolCalls:        choice.Message.ToolCalls,
		},
		FinishReason: choice.FinishReason,
	}}
	return chunk
}

// completedResponse picks the finished Response object out of the replayed
// events. A Chat body the translator rejected yields no such event, and the
// caller is told rather than handed an empty success.
func completedResponse(events []ResponsesEvent) map[string]any {
	for _, ev := range events {
		if ev.Event != "response.completed" {
			continue
		}
		if response, ok := ev.Data["response"].(map[string]any); ok {
			return response
		}
	}
	return nil
}
