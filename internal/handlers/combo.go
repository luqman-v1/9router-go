package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"9router/proxy/internal/constants"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
)

// visionProviders lists providers known to support vision/image input.
var visionProviders = map[string]bool{
	"openai":     true,
	"anthropic":  true,
	"claude":     true,
	"gemini":     true,
	"antigravity": true,
	"xai":        true,
	"mistral":    true,
	"groq":       true,
	"openrouter": true,
}

// pdfProviders lists providers known to support PDF/document input.
var pdfProviders = map[string]bool{
	"openai":     true,
	"anthropic":  true,
	"claude":     true,
	"gemini":     true,
	"antigravity": true,
}

// modelHasCapability checks if a model (from a "provider/model" entry) supports
// the given capability. Returns true when uncertain (optimistic default).
func modelHasCapability(modelEntry string, cap string) bool {
	provider := modelEntry
	if idx := strings.Index(modelEntry, "/"); idx >= 0 {
		provider = modelEntry[:idx]
	}

	switch cap {
	case "vision":
		return visionProviders[provider]
	case "pdf":
		return pdfProviders[provider]
	default:
		return true
	}
}

// detectRequiredCapabilities scans the request body for content that requires
// specific model capabilities (vision, pdf). Returns a set of requirements.
// Matches JS detectRequiredCapabilities in combo.js.
func detectRequiredCapabilities(body []byte) map[string]bool {
	required := make(map[string]bool)

	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return required
	}

	// Scan messages (OpenAI / Claude format)
	if msgs, ok := m["messages"].([]any); ok {
		for _, msg := range msgs {
			scanMessageContent(msg, required)
		}
	}

	// Scan input (Responses API format)
	if input, ok := m["input"].([]any); ok {
		for _, msg := range input {
			scanMessageContent(msg, required)
		}
	}

	return required
}

// scanMessageContent checks a single message for capability requirements.
func scanMessageContent(msg any, required map[string]bool) {
	m, ok := msg.(map[string]any)
	if !ok {
		return
	}
	content := m["content"]
	if content == nil {
		return
	}

	switch c := content.(type) {
	case string:
		// No modality detection from plain text
	case []any:
		for _, block := range c {
			scanContentBlock(block, required)
		}
	}
}

// scanContentBlock checks a single content block for capability requirements.
func scanContentBlock(block any, required map[string]bool) {
	b, ok := block.(map[string]any)
	if !ok {
		return
	}
	typ, _ := b["type"].(string)
	switch typ {
	case "image_url", "image", "input_image":
		required["vision"] = true
	case "file", "document", "input_file":
		required["pdf"] = true
	}
	// Check mime type for inlineData/fileData (Gemini format)
	if mime, ok := b["mimeType"].(string); ok {
		if strings.HasPrefix(mime, "image/") {
			required["vision"] = true
		} else if mime == "application/pdf" {
			required["pdf"] = true
		}
	}
	// Also check nested inlineData / fileData
	for _, key := range []string{"inlineData", "fileData"} {
		if fd, ok := b[key].(map[string]any); ok {
			if mime, ok := fd["mimeType"].(string); ok {
				if strings.HasPrefix(mime, "image/") {
					required["vision"] = true
				} else if mime == "application/pdf" {
					required["pdf"] = true
				}
			}
		}
	}
}

// reorderByCapabilities reorders models by capability fit.
// Tier 0: satisfies all required capabilities. Tier 1: rest.
// Matches JS reorderByCapabilities in combo.js.
func reorderByCapabilities(models []string, required map[string]bool) []string {
	if len(required) == 0 || len(models) <= 1 {
		return models
	}

	var tier0, tier1 []string
	for _, m := range models {
		allMatch := true
		for cap := range required {
			if !modelHasCapability(m, cap) {
				allMatch = false
				break
			}
		}
		if allMatch {
			tier0 = append(tier0, m)
		} else {
			tier1 = append(tier1, m)
		}
	}

	result := make([]string, 0, len(models))
	result = append(result, tier0...)
	result = append(result, tier1...)
	return result
}

// applyComboStrategy reorders combo models based on the configured strategy.
// stickyLimit: consecutive requests per model before rotating (0/1 = rotate every request).
func (h *ChatHandler) applyComboStrategy(strategy string, models []string, comboName string, stickyLimit int) []string {
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
	case "sticky":
		if stickyLimit <= 1 {
			stickyLimit = 1
		}
		h.stickyMu.Lock()
		defer h.stickyMu.Unlock()

		key := comboName
		if key == "" {
			key = "__default__"
		}
		state, exists := h.stickyState[key]
		if !exists {
			state = &comboStickyState{Index: 0, ConsecutiveUseCount: 0}
			h.stickyState[key] = state
		}

		currentIndex := state.Index % len(models)
		rotated := make([]string, len(models))
		for i := 0; i < len(models); i++ {
			rotated[i] = models[(currentIndex+i)%len(models)]
		}

		state.ConsecutiveUseCount++
		if state.ConsecutiveUseCount >= stickyLimit {
			state.Index = (currentIndex + 1) % len(models)
			state.ConsecutiveUseCount = 0
		}

		return rotated
	case "capacity":
		fallthrough
	default:
		out := make([]string, len(models))
		copy(out, models)
		return out
	}
}

// keysString returns a comma-separated list of map keys.
func keysString(m map[string]bool) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// handleComboFallback iterates through combo model entries, trying each one.
// Auto-capability-switch: floats vision/pdf-capable models to the front.
func (h *ChatHandler) handleComboFallback(w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int) {
	var lastErr *upstreamError

	// Auto-capability-switch: float models that satisfy the request's required capabilities to the front.
	models := comboModels
	if required := detectRequiredCapabilities(body); len(required) > 0 {
		reordered := reorderByCapabilities(comboModels, required)
		if reordered[0] != comboModels[0] {
			log.Printf("[combo] auto-switch for [%v] → %s", keysString(required), reordered[0])
		}
		models = reordered
	}

	models = h.applyComboStrategy(strategy, models, comboName, stickyLimit)

	for _, entry := range models {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			continue
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
				continue
			}
			connID = conn.ID
			connData = cData
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

		var fwdErr error
		if modelInfo.Provider == "mimo-free" {
			comboMetrics := &streamMetrics{}
			fwdErr = h.MimoFreeChat(w, upstreamJSON, isStream, comboMetrics)
		} else {
			fwdErr = h.tryForwardWithConnection(w, modelInfo.Provider, modelInfo.Model, connID, connData, upstreamJSON, isStream, translateResponse, "/v1/chat/completions")
		}
		if fwdErr != nil {
			var ue *upstreamError
			if errors.As(fwdErr, &ue) {
				lastErr = ue
				continue
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, fwdErr))}
			continue
		}

		// tryForwardWithConnection already logs usage, so we just return.
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
// Auto-capability-switch: floats vision/pdf-capable models to the front.
func (h *ChatHandler) handleMessagesComboFallback(w http.ResponseWriter, translatedReq map[string]any, comboModels []string, strategy string, isStream bool, comboName string, stickyLimit int) {
	var lastErr *upstreamError

	// Auto-capability-switch: convert body to JSON for detection
	bodyJSON, _ := json.Marshal(translatedReq)
	models := comboModels
	if required := detectRequiredCapabilities(bodyJSON); len(required) > 0 {
		reordered := reorderByCapabilities(comboModels, required)
		models = reordered
	}

	models = h.applyComboStrategy(strategy, models, comboName, stickyLimit)

	for _, entry := range models {
		modelInfo := h.resolveModelEntry(entry)
		if modelInfo == nil {
			continue
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
				continue
			}
			connID = conn.ID
			connData = cData
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

		fwdErr := h.tryForwardWithConnection(w, modelInfo.Provider, modelInfo.Model, connID, connData, upstreamJSON, isStream, true, "/v1/messages")
		
		if fwdErr != nil {
			var ue *upstreamError
			if errors.As(fwdErr, &ue) {
				lastErr = ue
				continue
			}
			lastErr = &upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v","type":"upstream_error","code":502}}`, fwdErr))}
			continue
		}

		// tryForwardWithConnection already logs usage, so we just return.
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
		fwdErr := h.tryForwardWithConnection(rec, modelInfo.Provider, modelInfo.Model, connID, connData, upstreamJSON, false, false, "/v1/chat/completions")
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

	b, err := json.Marshal(m)
	if err != nil {
		log.Printf("[combo] failed to marshal appendUserTurn body: %v", err)
		return body
	}
	return b
}

// handleFusion implements combo fusion: parallel model fan-out + judge synthesis.
// Matches JS handleFusionChat in combo.js.
func (h *ChatHandler) handleFusion(w http.ResponseWriter, body []byte, comboModels []string, strategy string, isStream bool, translateResponse bool, comboName string, stickyLimit int) {
	panel := h.applyComboStrategy(strategy, comboModels, comboName, stickyLimit)
	if len(panel) == 0 {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "fusion combo has no models")
		return
	}
	if len(panel) == 1 {
		h.handleComboFallback(w, body, panel, "fallback", isStream, translateResponse, comboName, stickyLimit)
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
		h.handleComboFallback(w, body, []string{answers[0].model}, "fallback", isStream, translateResponse, comboName, stickyLimit)
		return
	}

	// Judge synthesizes final answer
	judgeBody := appendUserTurn(body, buildJudgePrompt(answers))

	var ub map[string]any
	if err := json.Unmarshal(judgeBody, &ub); err != nil {
		log.Printf("[fusion] failed to unmarshal judge body: %v", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to parse judge request")
		return
	}
	ub["model"] = judgeModel
	if isStream {
		ub["stream"] = true
	} else {
		delete(ub, "stream")
	}
	judgeJSON, err := json.Marshal(ub)
	if err != nil {
		log.Printf("[fusion] failed to marshal judge request body: %v", err)
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal judge request")
		return
	}

	modelInfo := h.resolveModelEntry(judgeModel)
	if modelInfo == nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("unresolved judge model: %s", judgeModel))
		return
	}
	h.handleSingleModel(w, judgeJSON, modelInfo, isStream, translateResponse)
}
