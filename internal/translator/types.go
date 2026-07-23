package translator

import (
	"encoding/json"
	"time"
)

// StreamState holds translation state for a single SSE stream.
type StreamState struct {
	CreatedAt            time.Time
	MessageStartSent     bool
	MessageId            string
	Model                string
	NextBlockIndex       int
	TextBlockStarted     bool
	TextBlockClosed      bool
	TextBlockIndex       int
	ThinkingBlockStarted bool
	ThinkingBlockIndex   int
	ToolCalls            map[int]ToolCallState
	ToolArgBuffers       map[int]string
	FinishReason         string
	Usage                *OpenAIUsage
}

// ToolCallState holds partial tool call info during streaming.
type ToolCallState struct {
	ID         string
	Name       string
	BlockIndex int
}

// OpenAIUsage tracks token counts.
type OpenAIUsage struct {
	PromptTokens             int                      `json:"prompt_tokens"`
	CompletionTokens         int                      `json:"completion_tokens"`
	CachedTokens             int                      `json:"cached_tokens"`
	CacheCreationInputTokens int                      `json:"cache_creation_input_tokens"`
	PromptTokensDetails      *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails  *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

func (u *OpenAIUsage) GetCachedTokens() int {
	if u == nil {
		return 0
	}
	if u.CachedTokens > 0 {
		return u.CachedTokens
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		return u.PromptTokensDetails.CachedTokens
	}
	return 0
}

type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

func (u *OpenAIUsage) ReasoningTokens() int {
	if u != nil && u.CompletionTokensDetails != nil {
		return u.CompletionTokensDetails.ReasoningTokens
	}
	return 0
}

// OpenAIChunk represents a single SSE chunk from an OpenAI-compatible stream.
type OpenAIChunk struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []OpenAIChoice `json:"choices"`
	Usage   *OpenAIUsage   `json:"usage"`
}

// OpenAIChoice holds one choice from an OpenAI stream chunk.
type OpenAIChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

// OpenAIResponse represents a non-streaming OpenAI-compatible response.
type OpenAIResponse struct {
	ID      string              `json:"id"`
	Model   string              `json:"model"`
	Choices []OpenAIResponseChoice `json:"choices"`
	Usage   *OpenAIUsage        `json:"usage"`
}

// OpenAIResponseChoice holds one choice from a non-streaming OpenAI response.
type OpenAIResponseChoice struct {
	Index        int              `json:"index"`
	Message      OpenAIRespMsg    `json:"message"`
	FinishReason *string          `json:"finish_reason"`
}

// OpenAIRespMsg holds the message in a non-streaming OpenAI response choice.
type OpenAIRespMsg struct {
	Role             string                 `json:"role"`
	Content          string                 `json:"content"`
	ReasoningContent string                 `json:"reasoning_content"`
	Reasoning        string                 `json:"reasoning"`
	ToolCalls        []OpenAIToolCallStream `json:"tool_calls"`
}

// OpenAIDelta holds the per-chunk delta in an OpenAI stream.
type OpenAIDelta struct {
	Role             string                 `json:"role"`
	Content          string                 `json:"content"`
	ReasoningContent string                 `json:"reasoning_content"`
	Reasoning        string                 `json:"reasoning"`
	ToolCalls        []OpenAIToolCallStream `json:"tool_calls"`
}

// OpenAIToolCallStream holds a streaming tool call fragment.
type OpenAIToolCallStream struct {
	Index    *int                  `json:"index"`
	ID       string                `json:"id,omitempty"`
	Type     string                `json:"type,omitempty"`
	Function *OpenAIFunctionStream `json:"function,omitempty"`
}

// OpenAIFunctionStream holds a streaming function call fragment.
type OpenAIFunctionStream struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// Claude System Prompt block
type ClaudeSystemBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ClaudeContentBlock represents one content block in a Claude message.
type ClaudeContentBlock struct {
	Type      string             `json:"type"`
	Text      string             `json:"text,omitempty"`
	Thinking  string             `json:"thinking,omitempty"`
	Source    *ClaudeImageSource `json:"source,omitempty"`
	ID        string             `json:"id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Input     json.RawMessage    `json:"input,omitempty"`
	ToolUseID string             `json:"tool_use_id,omitempty"`
	Content   json.RawMessage    `json:"content,omitempty"`
}

// ClaudeImageSource holds base64 image data.
type ClaudeImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ClaudeMessage is a single message in a Claude request.
type ClaudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ClaudeTool describes a tool in a Claude request.
type ClaudeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ClaudeToolChoice represents the tool_choice field.
type ClaudeToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// ClaudeThinking represents the thinking configuration in a Claude request.
type ClaudeThinking struct {
	Type    string `json:"type"`
	Budget  int    `json:"budget_tokens"`
}

// ClaudeRequest is the full Claude /v1/messages request body.
type ClaudeRequest struct {
	Model       string           `json:"model"`
	Messages    []ClaudeMessage  `json:"messages"`
	System      json.RawMessage  `json:"system,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	Thinking    *ClaudeThinking  `json:"thinking,omitempty"`
	Tools       []ClaudeTool     `json:"tools,omitempty"`
	ToolChoice  *json.RawMessage `json:"tool_choice,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
}

// OpenAIRequest is the translated OpenAI-compatible request body.
type OpenAIRequest struct {
	Model           string          `json:"model"`
	Messages        []OpenAIMessage `json:"messages"`
	Temperature     *float64        `json:"temperature,omitempty"`
	MaxTokens       *int            `json:"max_tokens,omitempty"`
	Tools           []OpenAITool    `json:"tools,omitempty"`
	ToolChoice      any             `json:"tool_choice,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Stream          bool            `json:"stream,omitempty"`
}

// OpenAIMessage is a single message in OpenAI format.
type OpenAIMessage struct {
	Role             string           `json:"role"`
	Content          any              `json:"content,omitempty"` // string or []OpenAIContentBlock
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"` // used for tool role messages
}

// OpenAIContentBlock holds one content block (text or image_url).
type OpenAIContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageUrl *OpenAIImageUrl `json:"image_url,omitempty"`
}

// OpenAIImageUrl holds a data URL for inline images.
type OpenAIImageUrl struct {
	URL string `json:"url"`
}

// OpenAIToolCall represents a tool call in OpenAI format.
type OpenAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function OpenAIFunctionCall `json:"function"`
}

// OpenAIFunctionCall holds a function call in OpenAI format.
type OpenAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAITool describes a tool definition in OpenAI format.
type OpenAITool struct {
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

// OpenAIFunction holds the function definition for an OpenAI tool.
type OpenAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}
