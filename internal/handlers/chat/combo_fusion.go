package chat

import (
	json "9router/proxy/internal/fastjson"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/shutdown"
)

type fusionAnswer struct {
	model string
	text  string
}

// fusionResult holds a single panel model's response.
type fusionResult struct {
	model string
	body  []byte
	ok    bool
	err   error
}

// FusionTuning tunes parallel-collection behavior of combo fusion.
// Matches JS FUSION_DEFAULTS in open-sse/services/combo.js.
type FusionTuning struct {
	MinPanel           int `json:"minPanel"`
	StragglerGraceMs   int `json:"stragglerGraceMs"`
	PanelHardTimeoutMs int `json:"panelHardTimeoutMs"`
}

var fusionDefaults = FusionTuning{
	MinPanel:           2,
	StragglerGraceMs:   8000,
	PanelHardTimeoutMs: 90000,
}

// handleFusion implements combo fusion: parallel model fan-out + judge synthesis.
// Matches JS handleFusionChat in combo.js.
func (h *ChatHandler) handleFusion(ctx context.Context, w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int, customJudgeModel string) {
	cw := newCommittedResponseWriter(w)
	if h.Repo != nil {
		avail := h.loadComboAvailability(ctx)
		comboModels, _ = avail.filterModels(comboModels)
		if len(comboModels) == 0 {
			handlerutil.WriteJSONError(cw, http.StatusServiceUnavailable, "all fusion combo models are currently disabled or unavailable")
			return
		}
		if customJudgeModel != "" && !avail.isEntryAvailable(customJudgeModel) {
			customJudgeModel = ""
		}
	}
	panel := h.ApplyComboStrategy(strategy, comboModels, comboName, stickyLimit)
	if len(panel) == 0 {
		handlerutil.WriteJSONError(cw, http.StatusBadRequest, "fusion combo has no models")
		return
	}
	if len(panel) == 1 {
		h.handleComboFallback(ctx, cw, body, panel, "fallback", isStream, translateResponse, comboName, stickyLimit)
		return
	}

	// Build panel body: strip tools → prose, force non-streaming
	var panelBody map[string]any
	if err := json.Unmarshal(body, &panelBody); err != nil {
		handlerutil.WriteJSONError(cw, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if msgs, ok := panelBody["messages"].([]any); ok {
		panelBody["messages"] = flattenToolHistory(msgs)
	}
	panelBody["stream"] = false
	delete(panelBody, "tools")
	delete(panelBody, "tool_choice")
	panelJSON, err := json.Marshal(panelBody)
	if err != nil {
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to marshal panel body")
		return
	}

	// Fan-out panel calls (ctx aborts stragglers the panel moves on without).
	ft := fusionDefaults
	calls := make([]func(context.Context) *fusionResult, len(panel))
	for i, entry := range panel {
		calls[i] = h.makePanelCall(panelJSON, entry, comboName)
	}

	settled := collectPanel(ctx, calls, ft)

	// Extract successful answers
	var answers []fusionAnswer
	judgeModel := panel[0] // default: first panel model
	if customJudgeModel != "" {
		judgeModel = customJudgeModel
	}
	for i, res := range settled {
		if res == nil || !res.ok {
			continue
		}
		text := extractPanelText(res.body)
		if text == "" {
			continue
		}
		answers = append(answers, fusionAnswer{model: panel[i], text: text})
	}

	// Degradation
	if len(answers) == 0 {
		handlerutil.WriteJSONError(cw, http.StatusServiceUnavailable, "all fusion panel models failed")
		return
	}
	if len(answers) == 1 {
		h.handleComboFallback(ctx, cw, body, []string{answers[0].model}, "fallback", isStream, translateResponse, comboName, stickyLimit)
		return
	}

	// Judge synthesizes final answer
	judgeBody := appendUserTurn(body, buildJudgePrompt(answers))

	var ub map[string]any
	if err := json.Unmarshal(judgeBody, &ub); err != nil {
		log.Error("fusion", "unmarshal judge body failed", "error", err)
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to parse judge request")
		return
	}
	ub["model"] = judgeModel
	ctx = h.applyJailbreak(ctx, ub, comboName, judgeModel)
	if isStream {
		ub["stream"] = true
	} else {
		delete(ub, "stream")
	}
	judgeJSON, err := json.Marshal(ub)
	if err != nil {
		log.Error("combo", "marshal judge body failed", "error", err)
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to marshal judge request")
		return
	}

	modelInfo := h.resolveModelEntry(judgeModel)
	if modelInfo == nil {
		handlerutil.WriteJSONError(cw, http.StatusBadGateway, fmt.Sprintf("unresolved judge model: %s", judgeModel))
		return
	}
	h.handleSingleModel(ctx, cw, judgeJSON, modelInfo, isStream, translateResponse)
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

// stripToolsForNonToolModel drops tool definitions and converts tool turns to
// prose. A tool-capable client can land on a provider/model that cannot emit
// tool_calls (a combo fallback, for example); sending the raw tool history there
// gets an HTTP 400 instead of an answer. The prose form keeps the retrieved
// context in the prompt without advertising tools the model cannot call.
func stripToolsForNonToolModel(body []byte) []byte {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body
	}
	delete(obj, "tools")
	delete(obj, "tool_choice")
	delete(obj, "functions")
	if msgs, ok := obj["messages"].([]any); ok && len(msgs) > 0 {
		obj["messages"] = flattenToolHistory(msgs)
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
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
// all settled, grace fires, hard timeout reached, or ctx/shutdown fires.
// Stragglers that lose the race are abandoned via ctx cancel (their HTTP
// requests abort); without this they run uncancelable until upstream
// responds, leaking goroutines + sockets per fusion call.
// Matches JS collectPanel in combo.js.
func collectPanel(ctx context.Context, calls []func(context.Context) *fusionResult, ft FusionTuning) []*fusionResult {
	n := len(calls)
	if n == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]*fusionResult, n)
	type pair struct {
		idx int
		res *fusionResult
	}
	ch := make(chan pair, n)

	for i, call := range calls {
		go func(idx int) {
			select {
			case ch <- pair{idx, call(ctx)}:
			case <-ctx.Done():
			}
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
		case <-ctx.Done():
			return results
		case <-shutdown.Done():
			return results
		}
	}
	return results
}

// makePanelCall returns a closure that resolves one panel model and forwards the
// request. The closure signature matches what collectPanel expects. The ctx
// aborts the upstream HTTP request when the panel moves on without us.
func (h *ChatHandler) makePanelCall(body []byte, entry, comboName string) func(context.Context) *fusionResult {
	return func(ctx context.Context) *fusionResult {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			return &fusionResult{model: entry, err: fmt.Errorf("unresolved model: %s", entry)}
		}

		var connID string
		var connData *ConnectionData
		if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			connData = &ConnectionData{
				APIKey:      cfg.DefaultAPIKey,
				Provider:    modelInfo.Provider,
				ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider),
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
		ctx = h.applyJailbreak(ctx, upstreamBody, comboName, jailbreakLegIDs(modelInfo.Provider, modelInfo.Model)...)
		upstreamJSON, err := json.Marshal(upstreamBody)
		if err != nil {
			return &fusionResult{model: entry, err: err}
		}

		rec := &responseBuffer{header: http.Header{}}
		fusionParams := forwardRequestParams{
			Ctx: ctx, W: rec, Provider: modelInfo.Provider, Model: modelInfo.Model,
			ConnectionID: connID, ConnData: connData, Body: upstreamJSON, Attempts: 1,
			IsStream: false, TranslateResponse: false, Endpoint: "/v1/chat/completions",
		}
		fwdErr := h.tryForwardWithConnection(fusionParams)
		if fwdErr != nil {
			fwdErr = h.walkPoolOnFreeTierRefusalFor(fusionParams, connID, "", "", fwdErr)
		}
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

func (b *responseBuffer) Header() http.Header         { return b.header }
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
