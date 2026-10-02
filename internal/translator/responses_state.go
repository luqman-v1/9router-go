package translator

import (
	"fmt"
	"sort"
	"time"
)

// Responses API item type discriminators. Mirrors upstream
// open-sse/translator/schema/blocks.js RESPONSES_ITEM.
const (
	responsesItemMessage         = "message"
	responsesItemFunctionCall    = "function_call"
	responsesItemCustomToolCall  = "custom_tool_call"
	responsesItemReasoning       = "reasoning"
	responsesItemOutputText      = "output_text"
	responsesItemSummaryText     = "summary_text"
	responsesAssistantRole       = "assistant"
	responsesPrefixFunctionCall  = "fc"
	responsesPrefixCustomTool    = "ctc"
	responsesPrefixMessage       = "msg"
	responsesPrefixReasoningItem = "rs"
)

// ResponsesEvent is one server-sent event of the OpenAI Responses API stream:
// the wire `event:` name plus the JSON object that follows in `data:`.
type ResponsesEvent struct {
	Event string
	Data  map[string]any
}

// ResponsesInputDetails carries Responses-shaped prompt token detail.
type ResponsesInputDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// ResponsesOutputDetails carries Responses-shaped completion token detail.
type ResponsesOutputDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// ResponsesUsage is the usage shape the Responses API expects.
//
// Without it a /v1/responses client never learns the token cost, which leaves
// callers such as the Codex CLI with a "context used" gauge pinned at 0: it
// never auto-compacts, so the session grows until the upstream context limit
// rejects it.
type ResponsesUsage struct {
	InputTokens         int                     `json:"input_tokens"`
	OutputTokens        int                     `json:"output_tokens"`
	TotalTokens         int                     `json:"total_tokens"`
	InputTokensDetails  *ResponsesInputDetails  `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *ResponsesOutputDetails `json:"output_tokens_details,omitempty"`
}

// ResponsesState carries the per-stream translation state for one upstream
// Chat Completions stream being replayed as Responses API events. The field set
// mirrors upstream initState() in open-sse/translator/index.js.
type ResponsesState struct {
	Seq        int64
	ResponseID string
	Created    int64
	Model      string
	Started    bool
	Completed  bool

	// FlushReachesUs mirrors upstream state.targetFormat === FORMATS.OPENAI.
	// It is true when this translator sits on the direct route and therefore
	// does see the terminal null chunk. When the translator runs as the second
	// hop of a pivot that chunk is already gone, so completion cannot be
	// deferred to the flush without swallowing response.completed entirely.
	FlushReachesUs bool

	// CompletionPending reports that finish_reason closed the answer but
	// response.completed is still being held back waiting for the usage
	// trailer. The stream producer needs the flag to know a terminal event is
	// owed to the client and has to be flushed on a deadline rather than on
	// the upstream's cooperation.
	CompletionPending bool

	MsgTextBuf      map[int]string
	MsgItemAdded    map[int]bool
	MsgContentAdded map[int]bool
	MsgItemDone     map[int]bool

	ReasoningID        string
	ReasoningIndex     int
	ReasoningBuf       string
	ReasoningPartAdded bool
	ReasoningDone      bool
	InThinking         bool

	FuncArgsBuf   map[int]string
	FuncNames     map[int]string
	FuncCallIDs   map[int]string
	FuncItemAdded map[int]bool
	FuncArgsDone  map[int]bool
	FuncItemDone  map[int]bool

	CustomToolNames map[string]bool

	Usage *ResponsesUsage

	CompletedOutputItems map[int]map[string]any
}

// InitResponsesState builds the translation state for one stream. model is
// carried through for clients that echo it back; idPrefix seeds the synthetic
// response id until the first upstream chunk supplies its own.
func InitResponsesState(model string, flushReachesUs bool) *ResponsesState {
	now := time.Now()
	return &ResponsesState{
		Created:              now.Unix(),
		Model:                model,
		ResponseID:           fmt.Sprintf("resp_%d", now.UnixMilli()),
		FlushReachesUs:       flushReachesUs,
		ReasoningIndex:       -1,
		MsgTextBuf:           make(map[int]string),
		MsgItemAdded:         make(map[int]bool),
		MsgContentAdded:      make(map[int]bool),
		MsgItemDone:          make(map[int]bool),
		FuncArgsBuf:          make(map[int]string),
		FuncNames:            make(map[int]string),
		FuncCallIDs:          make(map[int]string),
		FuncItemAdded:        make(map[int]bool),
		FuncArgsDone:         make(map[int]bool),
		FuncItemDone:         make(map[int]bool),
		CustomToolNames:      make(map[string]bool),
		CompletedOutputItems: make(map[int]map[string]any),
	}
}

// emit appends one event, stamping it with the next sequence number. Every
// Responses event carries sequence_number and clients rely on it to detect gaps.
func (s *ResponsesState) emit(out *[]ResponsesEvent, eventType string, data map[string]any) {
	s.Seq++
	data["sequence_number"] = s.Seq
	*out = append(*out, ResponsesEvent{Event: eventType, Data: data})
}

// FormatResponsesSSE renders one event as an SSE frame.
func FormatResponsesSSE(ev ResponsesEvent) string {
	return formatSSE(ev.Data)
}

// SetCustomToolNames marks which function names are freeform custom tools, so
// the tool-call helpers emit custom_tool_call_input instead of JSON arguments.
func (s *ResponsesState) SetCustomToolNames(names []string) {
	s.CustomToolNames = make(map[string]bool, len(names))
	for _, name := range names {
		s.CustomToolNames[name] = true
	}
}

func (s *ResponsesState) isCustomTool(name string) bool {
	return name != "" && s.CustomToolNames[name]
}

// recordCompletedOutputItem remembers a finished item for response.completed.
//
// Keyed by output_index so a repeated close overwrites rather than duplicating
// the item, and ordered by output_index so the terminal event replays the items
// in the order they were emitted. Without this, clients that build their final
// result from response.completed (GitHub Copilot CLI, the OpenAI SDK "final
// response" helpers) treat the turn as empty even though the text streamed.
func (s *ResponsesState) recordCompletedOutputItem(outputIndex int, item map[string]any) {
	if s.CompletedOutputItems == nil {
		s.CompletedOutputItems = make(map[int]map[string]any)
	}
	s.CompletedOutputItems[outputIndex] = item
}

// collectCompletedOutputItems returns the recorded items ordered by output_index.
func (s *ResponsesState) collectCompletedOutputItems() []map[string]any {
	if len(s.CompletedOutputItems) == 0 {
		return []map[string]any{}
	}
	items := make([]map[string]any, 0, len(s.CompletedOutputItems))
	for _, idx := range sortedIntKeys(s.CompletedOutputItems) {
		items = append(items, s.CompletedOutputItems[idx])
	}
	return items
}

// sortedIntKeys returns map keys ascending, matching the numeric key order a JS
// Map guarantees. Callers rely on it: events must replay in output_index order.
func sortedIntKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}
