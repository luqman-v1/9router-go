package executor

import (
	stdjson "encoding/json"
	json "encoding/json/v2"
	"net/http"
	"strings"
)

// The execute bridge carries caller tool calls through models that can only
// emit calls to declared tool names (glm measured live 2026-10-25: it narrates
// and stops instead of calling an undeclared name, while deepseek and mimo
// emit the call directly). Such models are instructed — in the session tool
// protocol the translator injects — to wrap a caller tool call as the declared
// execute tool with code "CALL_TOOL <tool-name> <json-arguments>". The bridge
// rewrites that back into a plain call to the caller's tool name on the way
// out, so the client only ever sees its own spelling. The return trip needs no
// rewriting: the inference gate accepts history tool_calls to undeclared names.
const openCodeCallToolPrefix = "CALL_TOOL "

// parseOpenCodeCallTool decodes the execute-code convention: a tool name
// followed by a valid JSON argument object. Anything else is left alone so a
// genuine Code Mode execute call passes through verbatim.
func parseOpenCodeCallTool(code string) (tool, args string, ok bool) {
	s := strings.TrimSpace(code)
	if !strings.HasPrefix(s, openCodeCallToolPrefix) {
		return "", "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(s, openCodeCallToolPrefix))
	rest = strings.Trim(strings.TrimSpace(rest), "`")
	sep := strings.IndexAny(rest, " \t")
	if sep < 0 {
		return "", "", false
	}
	tool, args = rest[:sep], strings.TrimSpace(rest[sep+1:])
	if tool == "" || !stdjson.Valid([]byte(args)) {
		return "", "", false
	}
	return tool, args, true
}

// bridgeOpenCodeExecuteToolCallsJSON rewrites execute/CALL_TOOL tool calls in
// a non-streaming chat completion body. It returns the input untouched when
// nothing matches.
func bridgeOpenCodeExecuteToolCallsJSON(data []byte) []byte {
	var obj map[string]any
	if json.Unmarshal(data, &obj) != nil {
		return data
	}
	choices, _ := obj["choices"].([]any)
	changed := false
	for _, choice := range choices {
		cm, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		message, _ := cm["message"].(map[string]any)
		if message == nil {
			continue
		}
		calls, _ := message["tool_calls"].([]any)
		for _, call := range calls {
			callMap, ok := call.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := callMap["function"].(map[string]any)
			if fn == nil || fn["name"] != "execute" {
				continue
			}
			raw, _ := fn["arguments"].(string)
			tool, args, ok := parseOpenCodeCallTool(codeFromArgumentsJSON(raw))
			if !ok {
				continue
			}
			fn["name"] = tool
			fn["arguments"] = args
			changed = true
		}
	}
	if !changed {
		return data
	}
	out, err := json.Marshal(obj, json.Deterministic(true))
	if err != nil {
		return data
	}
	return out
}

// codeFromArgumentsJSON reads the {"code": ...} payload out of an execute
// call's arguments string.
func codeFromArgumentsJSON(raw string) string {
	var payload struct {
		Code string `json:"code"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return ""
	}
	return payload.Code
}

// sseToolCallFragment is the streaming tool_calls delta shape; only the
// fields the bridge decision needs are decoded, and passthrough lines are
// never re-serialized from them.
type sseToolCallFragment struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Function *struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type sseChoice struct {
	Index        *int    `json:"index"`
	FinishReason *string `json:"finish_reason"`
	Delta        *struct {
		ToolCalls []sseToolCallFragment `json:"tool_calls"`
	} `json:"delta"`
}

type sseChunk struct {
	Choices []sseChoice `json:"choices"`
}

// pendingExecuteCall assembles one execute-wrapped call whose fragments are
// held back until the call settles, then either rewritten (CALL_TOOL) or
// replayed verbatim.
type pendingExecuteCall struct {
	choiceIndex int
	callIndex   int
	id          string
	arguments   strings.Builder
	rawLines    []string
}

// openCodeExecuteBridgeWriter rewrites execute/CALL_TOOL streaming calls.
// Lines without tool_calls pass through byte-for-byte; tool_call fragments of
// an execute call are held until the stream finishes ([DONE] or a finish
// chunk) and then emitted as one complete rewritten call each.
type openCodeExecuteBridgeWriter struct {
	http.ResponseWriter
	lineBuf  string
	pending  map[int]*pendingExecuteCall
	finished bool
}

// NewOpenCodeExecuteBridgeWriter wraps w for the free chat lanes.
func NewOpenCodeExecuteBridgeWriter(w http.ResponseWriter) http.ResponseWriter {
	if _, already := w.(*openCodeExecuteBridgeWriter); already {
		return w
	}
	return &openCodeExecuteBridgeWriter{ResponseWriter: w, pending: map[int]*pendingExecuteCall{}}
}

func pendingKey(choiceIndex, callIndex int) int { return choiceIndex*10000 + callIndex }

func (w *openCodeExecuteBridgeWriter) Write(p []byte) (int, error) {
	w.lineBuf += string(p)
	for {
		line, rest, found := cutLine(w.lineBuf)
		w.lineBuf = rest
		if !found {
			break
		}
		if err := w.writeLine(line); err != nil {
			// The unconsumed remainder still belongs to the caller's payload;
			// report the consumed prefix length.
			return len(p) - len(w.lineBuf), err
		}
	}
	return len(p), nil
}

func cutLine(s string) (line, rest string, found bool) {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i+1], s[i+1:], true
	}
	return "", s, false
}

func (w *openCodeExecuteBridgeWriter) writeLine(line string) error {
	payload, isData := sseDataPayload(line)
	if !isData {
		_, err := w.ResponseWriter.Write([]byte(line))
		return err
	}
	if payload == "[DONE]" {
		if err := w.flushPending(); err != nil {
			return err
		}
		w.finished = true
		_, err := w.ResponseWriter.Write([]byte(line))
		return err
	}
	var chunk sseChunk
	if json.Unmarshal([]byte(payload), &chunk) != nil {
		return w.raw(line)
	}
	hold := false
	for ci := range chunk.Choices {
		choice := &chunk.Choices[ci]
		if choice.Delta == nil || len(choice.Delta.ToolCalls) == 0 {
			if choice.FinishReason != nil && len(w.pending) > 0 {
				// Hold the finish line until the held calls are flushed ahead
				// of it, keeping finish_reason last as clients expect.
				key := -1 - ci
				w.pending[key] = &pendingExecuteCall{choiceIndex: key, rawLines: []string{line}}
				hold = true
			}
			continue
		}
		for _, frag := range choice.Delta.ToolCalls {
			callIndex := 0
			if frag.Index != nil {
				callIndex = *frag.Index
			}
			choiceIndex := 0
			if choice.Index != nil {
				choiceIndex = *choice.Index
			}
			key := pendingKey(choiceIndex, callIndex)
			if frag.Function != nil && frag.Function.Name == "execute" {
				w.pending[key] = &pendingExecuteCall{choiceIndex: choiceIndex, callIndex: callIndex}
				if frag.ID != "" {
					w.pending[key].id = frag.ID
				}
			}
			if entry, held := w.pending[key]; held {
				var name string
				if frag.Function != nil {
					name = frag.Function.Name
				}
				if name != "" && name != "execute" {
					// A non-execute name on a held slot: a different call
					// reused the index. Replay what was held and pass this
					// fragment through untouched.
					w.replayEntry(entry)
					delete(w.pending, key)
					continue
				}
				if frag.Function != nil {
					entry.arguments.WriteString(frag.Function.Arguments)
				}
				entry.rawLines = append(entry.rawLines, line)
				hold = true
			}
		}
	}
	if hold {
		return nil
	}
	return w.raw(line)
}

func (w *openCodeExecuteBridgeWriter) raw(line string) error {
	_, err := w.ResponseWriter.Write([]byte(line))
	return err
}

func (w *openCodeExecuteBridgeWriter) replayEntry(entry *pendingExecuteCall) {
	for _, raw := range entry.rawLines {
		_ = w.raw(raw)
	}
}

// flushPending emits every held call: rewritten when its assembled code parses
// as the CALL_TOOL convention, verbatim otherwise. Finish lines held last keep
// their trailing position.
func (w *openCodeExecuteBridgeWriter) flushPending() error {
	type flushEntry struct {
		key  int
		call *pendingExecuteCall
	}
	entries := make([]flushEntry, 0, len(w.pending))
	for key, entry := range w.pending {
		entries = append(entries, flushEntry{key, entry})
	}
	// Held finish markers use negative keys (-1 - choiceIndex); sorting by key
	// puts them first, so flip the comparison to flush calls before their
	// finish line.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].key > entries[j-1].key; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	for _, e := range entries {
		delete(w.pending, e.key)
		if e.key < 0 {
			if err := w.raw(e.call.rawLines[0]); err != nil {
				return err
			}
			continue
		}
		if tool, args, ok := parseOpenCodeCallTool(codeFromArgumentsJSON(e.call.arguments.String())); ok {
			if err := w.writeRewrittenCall(e.call, tool, args); err != nil {
				return err
			}
			continue
		}
		w.replayEntry(e.call)
	}
	return nil
}

func (w *openCodeExecuteBridgeWriter) writeRewrittenCall(entry *pendingExecuteCall, tool, args string) error {
	fragment := map[string]any{
		"index": entry.callIndex,
		"id":    entry.id,
		"type":  "function",
		"function": map[string]any{
			"name":      tool,
			"arguments": args,
		},
	}
	delta := map[string]any{"tool_calls": []any{fragment}}
	if entry.id == "" {
		fragment["id"] = "call_" + tool
	}
	chunk := map[string]any{
		"object":  "chat.completion.chunk",
		"choices": []any{map[string]any{"index": entry.choiceIndex, "delta": delta}},
	}
	encoded, err := json.Marshal(chunk, json.Deterministic(true))
	if err != nil {
		w.replayEntry(entry)
		return nil
	}
	return w.raw("data: " + string(encoded) + "\n\n")
}

// Flush forwards to the underlying writer when it supports http.Flusher; held
// calls stay buffered until the stream completes.
func (w *openCodeExecuteBridgeWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// sseDataPayload returns the JSON text of a "data: ..." line, if it is one.
func sseDataPayload(line string) (string, bool) {
	trimmed := strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(trimmed, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")), true
}
