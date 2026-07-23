package chat

import (
	"bytes"
	"encoding/json"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/providers"
)

type fusionAnswer struct {
	model string
	text  string
}

// flattenToolHistory converts tool turns to prose so panel models keep context
// without emitting tool_calls. Matches JS flattenToolHistory in combo.js.
func flattenToolHistory(msgs []any) []any {
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			out = append(out, m)
			continue
		}
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)

		if role == "tool" {
			text := "[Tool result]\n" + content
			out = append(out, map[string]any{"role": "user", "content": text})
			continue
		}

		if role == "assistant" {
			if tcs, ok := msg["tool_calls"].([]any); ok && len(tcs) > 0 {
				var sb strings.Builder
				if content != "" {
					sb.WriteString(content)
					sb.WriteString("\n")
				}
				for _, tc := range tcs {
					tcObj, _ := tc.(map[string]any)
					fn, _ := tcObj["function"].(map[string]any)
					name, _ := fn["name"].(string)
					args, _ := fn["arguments"].(string)
					sb.WriteString(fmt.Sprintf("[Call tool %s(%s)]", name, args))
				}
				out = append(out, map[string]any{
					"role":    "assistant",
					"content": strings.TrimSpace(sb.String()),
				})
				continue
			}
		}

		out = append(out, m)
	}
	return out
}

// buildJudgePrompt builds the fusion judge system prompt from panel answers.
// Sources are anonymized so the judge weighs substance, not model reputation.
// Matches JS buildJudgePrompt in combo.js.
func buildJudgePrompt(answers []fusionAnswer) string {
	var panel strings.Builder
	for i, a := range answers {
		panel.WriteString(fmt.Sprintf("[Source %d]\n%s\n\n", i+1, a.text))
	}

	return fmt.Sprintf(`You are the JUDGE in a model-fusion panel. %d expert models independently answered the user's most recent request. Their responses are below, anonymized by source.

Do NOT mention that multiple models were used, and do NOT refer to the sources. Produce ONE authoritative final answer addressed directly to the user.

First, internally analyze the panel along these dimensions: consensus (points most sources agree on — treat as higher-confidence), contradictions (where they disagree — resolve with your own judgment), partial coverage, unique insights only one source surfaced, and blind spots every source missed. Then write the best possible final answer grounded in that analysis — more complete and correct than any single response, with no filler.

=== PANEL RESPONSES ===
%s=== END PANEL RESPONSES ===

Now write the final answer to the user's original request.`, len(answers), panel.String())
}

// collectPanel gathers panel responses with quorum-grace timing.
// Once minPanel answers arrive, a grace timer starts. Returns when
// all settled, grace fires, or hard timeout reached.
// Matches JS collectPanel in combo.js.
func collectPanel(calls []func() *fusionResult, ft FusionTuning) []*fusionResult {
	n := len(calls)
	if n == 0 {
		return nil
	}

	results := make([]*fusionResult, n)
	type pair struct {
		idx int
		res *fusionResult
	}
	ch := make(chan pair, n)

	for i, call := range calls {
		go func(idx int) {
			ch <- pair{idx, call()}
		}(i)
	}

	hardTimeout := time.After(time.Duration(ft.PanelHardTimeoutMs) * time.Millisecond)
	var graceTimeout <-chan time.Time

	settled := 0
	okCount := 0

	for settled < n {
		select {
		case p := <-ch:
			results[p.idx] = p.res
			settled++
			if p.res != nil && p.res.ok {
				okCount++
				if okCount >= ft.MinPanel && graceTimeout == nil {
					graceTimeout = time.After(time.Duration(ft.StragglerGraceMs) * time.Millisecond)
				}
			}
		case <-graceTimeout:
			return results
		case <-hardTimeout:
			return results
		}
	}
	return results
}

// makePanelCall returns a closure that resolves one panel model and forwards the
// request. The closure signature matches what collectPanel expects.
func (h *ChatHandler) makePanelCall(body []byte, entry string) func() *fusionResult {
	return func() *fusionResult {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			return &fusionResult{model: entry, err: fmt.Errorf("unresolved model: %s", entry)}
		}

		var connID string
		var connData *ConnectionData
		if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			connData = &ConnectionData{
				APIKey: cfg.DefaultAPIKey,
			}
		} else {
			conn, cData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
			if err != nil {
				return &fusionResult{model: entry, err: err}
			}
			connID = conn.ID
			connData = cData
		}

		var upstreamBody map[string]any
		if err := json.Unmarshal(body, &upstreamBody); err != nil {
			return &fusionResult{model: entry, err: err}
		}
		upstreamBody["model"] = modelInfo.Model
		upstreamJSON, err := json.Marshal(upstreamBody)
		if err != nil {
			return &fusionResult{model: entry, err: err}
		}

		rec := &responseBuffer{header: http.Header{}}
		fwdErr := h.tryForwardWithConnection(context.Background(), rec, modelInfo.Provider, modelInfo.Model, connID, connData, upstreamJSON, false, false, "/v1/chat/completions")
		if fwdErr != nil {
			return &fusionResult{model: entry, err: fwdErr}
		}

		return &fusionResult{model: entry, ok: true, body: rec.body.Bytes()}
	}
}

// responseBuffer is a minimal http.ResponseWriter that captures output in memory.
type responseBuffer struct {
	header http.Header
	body   bytes.Buffer
	code   int
}

func (b *responseBuffer) Header() http.Header        { return b.header }
func (b *responseBuffer) Write(p []byte) (int, error) { return b.body.Write(p) }
func (b *responseBuffer) WriteHeader(code int)        { b.code = code }

// extractPanelText extracts the content text from a chat completion JSON response.
func extractPanelText(body []byte) string {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return ""
	}
	if len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Message.Content
}

// appendUserTurn appends a user message with the given content to the messages
// or input array. Restores stream flag from original body.
func appendUserTurn(body []byte, content string) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}

	userMsg := map[string]any{"role": "user", "content": content}

	if msgs, ok := m["messages"].([]any); ok {
		m["messages"] = append(msgs, userMsg)
	} else if input, ok := m["input"].([]any); ok {
		m["input"] = append(input, userMsg)
	} else {
		m["messages"] = []any{userMsg}
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
