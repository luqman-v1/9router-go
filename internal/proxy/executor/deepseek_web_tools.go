package executor

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
)

// Tool calling on this upstream is not a protocol feature, it is a convention
// the model happens to follow.
//
// There is no `tools[]` parameter on /api/v0/chat/completion and no tool-call
// topic in the event stream. A model told a tool exists answers by *typing* a
// JSON object, one character at a time, through ordinary APPEND operations on the
// fragment content. Everything here exists because of that: a client treating
// the result as a normal streaming answer would hold
// `{"name":"get_weather","parameters":{"` with no way to tell a truncated tool
// call from prose.
//
// So the two paths are kept strictly apart. An ordinary chat streams as it always
// did and never enters this code. A request carrying `tools[]` is buffered to
// completion first, so what gets parsed is always whole JSON — the same guarantee
// the native API gives, since it does not stream tool arguments either.

// deepSeekToolSpec is one declared tool.
//
// `Parameters` is raw JSON because clients disagree about it: the OpenAI shape
// sends a JSON Schema object, while several SDKs stringify it first. Typing it as
// a string would reject the common form outright, so both are accepted and
// normalised where the schema is rendered.
type deepSeekToolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  jsontext.Value `json:"parameters"`
	} `json:"function"`
}

// UnmarshalJSON accepts both tool declaration spellings and folds them into the
// same shape, so nothing downstream has to know which one arrived.
//
// OpenAI wraps the declaration: {"type":"function","function":{...}}. The
// Claude/Anthropic spelling puts name, description and input_schema at the top
// level with no wrapper, and clients send it just as readily.
//
// The flat shape used to parse into a spec whose Function.Name was empty. The
// prompt builder skips those, so the model was told about no tools at all,
// reached for its native DSML instead, and the markup reached the client as if
// it were the answer — the client had no call to run and no question answered.
func (s *deepSeekToolSpec) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type     string `json:"type"`
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  jsontext.Value `json:"parameters"`
		} `json:"function"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema jsontext.Value `json:"input_schema"`
		Parameters  jsontext.Value `json:"parameters"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("deepseek-web: parse tool declaration: %w", err)
	}

	s.Type = raw.Type
	s.Function.Name = raw.Function.Name
	s.Function.Description = raw.Function.Description
	s.Function.Parameters = raw.Function.Parameters

	// The flat fields only fill what the wrapper left empty, so a declaration
	// carrying both spellings resolves to the nested one.
	if s.Function.Name == "" {
		s.Function.Name = raw.Name
	}
	if s.Function.Description == "" {
		s.Function.Description = raw.Description
	}
	if len(s.Function.Parameters) == 0 {
		s.Function.Parameters = raw.InputSchema
	}
	if len(s.Function.Parameters) == 0 {
		s.Function.Parameters = raw.Parameters
	}
	return nil
}

// deepSeekToolRequest is one call recovered from the model's answer.
type deepSeekToolRequest struct {
	Name      string
	Arguments string
	// Raw is the exact object text that was parsed. It lets the caller recover
	// whatever the model wrote around the object instead of discarding it.
	Raw string
}

// deepSeekSchemaText renders a declared schema for the prompt, accepting either
// shape and falling back to a permissive one. A schema that cannot be read is
// replaced rather than dropped: a less constrained call is recoverable, while a
// tool the model was never told about is not.
func deepSeekSchemaText(raw jsontext.Value) string {
	if len(raw) == 0 {
		return `{"type":"object","properties":{}}`
	}
	// A stringified schema arrives as a JSON string, not an object.
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if trimmed := strings.TrimSpace(asString); deepSeekValidJSON(trimmed) {
			return trimmed
		}
		return `{"type":"object","properties":{}}`
	}
	if deepSeekValidJSON(string(raw)) {
		return string(raw)
	}
	return `{"type":"object","properties":{}}`
}

// deepSeekBoundSchemaText renders a declared schema with the request's nonce
// bound into it as a const property, mirroring upstream's
// serializeDeepSeekToolPrompt.
//
// The binding is injected into the schema rather than only described in prose:
// a model reproducing the schema it was shown reproduces the binding, and one
// that copies a schema from an earlier turn copies a nonce that no longer
// matches. With no nonce the schema is rendered unchanged, which is the
// backward-compatible path for a caller that has not minted one.
func deepSeekBoundSchemaText(raw jsontext.Value, nonce ...string) string {
	rendered := deepSeekSchemaText(raw)
	if len(nonce) == 0 || nonce[0] == "" {
		return rendered
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(rendered), &schema); err != nil {
		return rendered
	}
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	}
	properties["_nonce"] = map[string]any{"type": "string", "const": nonce[0]}
	required, _ := schema["required"].([]any)
	names := make([]string, 0, len(required)+1)
	for _, value := range required {
		if name, ok := value.(string); ok {
			names = append(names, name)
		}
	}
	names = append(names, "_nonce")
	slices.Sort(names)
	schema["properties"] = properties
	schema["required"] = names
	encoded, err := json.Marshal(schema)
	if err != nil {
		return rendered
	}
	return string(encoded)
}

// deepSeekRenderTools folds the declared tools into prompt text.
//
// The schema is emitted as JSON rather than prose because a model reproducing a
// schema it has only seen in prose returns a differently-shaped object more often
// than not.
//
// Every schema carries a `_nonce` property bound to this request's nonce, and the
// instructions require it in the reply. The binding is the difference between a
// tool call and a quoted one: a client that pastes an example envelope into the
// chat has shown the model a shape it must not repeat, and only a value the
// client never sees tells the parser which envelope is the model's own.
func deepSeekRenderTools(tools []deepSeekToolSpec, nonce ...string) string {
	var b strings.Builder
	b.WriteString("You have access to the following tools.\n\n<tools>\n")
	for _, tool := range tools {
		if tool.Function.Name == "" {
			continue
		}
		fmt.Fprintf(&b, "{\"name\": %q, \"description\": %q, \"parameters\": %s}\n",
			tool.Function.Name, tool.Function.Description,
			deepSeekBoundSchemaText(tool.Function.Parameters, nonce...))
	}
	b.WriteString("</tools>\n\n")
	b.WriteString("To use a tool, reply with a single JSON object and nothing else: " +
		`{"name": "<tool name>", "arguments": {<arguments matching that tool's parameters>}}` + ". " +
		"Do not wrap it in prose or a code fence.")
	b.WriteString("\n\nThe tool syntax above is the only one this client understands. " +
		"Native markup is not transmitted: a reply made of `invoke`, `parameter` or " +
		"`calls` elements — including any spelled with a DSML namespace — is discarded " +
		"unread, and you will appear to have answered nothing.\n")
	if len(nonce) > 0 && nonce[0] != "" {
		b.WriteString("\n\nEvery tool call must carry the secret binding " +
			`"_nonce": "` + nonce[0] + `" inside its arguments object, exactly as shown. ` +
			"A call without it, or with a different value, is rejected and the turn is lost.\n")
	}
	b.WriteString("\n\nRules for using tools:\n")
	// Without this a model reads its own tool list as a description of what it
	// lacks and refuses the request — "I cannot check live weather, I have no
	// such capability" — even though the tool it needs is right there. Stating
	// that the tools below are installed and runnable is what turns that refusal
	// into the call the user asked for.
	b.WriteString("- The tools above are installed and runnable. You can use them; a " +
		"request is not outside your abilities because it needs one.\n")
	b.WriteString("- Never answer that you lack a capability the list above provides. " +
		"Call the tool instead.\n")
	b.WriteString("- If a request needs information you do not have, call a tool that " +
		"can fetch it before answering.\n")
	// A model that cannot see the difference between itself and its tools
	// refuses with "I have no internet access" while a command tool sits in the
	// list able to fetch exactly that. Only the tools need the network.
	b.WriteString("- A tool that runs commands executes on a real machine with network " +
		"access. \"I have no internet access\" is not a reason to refuse while such " +
		"a tool is declared — the tool has it, even when you do not.\n")
	b.WriteString("- A line reading \"[called <name> with <arguments>]\" means you already " +
		"made that call and its result is the following \"Tool result\" line. Do not repeat it.\n")
	b.WriteString("- Once you have a tool result for what was asked, answer the user in plain " +
		"text. Calling a tool again for the same information is an error.\n")
	// A tool result is the only evidence the model has. Without this rule it
	// fills the gap from memory and presents the numbers as if the call had
	// returned them — a failed command followed by a confident table is the
	// failure mode, and the user cannot tell the data is invented.
	b.WriteString("- A \"Tool result\" line is your only source of fact. Never state a " +
		"figure, date, temperature, price or count that is not in one of those lines, " +
		"and never present invented data as though a tool returned it.\n")
	b.WriteString("- When a tool result is empty, an error, or says nothing was found, " +
		"say so plainly. Do not substitute what you recall, and do not describe output " +
		"that was not returned.\n")
	b.WriteString("- Only call a tool when you actually need data you do not already have.\n")
	b.WriteString("- If several calls are needed, put them all in one reply rather than " +
		"answering one at a time.\n")
	return b.String()
}

// deepSeekExtractToolCalls recovers every tool call from the model's answer.
//
// The prompt asks for JSON and the model usually complies, but these models also
// have a native tool syntax they reach for on their own. Both are read here, and
// DSML is tried first: it is unambiguous, where a JSON-shaped object sitting in
// prose has to be guessed at.
//
// A plural answer, not a singular one: a model asked about two cities writes two
// invokes in a single reply. Returning only the first would cost the client a
// round trip per call and leave the rest of the block in the answer as markup.
//
// It is deliberately conservative. Every call must name one of the tools the
// client declared, in either dialect. Anything else — prose, an undeclared name,
// malformed JSON — returns nothing and reaches the client as ordinary content.
// Guessing here would turn a failed parse into an invocation the client then
// acts on.
func deepSeekExtractToolCalls(text string, tools []deepSeekToolSpec, nonce ...string) []deepSeekToolRequest {
	binding := ""
	if len(nonce) > 0 {
		binding = nonce[0]
	}
	raw := strings.TrimSpace(text)
	if deepSeekHasDSML(raw) {
		return deepSeekExtractDSML(raw, tools, binding)
	}

	// Every call in the answer, not just the first. A model told two cities are
	// involved writes two objects, and returning one leaves the other sitting in
	// the answer as text the user reads as a leaked fragment.
	calls := deepSeekJSONToolCalls(raw, tools)
	if len(calls) == 0 {
		return nil
	}
	// The binding contract: a call that carries `_nonce` must carry this
	// request's, and must satisfy the schema it was declared with. Upstream
	// applies it to every call in the answer and rejects the answer wholesale on
	// the first failure, so one copied envelope cannot cost the client the rest
	// of its calls.
	cleaned, ok := deepSeekFinalizeBoundCalls(calls, tools, binding)
	if !ok {
		return nil
	}
	return cleaned
}

// deepSeekJSONToolCalls reads every JSON object in the answer as a tool call.
func deepSeekJSONToolCalls(answer string, tools []deepSeekToolSpec) []deepSeekToolRequest {
	objects := deepSeekFindJSONObjects(answer)
	calls := make([]deepSeekToolRequest, 0, len(objects))
	for _, obj := range objects {
		if call, ok := deepSeekParseJSONCall(obj, tools); ok {
			calls = append(calls, call)
		}
	}
	if len(calls) == 0 {
		return nil
	}
	return calls
}

// deepSeekParseJSONCall reads one JSON object as a tool call.
func deepSeekParseJSONCall(obj string, tools []deepSeekToolSpec) (deepSeekToolRequest, bool) {
	var call struct {
		Name      string         `json:"name"`
		Arguments jsontext.Value `json:"arguments"`
		// Some models write "parameters" instead of "arguments"; both are read so
		// a correct call is not discarded over a key name.
		Params jsontext.Value `json:"parameters"`
	}
	if err := json.Unmarshal([]byte(obj), &call); err != nil {
		return deepSeekToolRequest{}, false
	}

	arguments := call.Arguments
	if len(arguments) == 0 {
		arguments = call.Params
	}
	if call.Name == "" || !deepSeekToolDeclared(call.Name, tools) {
		return deepSeekToolRequest{}, false
	}

	encoded := "{}"
	if len(arguments) > 0 {
		if !deepSeekValidJSON(string(arguments)) {
			return deepSeekToolRequest{}, false
		}
		encoded = string(arguments)
	}
	return deepSeekToolRequest{Name: call.Name, Arguments: encoded, Raw: obj}, true
}

// deepSeekToolDeclared reports whether the client asked for this tool. A model
// inventing a tool name is the most damaging failure mode here, so it is
// rejected rather than forwarded.
func deepSeekToolDeclared(name string, tools []deepSeekToolSpec) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}

// deepSeekValidJSON reports whether s is one complete JSON value.
//
// There is no exported validity predicate, so this round-trips through a scratch
// value. It matters because a truncated tool call is the failure this file exists
// to prevent: a streaming reader accepts `{"name":"x","arguments":{"` and
// silently yields a half-built call.
func deepSeekValidJSON(s string) bool {
	var scratch jsontext.Value
	return json.Unmarshal([]byte(s), &scratch) == nil
}

// deepSeekToolCallID mints a call id that is unique within the process.
//
// The clock is not a source of uniqueness here. The previous version derived the
// id from time.Now().UnixNano() % 1_000_000, and two calls minted in the same
// turn landed on the same value every time: a Windows clock advances in coarse
// steps of hundreds of microseconds, so both calls were stamped identically. A
// client that groups parallel calls by id — which is the natural reading of a
// field called `id` — then merged two tool calls into one, and the concatenated
// argument objects were rejected before any of them ran ("Unexpected trailing
// characters at position 2, Raw JSON: {}{}").
//
// A counter is the right tool: it cannot repeat, and the name is kept in the id
// so a log line names the tool that was called.
func deepSeekToolCallID(name string) string {
	return "call_" + name + "_" + strconv.FormatUint(deepSeekToolCallSeq.Add(1), 10)
}

// deepSeekToolCallSeq numbers every call the process mints. Monotonic, so an id
// is never reused even across requests.
var deepSeekToolCallSeq atomic.Uint64

// deepSeekSurroundingText returns what the model wrote before and after the tool
// calls, with the calls themselves removed.
//
// This is what stops an agent from seeing an empty stop. A model that says "Let me
// check that for you." and then emits its call has answered twice over; the
// wrapper used to keep only the second half, so the client received
// `content: null` and concluded the model had said nothing.
//
// Only text is meant to come back. The model writes its calls wrapped — a DSML
// answer puts the invokes inside a `calls` element — and that wrapper is markup,
// not a preamble. Leaving it in is how a client ends up showing the user an empty
// `<｜｜DSML｜｜ calls></｜｜DSML｜｜ calls>` under their own question: the calls ran
// correctly and the tags that enclosed them were handed over as assistant text.
func deepSeekSurroundingText(answer string, raws ...string) string {
	if answer == "" || len(raws) == 0 {
		return ""
	}

	remaining := answer
	surrounding := ""
	for _, raw := range raws {
		if raw == "" {
			continue
		}
		idx := strings.Index(remaining, raw)
		if idx < 0 {
			continue
		}
		surrounding += remaining[:idx]
		remaining = remaining[idx+len(raw):]
	}
	surrounding += remaining

	surrounding = strings.TrimSpace(deepSeekDsmlStripped(surrounding))
	surrounding = deepSeekStripStrayToolTags(surrounding)
	surrounding = strings.TrimSpace(strings.Trim(surrounding, "`"))
	return strings.TrimSpace(strings.TrimPrefix(surrounding, "json"))
}

// deepSeekFindJSONObjects returns every balanced top-level JSON object in s.
//
// Balanced rather than greedy, because a fenced answer holds braces in the prose
// ("use {x} here") and a regex would run from the first one to the last. Each
// returned slice is one object alone, so the caller can keep everything outside
// them as the model's prose.
//
// All of them, because a model that was asked about two things writes two
// objects: returning one left the other in the answer as a fragment the user
// reads as a leak.
func deepSeekFindJSONObjects(s string) []string {
	var objects []string
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		end := deepSeekMatchBrace(s, i)
		if end <= i {
			continue
		}
		candidate := s[i : end+1]
		if !deepSeekValidJSON(candidate) {
			continue
		}
		objects = append(objects, candidate)
		// Skip past the object rather than descending into it: braces between
		// these two indices are its own, and a nested object is not a call.
		i = end
	}
	return objects
}

// deepSeekMatchBrace returns the index of the '}' closing the '{' at start, or
// -1. Quotes and escapes are tracked so a brace inside a string does not end
// the object early.
func deepSeekMatchBrace(s string, start int) int {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// Braces inside a string are literal text.
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
