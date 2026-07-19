package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/constants"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// applyComboStrategy reorders combo models based on the configured strategy.
func (h *ChatHandler) applyComboStrategy(strategy string, models []string) []string {
	if len(models) <= 1 {
		return models
	}

	switch strategy {
	case "round-robin":
		h.rrMu.Lock()
		start := h.rrIdx % len(models)
		h.rrIdx++
		h.rrMu.Unlock()
		out := make([]string, len(models))
		for i := 0; i < len(models); i++ {
			out[i] = models[(start+i)%len(models)]
		}
		return out
	case "capacity":
		fallthrough
	default:
		out := make([]string, len(models))
		copy(out, models)
		return out
	}
}

// handleComboFallback iterates through combo model entries, trying each one.
func (h *ChatHandler) handleComboFallback(w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool) {
	var lastErr *upstreamError

	models := h.applyComboStrategy(strategy, comboModels)

	for _, entry := range models {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			continue
		}

		var providerCfg *providers.ProviderConfig
		var apiKey string
		if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			c := cfg
			providerCfg = &c
			apiKey = c.DefaultAPIKey
		} else {
			_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
			if err != nil {
				continue
			}
			cfg, err := h.getProviderConfig(modelInfo.Provider, connData)
			if err != nil {
				continue
			}
			providerCfg = cfg
			apiKey = extractAPIKey(connData)
			if apiKey == "" {
				continue
			}
		}

		var upstreamBody map[string]any
		if err := json.Unmarshal(body, &upstreamBody); err != nil {
			handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to parse request body")
			return
		}
		upstreamBody["model"] = modelInfo.Model

		upstreamJSON, err := json.Marshal(upstreamBody)
		if err != nil {
			continue
		}

		comboStart := time.Now()
		comboMetrics := &streamMetrics{}

		var fwdErr error
		if modelInfo.Provider == "mimo-free" {
			fwdErr = h.MimoFreeChat(w, upstreamJSON, isStream, comboMetrics)
		} else {
			comboBody := h.applyTokenSavers(upstreamJSON)
			fwdErr = h.forwardRequest(w, providerCfg, apiKey, comboBody, isStream, translateResponse, comboMetrics)
		}
		comboLatency := time.Since(comboStart).Milliseconds()
		if fwdErr != nil {
			if ue, ok := fwdErr.(*upstreamError); ok {
				lastErr = ue
				continue
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, fwdErr))}
			continue
		}

		usage := translator.GetAndClearLastUsage()
		if usage == nil {
			usage = &translator.OpenAIUsage{}
		}
		logInfo := &UsageLogInfo{
			Provider:     modelInfo.Provider,
			Model:        modelInfo.Model,
			ConnectionID: modelInfo.ConnectionID,
			APIKey:       apiKey,
			Endpoint:     "/v1/chat/completions",
		}
		h.logUsage(logInfo, usage, comboLatency, upstreamJSON, comboMetrics)
		return
	}

	if lastErr != nil {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(lastErr.StatusCode)
		w.Write(lastErr.Body)
		return
	}
	handlerutil.WriteJSONError(w, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// handleMessagesComboFallback iterates through combo models for the Claude endpoint.
func (h *ChatHandler) handleMessagesComboFallback(w http.ResponseWriter, translatedReq map[string]any, comboModels []string, strategy string, isStream bool) {
	var lastErr *upstreamError

	models := h.applyComboStrategy(strategy, comboModels)

	for _, entry := range models {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			continue
		}

		var providerCfg *providers.ProviderConfig
		var apiKey string
		if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			c := cfg
			providerCfg = &c
			apiKey = c.DefaultAPIKey
		} else {
			_, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
			if err != nil {
				continue
			}
			cfg, err := h.getProviderConfig(modelInfo.Provider, connData)
			if err != nil {
				continue
			}
			providerCfg = cfg
			apiKey = extractAPIKey(connData)
			if apiKey == "" {
				continue
			}
		}

		entryReq := make(map[string]any, len(translatedReq))
		for k, v := range translatedReq {
			entryReq[k] = v
		}
		entryReq["model"] = modelInfo.Model

		upstreamJSON, err := json.Marshal(entryReq)
		if err != nil {
			continue
		}

		comboStart := time.Now()
		comboMetrics := &streamMetrics{}
		comboBody := h.applyTokenSavers(upstreamJSON)
		fwdErr := h.forwardRequest(w, providerCfg, apiKey, comboBody, isStream, true, comboMetrics)
		comboLatency := time.Since(comboStart).Milliseconds()
		if fwdErr != nil {
			if ue, ok := fwdErr.(*upstreamError); ok {
				lastErr = ue
				continue
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, fwdErr))}
			continue
		}

		usage := translator.GetAndClearLastUsage()
		if usage == nil {
			usage = &translator.OpenAIUsage{}
		}
		logInfo := &UsageLogInfo{
			Provider:     modelInfo.Provider,
			Model:        modelInfo.Model,
			ConnectionID: modelInfo.ConnectionID,
			APIKey:       apiKey,
			Endpoint:     "/v1/v1/messages",
		}
		h.logUsage(logInfo, usage, comboLatency, upstreamJSON, comboMetrics)
		return
	}

	if lastErr != nil {
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(lastErr.StatusCode)
		w.Write(lastErr.Body)
		return
	}
	handlerutil.WriteJSONError(w, http.StatusBadGateway, "all combo models failed: no valid entries")
}

// ---- Fusion (parallel fan-out + judge synthesis) ----

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

type fusionAnswer struct {
	model string
	text  string
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

		var providerCfg *providers.ProviderConfig
		var apiKey string
		var connID string
		if cfg, ok := providers.KnownProviders[modelInfo.Provider]; ok && (cfg.NoAuth || cfg.DefaultAPIKey != "") {
			c := cfg
			providerCfg = &c
			apiKey = c.DefaultAPIKey
		} else {
			conn, connData, err := h.getBestConnection(modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
			if err != nil {
				return &fusionResult{model: entry, err: err}
			}
			cfg, err := h.getProviderConfig(modelInfo.Provider, connData)
			if err != nil {
				return &fusionResult{model: entry, err: err}
			}
			providerCfg = cfg
			apiKey = extractAPIKey(connData)
			if apiKey == "" {
				return &fusionResult{model: entry, err: fmt.Errorf("no API key")}
			}
			connID = conn.ID
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
		_ = connID
		fwdErr := h.forwardRequest(rec, providerCfg, apiKey, upstreamJSON, false, false, nil)
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
// array. Restores stream flag from original body.
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

	b, _ := json.Marshal(m)
	return b
}

// handleFusion implements combo fusion: parallel model fan-out + judge synthesis.
// Matches JS handleFusionChat in combo.js.
func (h *ChatHandler) handleFusion(w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool) {
	panel := h.applyComboStrategy(strategy, comboModels)
	if len(panel) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "fusion combo has no models")
		return
	}
	if len(panel) == 1 {
		h.handleComboFallback(w, body, panel, "fallback", isStream, translateResponse)
		return
	}

	// Build panel body: strip tools → prose, force non-streaming
	var panelBody map[string]any
	if err := json.Unmarshal(body, &panelBody); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
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
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal panel body")
		return
	}

	// Fan-out panel calls
	ft := fusionDefaults
	calls := make([]func() *fusionResult, len(panel))
	for i, entry := range panel {
		calls[i] = h.makePanelCall(panelJSON, entry)
	}

	settled := collectPanel(calls, ft)

	// Extract successful answers
	var answers []fusionAnswer
	judgeModel := panel[0] // default: first panel model
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
		handlerutil.WriteJSONError(w, http.StatusServiceUnavailable, "all fusion panel models failed")
		return
	}
	if len(answers) == 1 {
		h.handleComboFallback(w, body, []string{answers[0].model}, "fallback", isStream, translateResponse)
		return
	}

	// Judge synthesizes final answer
	judgeBody := appendUserTurn(body, buildJudgePrompt(answers))

	var ub map[string]any
	json.Unmarshal(judgeBody, &ub)
	ub["model"] = judgeModel
	if isStream {
		ub["stream"] = true
	} else {
		delete(ub, "stream")
	}
	judgeJSON, _ := json.Marshal(ub)

	modelInfo := h.resolveModelEntry(judgeModel)
	if modelInfo == nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("unresolved judge model: %s", judgeModel))
		return
	}
	h.handleSingleModel(w, judgeJSON, modelInfo, isStream, translateResponse)
}
