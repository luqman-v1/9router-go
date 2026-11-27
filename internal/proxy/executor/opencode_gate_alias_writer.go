package executor

import (
	"bytes"
	json "encoding/json/v2"
	"net/http"
	"strings"

	"9router/proxy/internal/translator"
)

// openCodeGateAliasRestoringWriter puts the caller's tool names and argument
// keys back after the v2 free gate shaping. It is separate from
// toolNameRestoringWriter because argument-key restoration is new: that writer
// only knows about names and cannot recover `old_string` from `old_text`.
//
// Streaming arguments arrive as fragments, and a fragment boundary can split a
// key. Tool-call fragments are therefore assembled per call index and emitted
// as one complete call when the stream finishes — the same approach the
// execute bridge uses. Non-tool lines pass through byte-for-byte.
type openCodeGateAliasRestoringWriter struct {
	http.ResponseWriter
	aliases translator.OpenCodeGateAliases

	lineBuf    string
	pending    map[int]*pendingGateCall
	finished   bool
	trailingNL int
}

type pendingGateCall struct {
	choiceIndex int
	callIndex   int
	id          string
	name        string
	arguments   strings.Builder
}

// NewOpenCodeGateAliasWriter wraps w only when the gate shaping renamed
// something. Embedding ResponseWriter preserves Header()/WriteHeader(), and
// Flush is forwarded so the SSE paths keep their http.Flusher assertion.
func NewOpenCodeGateAliasWriter(w http.ResponseWriter, aliases translator.OpenCodeGateAliases) http.ResponseWriter {
	if w == nil || !aliases.HasAliases() {
		return w
	}
	if existing, already := w.(*openCodeGateAliasRestoringWriter); already {
		for k, v := range aliases.ToolNames {
			if existing.aliases.ToolNames == nil {
				existing.aliases.ToolNames = map[string]string{}
			}
			existing.aliases.ToolNames[k] = v
		}
		for k, v := range aliases.ParamKeys {
			if existing.aliases.ParamKeys == nil {
				existing.aliases.ParamKeys = map[string]string{}
			}
			existing.aliases.ParamKeys[k] = v
		}
		return w
	}
	return &openCodeGateAliasRestoringWriter{
		ResponseWriter: w,
		aliases:        aliases,
		pending:        map[int]*pendingGateCall{},
	}
}

func (w *openCodeGateAliasRestoringWriter) Write(p []byte) (int, error) {
	trimmed := bytes.TrimSpace(p)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		restored := translator.RestoreOpenCodeGateAliases(p, w.aliases)
		if _, err := w.ResponseWriter.Write(restored); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	w.lineBuf += string(p)
	for {
		line, rest, found := cutLine(w.lineBuf)
		w.lineBuf = rest
		if !found {
			break
		}
		if err := w.writeLine(line); err != nil {
			return len(p) - len(w.lineBuf), err
		}
	}
	return len(p), nil
}

func (w *openCodeGateAliasRestoringWriter) writeLine(line string) error {
	payload, isData := sseDataPayload(line)
	if !isData {
		return w.raw(line)
	}
	if payload == "[DONE]" {
		if err := w.flushPending(); err != nil {
			return err
		}
		w.finished = true
		switch w.trailingNL {
		case 0:
			if err := w.raw("\n\n"); err != nil {
				return err
			}
		case 1:
			if err := w.raw("\n"); err != nil {
				return err
			}
		}
		return w.raw(line)
	}
	// A non-streaming body is a single JSON document, not an SSE frame:
	// restore it in place and pass it through.
	if !strings.HasPrefix(strings.TrimSpace(line), "data:") {
		return w.raw(line)
	}
	var chunk sseChunk
	if json.Unmarshal([]byte(payload), &chunk) != nil {
		return w.raw(line)
	}
	hold := false
	hasToolCall := false
	for ci := range chunk.Choices {
		choice := &chunk.Choices[ci]
		choiceIndex := 0
		if choice.Index != nil {
			choiceIndex = *choice.Index
		}
		if choice.Delta == nil || len(choice.Delta.ToolCalls) == 0 {
			// Hold finish_reason behind the calls it belongs to, as the
			// execute bridge does; clients expect tool_calls before finish.
			if choice.FinishReason != nil && len(w.pending) > 0 {
				key := -1 - choiceIndex
				w.pending[key] = &pendingGateCall{choiceIndex: key, id: line}
				hold = true
			}
			continue
		}
		for _, frag := range choice.Delta.ToolCalls {
			callIndex := 0
			if frag.Index != nil {
				callIndex = *frag.Index
			}
			key := pendingKey(choiceIndex, callIndex)
			hasToolCall = true
			entry, exists := w.pending[key]
			if !exists {
				entry = &pendingGateCall{choiceIndex: choiceIndex, callIndex: callIndex}
				w.pending[key] = entry
			}
			if frag.ID != "" {
				entry.id = frag.ID
			}
			if frag.Function != nil {
				if frag.Function.Name != "" {
					entry.name = frag.Function.Name
				}
				entry.arguments.WriteString(frag.Function.Arguments)
			}
			hold = true
		}
	}
	if hold {
		return nil
	}
	if hasToolCall {
		return w.raw(line)
	}
	// Responses-lane SSE frames have no choices: restore in place (the
	// translator's SSE path keeps the line framing and [DONE] sentinel).
	return w.raw(string(translator.RestoreOpenCodeGateAliases([]byte(line), w.aliases)))
}

func (w *openCodeGateAliasRestoringWriter) raw(line string) error {
	_, err := w.ResponseWriter.Write([]byte(line))
	w.noteTrailing(line)
	return err
}

func (w *openCodeGateAliasRestoringWriter) noteTrailing(line string) {
	trail := 0
	for i := len(line) - 1; i >= 0; i-- {
		if line[i] != '\n' {
			break
		}
		trail++
		if trail == 2 {
			break
		}
	}
	if trail == len(line) {
		w.trailingNL += trail
		if w.trailingNL > 2 {
			w.trailingNL = 2
		}
		return
	}
	w.trailingNL = trail
}

// flushPending emits each assembled call with restored argument keys. A call
// whose arguments are not JSON on their own passes through the token-level
// restorer, which handles the common case of a single complete object.
func (w *openCodeGateAliasRestoringWriter) flushPending() error {
	type flushEntry struct {
		key  int
		call *pendingGateCall
	}
	entries := make([]flushEntry, 0, len(w.pending))
	for key, call := range w.pending {
		entries = append(entries, flushEntry{key, call})
	}
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].key > entries[j-1].key; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	for _, e := range entries {
		delete(w.pending, e.key)
		call := e.call
		if e.key < 0 {
			if err := w.raw(call.id); err != nil {
				return err
			}
			continue
		}
		if call.name == "" && call.arguments.Len() == 0 {
			continue
		}
		args := translator.RestoreOpenCodeGateArgumentString(call.arguments.String(), w.aliases)
		fragment := map[string]any{
			"index": call.callIndex,
			"id":    call.id,
			"type":  "function",
			"function": map[string]any{
				"name":      w.restoreGateName(call.name),
				"arguments": args,
			},
		}
		if call.id == "" {
			fragment["id"] = "call_" + call.name
		}
		chunk := map[string]any{
			"object":  "chat.completion.chunk",
			"choices": []any{map[string]any{"index": call.choiceIndex, "delta": map[string]any{"tool_calls": []any{fragment}}}},
		}
		encoded, err := json.Marshal(chunk, json.Deterministic(true))
		if err != nil {
			continue
		}
		if err := w.raw("data: " + string(encoded) + "\n\n"); err != nil {
			return err
		}
	}
	return nil
}

func (w *openCodeGateAliasRestoringWriter) restoreGateName(name string) string {
	if original, found := w.aliases.ToolNames[name]; found {
		return original
	}
	return name
}

// Flush forwards to the underlying writer when it supports http.Flusher; held
// calls stay buffered until the stream completes.
func (w *openCodeGateAliasRestoringWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
