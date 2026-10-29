package executor

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strconv"
	"strings"
)

// deepSeekDefaultMaxPromptChars bounds the rendered prompt, matching the
// sibling client's maxPromptChars default.
//
// This is an account-protection bound, not a model one: the sibling client's
// maintainer accounts were rate-limited on the days their conversations grew
// past ~290k tokens, because a transcript no human types at that density is
// read as volume, and volume is what the account-level enforcement measures.
// A browser user hits it by starting a new chat; a gateway hits the same
// reset through the session store, whose tail logic cannot treat a clamped
// prompt as an extension of the previous one and therefore opens a fresh
// session — the same outcome, arrived at honestly.
const deepSeekDefaultMaxPromptChars = 400_000

// deepSeekMaxPromptChars reads the per-connection bound. Zero disables it;
// the default is the account-protection value above.
func deepSeekMaxPromptChars(connData map[string]any) int {
	if connData == nil {
		return deepSeekDefaultMaxPromptChars
	}
	switch value := connData["maxPromptChars"].(type) {
	case int:
		if value >= 0 {
			return value
		}
	case float64:
		if value >= 0 {
			return int(value)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return deepSeekDefaultMaxPromptChars
}

// deepSeekClampPrompt keeps the last limit characters of the prompt, cut at a
// paragraph boundary when one falls early in the dropped head, so the recent
// turns survive whole rather than as a severed half-sentence.
//
// The cut is deterministic for a given prompt, which is all the session
// store's tail logic needs: a clamped prompt either still extends what was
// sent or it does not, and a prompt that no longer extends it starts a fresh
// session, exactly as a diverged one does.
func deepSeekClampPrompt(prompt string, limit int) string {
	if limit <= 0 || len(prompt) <= limit {
		return prompt
	}
	head := prompt[len(prompt)-limit:]
	if cut := strings.Index(head, "\n\n"); cut >= 0 && cut < limit/2 {
		head = head[cut+len("\n\n"):]
	}
	return head
}

// deepSeekMessage is the inbound OpenAI message shape. Content and tool_calls
// stay raw JSON so a field this prompt builder does not read costs no
// intermediate allocation on a long agent transcript.
type deepSeekMessage struct {
	Role       string         `json:"role"`
	Content    jsontext.Value `json:"content"`
	ToolCalls  jsontext.Value `json:"tool_calls"`
	ToolCallID string         `json:"tool_call_id"`
	Name       string         `json:"name"`
}

// deepSeekTurnLine is one rendered line of the replayed conversation.
type deepSeekTurnLine struct {
	role string
	text string
}

// deepSeekDefaultAutoHistoryWindow bounds the auto-replayed trajectory when a
// connection configures no window of its own, matching upstream's
// DEFAULT_AUTO_HISTORY_WINDOW.
//
// This is the difference between a chat provider and an agentic one for this
// upstream. /api/v0/chat/completion accepts a single `prompt` string and no
// message array, so a whole conversation has to be folded into one. Sending
// only the last user message makes an agent client silently lose the original
// task after a couple of tool-result turns, because only the newest line ever
// reaches the model.
const deepSeekDefaultAutoHistoryWindow = 20

// deepSeekMessageText extracts the plain text of one message. This format has
// no image or attachment slot, so only text parts survive; non-text parts are
// dropped rather than serialized as JSON the model would have to be told to
// ignore.
func deepSeekMessageText(content jsontext.Value) string {
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return asString
	}

	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			b.WriteString(part.Text)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// deepSeekMessagesToPrompt renders an OpenAI conversation as the single prompt
// string the web API accepts.
//
// System prompts lead, the conversation is replayed as a role-tagged transcript
// when it is genuinely multi-turn, and a single-turn request collapses to just
// its user message. Tool results have no native slot in this format, so they are
// folded into the transcript as labelled text: without that the model never sees
// what its tool returned, and either re-calls it forever or claims it has no
// information.
//
// historyWindow bounds the replayed turns. Zero means auto: a genuinely
// multi-turn conversation replays up to deepSeekDefaultAutoHistoryWindow turns,
// while a single-turn one sends only its user message, so an ordinary chat turn
// is not inflated by a transcript it does not need.
//
// transcript forces the role-tagged form even for a single-turn conversation.
// Session persistence needs it: the rendering of one conversation has to have
// the same shape from turn to turn, or each turn's prompt is a different
// document and none can be expressed as an extension of the previous one. The
// plain path leaves it off, which keeps its prompt byte-for-byte what it was.
func deepSeekMessagesToPrompt(messages []deepSeekMessage, historyWindow int, transcript bool) string {
	if len(messages) == 0 {
		return ""
	}

	var systemParts []string
	var conversation []deepSeekTurnLine
	toolNames := make(map[string]string)
	lastUser := ""

	for _, message := range messages {
		text := strings.TrimSpace(deepSeekMessageText(message.Content))
		switch message.Role {
		case "system":
			if text != "" {
				systemParts = append(systemParts, text)
			}
		case "user", "assistant":
			if message.Role == "user" {
				if text != "" {
					conversation = append(conversation, deepSeekTurnLine{role: "user", text: text})
				}
				lastUser = text
				deepSeekCollectToolNames(message.ToolCalls, toolNames)
				break
			}
			conversation = deepSeekAppendAssistant(conversation, text, message.ToolCalls)
			deepSeekCollectToolNames(message.ToolCalls, toolNames)
		case "tool":
			if text == "" {
				continue
			}
			name := message.Name
			if resolved, ok := toolNames[message.ToolCallID]; ok && resolved != "" {
				name = resolved
			}
			if name == "" {
				name = "tool"
			}
			conversation = append(conversation, deepSeekTurnLine{role: "tool", text: "(" + name + ") " + text})
		}
	}

	var parts []string
	if len(systemParts) > 0 {
		parts = append(parts, strings.Join(systemParts, "\n\n"))
	}

	// Transcript mode renders the whole conversation, untruncated: the upstream
	// session is the memory in that mode, and only the tail of this prompt is
	// ever sent, so a local window would serve no purpose while making each
	// turn's prompt shorter than the last and unusable as an extension of it.
	if transcript {
		parts = append(parts, deepSeekRenderTranscript(conversation))
		return strings.Join(parts, "\n\n")
	}

	window := historyWindow
	if window <= 0 && len(conversation) > 1 {
		window = deepSeekDefaultAutoHistoryWindow
	}

	if window > 0 && len(conversation) > 1 {
		recent := conversation
		if len(recent) > window {
			recent = recent[len(recent)-window:]
		}
		parts = append(parts, deepSeekRenderTranscript(recent))
	} else if lastUser != "" {
		parts = append(parts, lastUser)
	}

	return strings.Join(parts, "\n\n")
}

// deepSeekRenderTranscript renders replayed turns with the role labels the web
// model was trained on.
func deepSeekRenderTranscript(turns []deepSeekTurnLine) string {
	lines := make([]string, 0, len(turns))
	for _, turn := range turns {
		switch turn.role {
		case "assistant":
			lines = append(lines, "Assistant: "+turn.text)
		case "tool":
			lines = append(lines, "Tool result "+turn.text)
		default:
			lines = append(lines, "User: "+turn.text)
		}
	}
	return strings.Join(lines, "\n\n")
}

// deepSeekToolCall is one entry of an assistant turn's tool_calls array.
type deepSeekToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// deepSeekCollectToolNames maps each tool_call id to the function it invoked, so
// a later `tool` turn can be labelled with the name the model itself chose
// rather than a generic one.
func deepSeekCollectToolNames(toolCalls jsontext.Value, into map[string]string) {
	if len(toolCalls) == 0 {
		return
	}
	var calls []deepSeekToolCall
	if err := json.Unmarshal(toolCalls, &calls); err != nil {
		return
	}
	for _, call := range calls {
		if call.ID != "" && call.Function.Name != "" {
			into[call.ID] = call.Function.Name
		}
	}
}

// deepSeekRequestEnvelope is the subset of the inbound request body that changes
// what is sent upstream. Everything else is dropped, because this upstream
// accepts only the fields in deepSeekCompletionRequest.
type deepSeekRequestEnvelope struct {
	Model    string            `json:"model"`
	Messages []deepSeekMessage `json:"messages"`
	// Tools are carried only so the executor can tell a tool-calling request
	// from an ordinary one; they never reach the upstream as a parameter,
	// because this endpoint has no field for them.
	Tools           []deepSeekToolSpec `json:"tools"`
	ThinkingEnabled bool               `json:"thinking_enabled"`
	Thinking        bool               `json:"thinking"`
	ReasoningEffort string             `json:"reasoning_effort"`
	SearchEnabled   bool               `json:"search_enabled"`
	Search          bool               `json:"search"`
	WebSearch       bool               `json:"web_search"`
}

// deepSeekModelOptions is how a requested model maps onto this upstream's two
// switches. It has no per-model catalogue of its own: the web UI exposes one
// model with two behaviours, and the requested id is where a client's choice
// survives.
type deepSeekModelOptions struct {
	modelType string
	thinking  bool
	search    bool
}

// resolveDeepSeekModelOptions derives the upstream switches from the requested
// model and the request body, matching upstream's resolveModelOptions: "pro" and
// "expert" pick the reasoning model, and think/reason/r1 or an explicit
// reasoning_effort turn reasoning on; "search" or an explicit search flag turns
// web search on.
func resolveDeepSeekModelOptions(model string, body deepSeekRequestEnvelope) deepSeekModelOptions {
	name := strings.ToLower(model)
	options := deepSeekModelOptions{
		modelType: "default",
		thinking: strings.Contains(name, "r1") ||
			strings.Contains(name, "think") ||
			strings.Contains(name, "reason") ||
			body.ThinkingEnabled || body.Thinking || body.ReasoningEffort != "",
		search: strings.Contains(name, "search") ||
			body.SearchEnabled || body.Search || body.WebSearch,
	}
	if strings.Contains(name, "pro") || strings.Contains(name, "expert") {
		options.modelType = "expert"
	}
	return options
}

// deepSeekRenderToolCalls renders an assistant turn's tool_calls for the
// transcript.
//
// The web API has no field for tool calls, so the only way the model can know it
// already made a call is to read it back as text. Rendering it is what stops an
// agent loop from repeating the same call every turn: without the line, the
// replay shows a result with no cause and the model infers it never called.
func deepSeekRenderToolCalls(toolCalls jsontext.Value) string {
	if len(toolCalls) == 0 {
		return ""
	}
	var calls []deepSeekToolCall
	if err := json.Unmarshal(toolCalls, &calls); err != nil {
		return ""
	}
	lines := make([]string, 0, len(calls))
	for _, call := range calls {
		if call.Function.Name == "" {
			continue
		}
		line := "[called " + call.Function.Name
		if call.Function.Arguments != "" {
			line += " with " + call.Function.Arguments
		}
		lines = append(lines, line+"]")
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// deepSeekAppendAssistant records an assistant turn, repairing one that the
// extractor let through as text.
//
// When the model answers with a tool call the gateway recognises, the call is
// returned to the client and the transcript reads it back as a `[called …]`
// line. But when the extractor cannot recognise the call — an answer built out of
// invoke/parameter elements is one that can happen — the text is passed through
// as content, and the client sends it back in the next request. Replaying it
// verbatim teaches the model that markup is how this conversation calls tools:
// it sees its own unparsed call in the transcript and repeats it, every turn,
// with the round always one step behind what the client actually executed.
//
// So a recognised call is rendered from tool_calls, and any leftover text is
// dropped as the scaffolding it is. Text carrying no markup is kept as before.
func deepSeekAppendAssistant(conversation []deepSeekTurnLine, text string, toolCalls jsontext.Value) []deepSeekTurnLine {
	call := deepSeekRenderToolCalls(toolCalls)

	kept := deepSeekDsmlStripped(text)
	// A JSON call object is the other dialect the extractor reads, and one that
	// reached the client as content is replayed here for the same reason markup
	// is not: it shows the model that answering with a bare object is accepted.
	// Only the object goes, and only when it really is a call — an answer that
	// merely contains a JSON fragment the extractor rejected is left alone.
	for _, called := range deepSeekFindToolCallObjects(kept) {
		kept = deepSeekRemoveObject(kept, called)
	}

	if call != "" {
		if kept != "" {
			conversation = append(conversation, deepSeekTurnLine{role: "assistant", text: kept})
		}
		return append(conversation, deepSeekTurnLine{role: "assistant", text: call})
	}
	if kept != "" {
		return append(conversation, deepSeekTurnLine{role: "assistant", text: kept})
	}
	// Nothing but markup: the turn was a call, and saying so beats leaving a gap
	// the model reads as never having called anything.
	if summary := deepSeekMarkupCallSummary(text); summary != "" {
		return append(conversation, deepSeekTurnLine{role: "assistant", text: summary})
	}
	return conversation
}

// deepSeekDsmlStripped removes a DSML block if the text carries one.
func deepSeekDsmlStripped(text string) string {
	if !deepSeekHasDSML(text) {
		return text
	}
	return deepSeekStripDSML(text)
}

// deepSeekFindToolCallObjects returns every JSON object in the text that really
// is a tool call, so the transcript can drop them.
//
// The test is the object's shape, not the answer's: a fragment that happens to
// be JSON but declares no tool stays where it is.
func deepSeekFindToolCallObjects(text string) []string {
	var objects []string
	for _, obj := range deepSeekFindJSONObjects(text) {
		var call struct {
			Name string         `json:"name"`
			Args jsontext.Value `json:"arguments"`
			Para jsontext.Value `json:"parameters"`
		}
		if err := json.Unmarshal([]byte(obj), &call); err != nil || call.Name == "" {
			continue
		}
		if len(call.Args) == 0 && len(call.Para) == 0 {
			continue
		}
		objects = append(objects, obj)
	}
	return objects
}

// deepSeekRemoveObject deletes the first occurrence of an object from the text.
func deepSeekRemoveObject(text, obj string) string {
	return strings.TrimSpace(strings.Replace(text, obj, "", 1))
}

// deepSeekMarkupCallSummary describes an assistant turn that was nothing but
// markup, so the transcript shows a call rather than an empty assistant turn.
func deepSeekMarkupCallSummary(text string) string {
	names := make([]string, 0, 2)
	for _, invoke := range deepSeekDSMLInvokes(text) {
		attributes, _, _ := strings.Cut(invoke, ">")
		if name := deepSeekDSMLAttribute(attributes, "name"); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "[called " + strings.Join(names, ", ") + "]"
}
