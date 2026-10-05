package translator

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
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
	PromptCacheIncluded      bool                     `json:"-"`
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
	ID      string                 `json:"id"`
	Model   string                 `json:"model"`
	Choices []OpenAIResponseChoice `json:"choices"`
	Usage   *OpenAIUsage           `json:"usage"`
}

// OpenAIResponseChoice holds one choice from a non-streaming OpenAI response.
type OpenAIResponseChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIRespMsg `json:"message"`
	FinishReason *string       `json:"finish_reason"`
}

// OpenAIRespMsg holds the message in a non-streaming OpenAI response choice.
type OpenAIRespMsg struct {
	Role             string                 `json:"role"`
	Content          string                 `json:"content"`
	ReasoningContent string                 `json:"reasoning_content"`
	Reasoning        string                 `json:"reasoning"`
	ToolCalls        []OpenAIToolCallStream `json:"tool_calls"`
}

// OpenAIReasoningDetail is one entry of reasoning_details. Vendors disagree on
// the shape: some send a bare string, others an object carrying text or content.
type OpenAIReasoningDetail struct {
	Text    string
	Content string
}

// UnmarshalJSON accepts both the bare-string and the object shape.
func (d *OpenAIReasoningDetail) UnmarshalJSON(b []byte) error {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("OpenAIReasoningDetail.UnmarshalJSON: %w", err)
	}
	switch v := raw.(type) {
	case string:
		d.Text = v
	case map[string]any:
		d.Text, _ = v["text"].(string)
		d.Content, _ = v["content"].(string)
	}
	return nil
}

// MarshalJSON emits the bare string when the entry carries nothing but text, so
// a translated request round-trips back to the shape the vendor expects.
func (d OpenAIReasoningDetail) MarshalJSON() ([]byte, error) {
	if d.Content == "" {
		return json.Marshal(d.Text)
	}
	return json.Marshal(map[string]string{"text": d.Text, "content": d.Content})
}

// OpenAIDelta holds the per-chunk delta in an OpenAI stream.
type OpenAIDelta struct {
	Role             string                  `json:"role"`
	Content          string                  `json:"content"`
	ReasoningContent string                  `json:"reasoning_content"`
	Reasoning        string                  `json:"reasoning"`
	ReasoningDetails []OpenAIReasoningDetail `json:"reasoning_details,omitempty"`
	ToolCalls        []OpenAIToolCallStream  `json:"tool_calls"`
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
	Input     jsontext.Value     `json:"input,omitempty"`
	ToolUseID string             `json:"tool_use_id,omitempty"`
	Content   jsontext.Value     `json:"content,omitempty"`
}

// ClaudeImageSource holds base64 image data.
type ClaudeImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// ClaudeMessage is a single message in a Claude request.
type ClaudeMessage struct {
	Role    string         `json:"role"`
	Content jsontext.Value `json:"content"`
}

// ClaudeTool describes a tool in a Claude request.
type ClaudeTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema jsontext.Value `json:"input_schema,omitempty"`
	// Strict is not an Anthropic-native field; it is carried through translation
	// so a client instruction survives a Claude<->OpenAI hop (parity with
	// decolua/9router#4607). Pointer + omitempty keeps "unset" distinguishable
	// from an explicit false, which is a real client instruction. A non-boolean
	// value is dropped rather than fatal — see UnmarshalStrict.
	Strict *bool `json:"strict,omitempty"`
}

// UnmarshalStrict decodes a tool strict flag, reporting whether one was
// actually present. Upstream reads it behind a `typeof === "boolean"` guard,
// so a non-boolean is ignored, not an error; a plain *bool would instead fail
// the whole request body on `"strict":"yes"`.
func UnmarshalStrict(data []byte) (*bool, error) {
	var v bool
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, nil //nolint:nilerr // non-boolean means "unset"
	}
	return &v, nil
}

// claudeToolJSON mirrors ClaudeTool with the strict field left raw, so a
// non-boolean value can be dropped instead of aborting the decode.
type claudeToolJSON struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema jsontext.Value `json:"input_schema,omitempty"`
	Strict      jsontext.Value `json:"strict,omitempty"`
}

// UnmarshalJSON decodes a Claude tool, dropping a non-boolean strict.
func (t *ClaudeTool) UnmarshalJSON(data []byte) error {
	var raw claudeToolJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.Name, t.Description, t.InputSchema = raw.Name, raw.Description, raw.InputSchema
	if len(raw.Strict) > 0 {
		strict, err := UnmarshalStrict(raw.Strict)
		if err != nil {
			return err
		}
		t.Strict = strict
	}
	return nil
}

// ClaudeToolChoice represents the tool_choice field.
type ClaudeToolChoice struct {
	Type string `json:"type"`
	// DisableParallelToolUse is Claude's single-tool-call policy: the model may
	// emit at most one tool call per turn. It is the counterpart of OpenAI's
	// parallel_tool_calls:false (parity with decolua/9router#4581). Plain bool
	// with omitempty: only an explicit true is worth putting on the wire.
	DisableParallelToolUse bool `json:"disable_parallel_tool_use,omitempty"`
	Name string `json:"name,omitempty"`
}

// ClaudeThinking represents the thinking configuration in a Claude request.
type ClaudeThinking struct {
	Type   string `json:"type"`
	Budget int    `json:"budget_tokens,omitempty"`
}

type ClaudeOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// ClaudeRequest is the full Claude /v1/messages request body.
type ClaudeRequest struct {
	Model        string              `json:"model"`
	Messages     []ClaudeMessage     `json:"messages"`
	System       jsontext.Value      `json:"system,omitempty"`
	Temperature  *float64            `json:"temperature,omitempty"`
	MaxTokens    *int                `json:"max_tokens,omitempty"`
	Thinking     *ClaudeThinking     `json:"thinking,omitempty"`
	OutputConfig *ClaudeOutputConfig `json:"output_config,omitempty"`
	Tools        []ClaudeTool        `json:"tools,omitempty"`
	ToolChoice   *jsontext.Value     `json:"tool_choice,omitempty"`
	Stream       bool                `json:"stream,omitempty"`
}

// OpenAIRequest is the translated OpenAI-compatible request body.
type OpenAIRequest struct {
	Model               string          `json:"model"`
	Messages            []OpenAIMessage `json:"messages"`
	Temperature         *float64        `json:"temperature,omitempty"`
	MaxTokens           *int            `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	Tools               []OpenAITool    `json:"tools,omitempty"`
	ToolChoice          any             `json:"tool_choice,omitempty"`
	// ParallelToolCalls carries OpenAI's single-tool-call policy. Pointer so an
	// absent field stays absent while an explicit false still serialises.
	ParallelToolCalls *bool `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	Stream              bool            `json:"stream,omitempty"`
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
	File     *OpenAIFile     `json:"file,omitempty"`
}

// OpenAIFile holds inline file/document data (e.g. data:application/pdf;base64,...).
type OpenAIFile struct {
	FileData string `json:"file_data"`
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
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  jsontext.Value `json:"parameters,omitempty"`
	// Strict preserves an explicit client strict mode in both directions
	// (parity with decolua/9router#4607/#4543/#4573). Pointer + omitempty keeps
	// "unset" distinguishable from an explicit false.
	Strict *bool `json:"strict,omitempty"`
}
