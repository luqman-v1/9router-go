package chat

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	json "9router/proxy/internal/fastjson"

	"9router/proxy/internal/providers"
)

// A capacity-adapter model is only reached because the request's original model
// could not serve it, and the pool often holds a model with a far smaller
// context window than the one the client was sending to. Sending the full
// history to it fails upstream on length.
//
// stripHistoryForContext trims the conversation to fit by dropping the MIDDLE.
// It preserves every system/instruction message (the head) and the trailing
// user run carrying the media the switch happened for (the tail).
//
// Port of upstream stripHistoryForContext (open-sse/services/capacityAdapter.js).

// charsPerToken is a rough estimate; pulling in a tokenizer dependency for one
// budget check is not worth it. Upstream uses the same constant.
const charsPerToken = 4

// headKeep is how many messages after the system ones are kept verbatim before
// the middle starts being dropped.
const headKeep = 6

// historyBudgetFraction leaves room inside the window for the response.
const historyBudgetFraction = 0.8

// stripHistoryForContext returns the request body trimmed to fit contextWindow.
// The input is returned untouched when nothing needs dropping.
func stripHistoryForContext(body []byte, contextWindow int) []byte {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}

	key, arr, ok := historyArray(req)
	if !ok || len(arr) == 0 {
		return body
	}

	systemMsgs, rest := splitSystemMessages(arr)
	if len(rest) == 0 {
		return body
	}

	tail := trailingUserItems(rest)
	older := rest[:len(rest)-len(tail)]
	if len(older) == 0 {
		return body
	}

	// 80% of the adapter model's window, in characters.
	window := contextWindow
	if window <= 0 {
		window = 200000
	}
	budget := int(float64(window) * historyBudgetFraction * charsPerToken)

	// Keep the first headKeep older turns verbatim; only trim further if even
	// that exceeds the budget.
	keep := min(headKeep, len(older))
	head := older[:keep]
	total := contentLen(systemMsgs) + contentLen(head) + contentLen(tail)

	// Drop head turns from the end (closest to the middle) first.
	for total > budget && len(head) > 0 {
		total -= blockLen(itemContent(head[len(head)-1]))
		head = head[:len(head)-1]
	}

	if len(head) == len(older) {
		return body
	}

	trimmed := make([]any, 0, len(systemMsgs)+len(head)+len(tail))
	trimmed = append(trimmed, systemMsgs...)
	trimmed = append(trimmed, head...)
	trimmed = append(trimmed, tail...)
	req[key] = trimmed

	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// historyArray finds the conversation array in a request body and returns the
// key holding it plus its contents.
func historyArray(req map[string]any) (string, []any, bool) {
	for _, key := range []string{"messages", "input", "contents"} {
		if arr, ok := req[key].([]any); ok {
			return key, arr, true
		}
	}
	return "", nil, false
}

// splitSystemMessages partitions a conversation into system/instruction
// messages and everything else, preserving relative order in both.
func splitSystemMessages(arr []any) (system, rest []any) {
	for _, item := range arr {
		role := ""
		if m, ok := item.(map[string]any); ok {
			role = stringOf(m, "role")
		}
		if role == "system" || role == "developer" {
			system = append(system, item)
		} else {
			rest = append(rest, item)
		}
	}
	return system, rest
}

// contentLen sums the estimated length of a set of messages.
func contentLen(items []any) int {
	total := 0
	for _, it := range items {
		total += blockLen(itemContent(it))
	}
	return total
}

// blockLen estimates a message or block's length in characters.
func blockLen(content any) int {
	switch v := content.(type) {
	case string:
		return len(v)
	case []any:
		total := 0
		for _, b := range v {
			bm, ok := b.(map[string]any)
			if !ok {
				total += 50
				continue
			}
			if s := stringOf(bm, "text"); s != "" {
				total += len(s)
				continue
			}
			total += 50
		}
		return total
	default:
		return 0
	}
}

// itemContent returns a message's text carrier: the OpenAI/Claude content
// field, falling back to the Gemini parts field.
func itemContent(item any) any {
	m, ok := item.(map[string]any)
	if !ok {
		return nil
	}
	if c, ok := m["content"]; ok {
		return c
	}
	return m["parts"]
}

// adapterTrimmedBody returns the request body for a combo entry, trimmed when
// that entry is a capacity-adapter model with a smaller context window.
func adapterTrimmedBody(body []byte, entry string, adapterModels []string) []byte {
	if !slices.Contains(adapterModels, entry) {
		return body
	}
	return stripHistoryForContext(body, adapterContextWindow(entry))
}

// adapterTrimmedMap is adapterTrimmedBody for an already-decoded body.
func adapterTrimmedMap(req map[string]any, entry string, adapterModels []string) (map[string]any, error) {
	if !slices.Contains(adapterModels, entry) {
		out := make(map[string]any, len(req))
		maps.Copy(out, req)
		return out, nil
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request body: %w", err)
	}
	trimmed := stripHistoryForContext(encoded, adapterContextWindow(entry))

	var out map[string]any
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, fmt.Errorf("unmarshal trimmed request body: %w", err)
	}
	return out, nil
}

// adapterContextWindow returns the declared context window of an adapter model,
// or 0 when the catalog does not declare one.
func adapterContextWindow(modelEntry string) int {
	provider, model, _ := strings.Cut(modelEntry, "/")
	if model == "" {
		model = provider
	}
	caps := providers.GetCapabilitiesForModel(provider, model)
	if caps.ContextWindow > 0 {
		return caps.ContextWindow
	}
	window, _ := providers.GetModelTokenLimits(model)
	return window
}
