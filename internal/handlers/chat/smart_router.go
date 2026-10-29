package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
)

// Smart Router (System One) combo strategy, ported from upstream
// open-sse/services/systemoneRouter.js + src/sse/services/systemoneRouter.js
// (f3f86fdd, #4975): a decision model evaluates the request before dispatch
// and orders the combo's models by its choice, with per-session stickiness so
// later turns reuse the provider holding the warm prompt cache. Everything
// here fails open: any evaluation problem leaves the combo's own ordering in
// place, so a broken router model can never take the combo down.

const (
	// smartRouterDefaultModel is the free OpenCode System One model upstream
	// defaults to when a combo configures none.
	smartRouterDefaultModel = "oc/jev-1.13-free"
	// smartRouterTimeout bounds one evaluation; past it the strategy falls
	// back to the combo's own ordering rather than stalling the turn.
	smartRouterTimeout = 8 * time.Second
	// smartMaxStateChars caps the prompt text sent to the classifier.
	smartMaxStateChars = 2000
	// smartStickyTTL is how long a session's pinned model survives idle.
	smartStickyTTL = 2 * time.Hour
	// smartMaxStickySessions bounds the sticky store; at the ceiling the
	// oldest quarter is dropped (upstream pruneStickyComboSessions).
	smartMaxStickySessions = 5000
	// smartDefaultThreshold is the Smart Switch confidence floor.
	smartDefaultThreshold = 0.8
)

// smartChoice is one parsed System One routing decision.
type smartChoice struct {
	Choice        string
	Confidence    *float64
	Probabilities map[string]float64
}

// smartRouteState rides the request context so the combo failure loop can
// clear a pinned model that just failed, without threading the smart fields
// through every signature between dispatch and the per-entry error path.
type smartRouteState struct {
	comboName   string
	sessionID   string
	activeModel string
}

type smartRouteStateKey struct{}

// extractSmartPromptState pulls the latest user turn out of a request body
// (messages[] or input[] or a string prompt), truncated to the classifier
// cap. Upstream extractPromptState.
func extractSmartPromptState(body []byte) string {
	var raw map[string]any
	if json.Unmarshal(body, &raw) != nil || raw == nil {
		return ""
	}
	if messages, ok := raw["messages"].([]any); ok && len(messages) > 0 {
		for i := len(messages) - 1; i >= 0; i-- {
			msg, _ := messages[i].(map[string]any)
			if msg == nil {
				continue
			}
			role, _ := msg["role"].(string)
			if role == "user" || !hasRoleAndNoContent(msg) {
				if text := smartTextOf(msg["content"]); strings.TrimSpace(text) != "" {
					return truncateSmartState(text)
				}
			}
		}
		return truncateSmartState(smartTextOf(lastSmartMessageContent(messages)))
	}
	if input, ok := raw["input"].([]any); ok && len(input) > 0 {
		for i := len(input) - 1; i >= 0; i-- {
			msg, _ := input[i].(map[string]any)
			if msg == nil {
				continue
			}
			if t, _ := msg["type"].(string); t == "message" {
				if text := smartTextOf(msg["content"]); strings.TrimSpace(text) != "" {
					return truncateSmartState(text)
				}
			}
		}
		return truncateSmartState(smartTextOf(lastSmartMessageContent(input)))
	}
	if s, ok := raw["prompt"].(string); ok && strings.TrimSpace(s) != "" {
		return truncateSmartState(s)
	}
	if s, ok := raw["input"].(string); ok && strings.TrimSpace(s) != "" {
		return truncateSmartState(s)
	}
	return ""
}

func hasRoleAndNoContent(msg map[string]any) bool {
	role, _ := msg["role"].(string)
	return role != "" && msg["content"] == nil
}

func lastSmartMessageContent(messages []any) any {
	if msg, _ := messages[len(messages)-1].(map[string]any); msg != nil {
		if c, ok := msg["content"]; ok {
			return c
		}
		return msg["text"]
	}
	return nil
}

func smartTextOf(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, part := range v {
			switch p := part.(type) {
			case string:
				parts = append(parts, p)
			case map[string]any:
				if t, _ := p["type"].(string); t == "text" || t == "input_text" || t == "output_text" {
					if text, ok := p["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func truncateSmartState(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > smartMaxStateChars {
		return text[:smartMaxStateChars]
	}
	return text
}

// buildSmartCriteria mirrors upstream buildChoiceCriteria: the combo's model
// ids are the choice keys, with null descriptions so System One leans on its
// own knowledge of the models.
func buildSmartCriteria(models []string) map[string]any {
	criteria := make(map[string]any, len(models))
	for _, m := range models {
		if trimmed := strings.TrimSpace(m); trimmed != "" {
			criteria[trimmed] = nil
		}
	}
	return criteria
}

// parseSmartRouteAnswer reads the upstream answers.route object. A missing or
// empty choice is not a decision; confidence and probabilities are optional.
func parseSmartRouteAnswer(raw []byte) *smartChoice {
	var payload struct {
		Answers struct {
			Route struct {
				Choice        string         `json:"choice"`
				Confidence    *float64       `json:"confidence"`
				Probabilities map[string]any `json:"probabilities"`
			} `json:"route"`
		} `json:"answers"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	choice := strings.TrimSpace(payload.Answers.Route.Choice)
	if choice == "" {
		return nil
	}
	out := &smartChoice{Choice: choice, Confidence: payload.Answers.Route.Confidence}
	if probs := payload.Answers.Route.Probabilities; len(probs) > 0 {
		out.Probabilities = make(map[string]float64, len(probs))
		for k, v := range probs {
			if n, ok := v.(float64); ok {
				out.Probabilities[k] = n
			}
		}
	}
	return out
}

// orderModelsBySmartChoice puts the chosen model first and orders the rest by
// their probabilities descending; models without a probability keep their
// relative order at the end. A choice outside the combo changes nothing.
func orderModelsBySmartChoice(models []string, choice *smartChoice) []string {
	out := make([]string, len(models))
	copy(out, models)
	if len(out) <= 1 || choice == nil {
		return out
	}
	if !containsString(out, choice.Choice) {
		return out
	}
	others := make([]string, 0, len(out)-1)
	for _, m := range out {
		if m != choice.Choice {
			others = append(others, m)
		}
	}
	if len(choice.Probabilities) > 0 {
		// Insertion sort by probability descending: the lists are tiny (combo
		// membership) and the tie must keep the original relative order,
		// which sort.Slice does not guarantee.
		for i := 1; i < len(others); i++ {
			for j := i; j > 0 && choice.Probabilities[others[j]] > choice.Probabilities[others[j-1]]; j-- {
				others[j], others[j-1] = others[j-1], others[j]
			}
		}
	}
	return append([]string{choice.Choice}, others...)
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// getSmartStickyModel returns the session's pinned model when it is still
// live (TTL) and still a member of the combo. Upstream getStickyComboModel.
func (h *ChatHandler) getSmartStickyModel(comboName, sessionID string, models []string) string {
	if comboName == "" || sessionID == "" || len(models) == 0 {
		return ""
	}
	h.smartStickyMu.Lock()
	defer h.smartStickyMu.Unlock()
	entry, ok := h.smartSticky[smartStickyKey(comboName, sessionID)]
	if !ok {
		return ""
	}
	if time.Since(entry.LastUsed) > smartStickyTTL || !containsString(models, entry.Model) {
		delete(h.smartSticky, smartStickyKey(comboName, sessionID))
		return ""
	}
	entry.LastUsed = time.Now()
	h.smartSticky[smartStickyKey(comboName, sessionID)] = entry
	return entry.Model
}

// setSmartStickyModel pins a session's model. Upstream setStickyComboModel.
func (h *ChatHandler) setSmartStickyModel(comboName, sessionID, model string) {
	if comboName == "" || sessionID == "" || model == "" {
		return
	}
	h.pruneSmartSticky()
	h.smartStickyMu.Lock()
	defer h.smartStickyMu.Unlock()
	if h.smartSticky == nil {
		h.smartSticky = make(map[string]smartStickyEntry)
	}
	h.smartSticky[smartStickyKey(comboName, sessionID)] = smartStickyEntry{Model: model, LastUsed: time.Now()}
}

// clearSmartStickySession drops one session's pin, or every pin of a combo
// when sessionID is empty. Upstream clearStickyComboSession.
func (h *ChatHandler) clearSmartStickySession(comboName, sessionID string) {
	h.smartStickyMu.Lock()
	defer h.smartStickyMu.Unlock()
	if comboName == "" {
		h.smartSticky = nil
		return
	}
	if sessionID != "" {
		delete(h.smartSticky, smartStickyKey(comboName, sessionID))
		return
	}
	prefix := comboName + ":"
	for k := range h.smartSticky {
		if strings.HasPrefix(k, prefix) {
			delete(h.smartSticky, k)
		}
	}
}

// pruneSmartSticky bounds the store: expired entries go first, then the
// oldest quarter once the ceiling is still exceeded.
func (h *ChatHandler) pruneSmartSticky() {
	h.smartStickyMu.Lock()
	defer h.smartStickyMu.Unlock()
	if len(h.smartSticky) < smartMaxStickySessions {
		return
	}
	now := time.Now()
	for k, v := range h.smartSticky {
		if now.Sub(v.LastUsed) > smartStickyTTL {
			delete(h.smartSticky, k)
		}
	}
	if len(h.smartSticky) < smartMaxStickySessions {
		return
	}
	type aged struct {
		key string
		at  time.Time
	}
	entries := make([]aged, 0, len(h.smartSticky))
	for k, v := range h.smartSticky {
		entries = append(entries, aged{k, v.LastUsed})
	}
	// Selection by repeated min-scan: stable and allocation-light for a
	// once-per-5000-sessions prune.
	drop := len(entries) / 4
	for drop > 0 && len(entries) > 0 {
		oldest := 0
		for i := range entries {
			if entries[i].at.Before(entries[oldest].at) {
				oldest = i
			}
		}
		delete(h.smartSticky, entries[oldest].key)
		entries = append(entries[:oldest], entries[oldest+1:]...)
		drop--
	}
}

func smartStickyKey(comboName, sessionID string) string {
	return comboName + ":" + sessionID
}

// smartSwitchThreshold resolves the confidence floor for the configured
// policy (upstream handleComboChat's switchThreshold ladder).
func smartSwitchThreshold(cfg db.ComboStrategy) float64 {
	policy := strings.ToLower(strings.TrimSpace(cfg.SmartPolicy))
	threshold := 0.0
	if cfg.ConfidenceThreshold != nil {
		threshold = *cfg.ConfidenceThreshold
	}
	switch {
	case policy == "custom":
		if threshold > 0 {
			return threshold
		}
	case policy == "smart-switch-70":
		return 0.7
	case policy == "smart-switch-90":
		return 0.9
	case strings.HasPrefix(policy, "smart-switch") || policy == "smart" || policy == "":
		if threshold > 0 {
			return threshold
		}
	}
	return smartDefaultThreshold
}

// resolveSmartChoice asks the configured System One model to pick the best
// combo member for this request. It never fails the turn: any problem
// (missing credentials, timeout, unparseable answer) returns nil and the
// strategy falls back to the combo's own ordering. Port of upstream
// resolveSmartChoice.
func (h *ChatHandler) resolveSmartChoice(ctx context.Context, body []byte, models []string, routerModel string) *smartChoice {
	if len(models) <= 1 {
		return nil
	}
	target := strings.TrimSpace(routerModel)
	if target == "" {
		target = smartRouterDefaultModel
	}
	state := extractSmartPromptState(body)
	if state == "" {
		return nil
	}
	criteria := buildSmartCriteria(models)
	if len(criteria) == 0 {
		return nil
	}
	choice, err := h.querySystemOneRouter(ctx, target, state, criteria)
	if err != nil || choice == nil {
		return nil
	}
	return choice
}

// querySystemOneRouter performs the evaluation call against the router
// model's provider, using the same URL rules as the /v1/systemone lane.
func (h *ChatHandler) querySystemOneRouter(ctx context.Context, targetModel, state string, criteria map[string]any) (*smartChoice, error) {
	modelInfo, err := h.resolveModel(targetModel)
	if err != nil || modelInfo == nil {
		log.Warn("combo", "smart-router: unrecognized router model", "model", targetModel)
		return nil, fmt.Errorf("unrecognized router model")
	}
	conn, connData, err := h.GetBestConnectionWithContext(ctx, modelInfo.Provider, modelInfo.ConnectionID, nil, modelInfo.Model)
	if err != nil || connData == nil {
		// The default router model rides a NoAuth provider (free OpenCode);
		// it answers with synthetic credentials, like the /v1/systemone lane.
		cfg, known := providers.KnownProviders[modelInfo.Provider]
		if !known || (!cfg.NoAuth && cfg.DefaultAPIKey == "") {
			log.Warn("combo", "smart-router: no credentials for router provider", "provider", modelInfo.Provider)
			return nil, fmt.Errorf("no router credentials")
		}
		connData = &ConnectionData{ProxyPoolID: h.ResolveProviderProxyPoolID(modelInfo.Provider)}
	}
	apiKey := ExtractAPIKey(connData)
	connID := ""
	if conn != nil {
		connID = conn.ID
	}
	if refreshed, _, rErr := h.RefreshOAuthTokenIfExpired(connID, apiKey); rErr == nil && refreshed != "" {
		apiKey = refreshed
	}
	providerCfg, err := h.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil || providerCfg == nil {
		return nil, fmt.Errorf("router provider config: %w", err)
	}
	if apiKey == "" && providerCfg.DefaultAPIKey != "" {
		apiKey = providerCfg.DefaultAPIKey
	}

	question := map[string]any{
		"model": modelInfo.Model,
		"state": state,
		"questions": map[string]any{
			"route": map[string]any{
				"type":         "choice",
				"instructions": "Which of these configured models is the most appropriate for this request?",
				"criteria":     criteria,
			},
		},
	}
	payload, err := json.Marshal(question)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, smartRouterTimeout)
	defer cancel()
	targetURL := systemoneTargetURL(providerCfg, connData.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	handlerutil.SetAuthHeader(req, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
	for k, v := range providerCfg.StaticHeaders {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	client, clientErr := h.GetClientForConnection(connData)
	if clientErr != nil {
		return nil, clientErr
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn("combo", "smart-router: evaluation aborted", "error", err.Error())
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		log.Warn("combo", "smart-router: evaluation failed", "status", resp.StatusCode)
		return nil, fmt.Errorf("router status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return parseSmartRouteAnswer(raw), nil
}

// applySmartRouting orders a combo's models for the smart strategy and
// returns a context carrying the routing state the failure loop reads. Port
// of the smart branch of upstream handleComboChat: a pinned session model is
// reused (Strict Lock without an evaluation; Smart Switch re-evaluated and
// only switched above the confidence floor), and a fresh or per-turn session
// is ordered by the System One choice. Every failure path keeps the combo's
// own ordering.
func (h *ChatHandler) applySmartRouting(ctx context.Context, body []byte, comboModels []string, comboName string) (context.Context, []string) {
	models := make([]string, len(comboModels))
	copy(models, comboModels)
	if len(models) <= 1 {
		return ctx, models
	}

	cfg := db.ComboStrategy{}
	if settings, err := h.Repo.GetSettings(); err == nil && settings != nil {
		if saved, ok := settings.ComboStrategies[comboName]; ok {
			cfg = saved
		}
	}
	policy := strings.ToLower(strings.TrimSpace(cfg.SmartPolicy))
	if policy == "" {
		policy = "smart"
	}
	sessionSticky := true
	if cfg.SessionSticky != nil {
		sessionSticky = *cfg.SessionSticky
	}
	isPerTurn := policy == "per-turn" || !sessionSticky
	isStrict := policy == "strict"
	threshold := smartSwitchThreshold(cfg)
	sessionID := handlerutil.GetSessionID(ctx)

	var activeModel string
	if !isPerTurn && sessionID != "" {
		activeModel = h.getSmartStickyModel(comboName, sessionID, models)
	}

	pinFirst := func(model string) []string {
		ordered := make([]string, 0, len(models))
		ordered = append(ordered, model)
		for _, m := range models {
			if m != model {
				ordered = append(ordered, m)
			}
		}
		return ordered
	}

	if activeModel != "" && isStrict {
		// Strict Lock: reuse the pinned model without an evaluation.
		log.Info("combo", "smart-router [strict] locked session", "session", sessionID, "model", activeModel)
		return withSmartRouteState(ctx, &smartRouteState{comboName: comboName, sessionID: sessionID, activeModel: activeModel}), pinFirst(activeModel)
	}

	if activeModel != "" {
		// Smart Switch: re-evaluate, and switch only above the floor.
		if choice := h.resolveSmartChoice(ctx, body, models, cfg.RouterModel); choice != nil {
			conf := -1.0
			if choice.Confidence != nil {
				conf = *choice.Confidence
			}
			if choice.Choice != activeModel && containsString(models, choice.Choice) && conf >= threshold {
				log.Info("combo", "smart-router [smart-switch] task changed", "from", activeModel, "to", choice.Choice, "confidence", conf, "threshold", threshold)
				h.setSmartStickyModel(comboName, sessionID, choice.Choice)
				return withSmartRouteState(ctx, &smartRouteState{comboName: comboName, sessionID: sessionID, activeModel: choice.Choice}), orderModelsBySmartChoice(models, choice)
			}
		}
		// Below the floor, on no decision, or on error: keep the warm cache.
		log.Info("combo", "smart-router [smart-switch] retained model", "model", activeModel)
		return withSmartRouteState(ctx, &smartRouteState{comboName: comboName, sessionID: sessionID, activeModel: activeModel}), pinFirst(activeModel)
	}

	// Turn 1 or per-turn: evaluate and order by the choice.
	choice := h.resolveSmartChoice(ctx, body, models, cfg.RouterModel)
	if choice == nil || !containsString(models, choice.Choice) {
		log.Info("combo", "smart-router no decision, fallback to order", "combo", comboName)
		return ctx, models
	}
	conf := -1.0
	if choice.Confidence != nil {
		conf = *choice.Confidence
	}
	if threshold > 0 && conf >= 0 && conf < threshold {
		log.Info("combo", "smart-router confidence below threshold", "confidence", conf, "threshold", threshold)
		return ctx, models
	}
	log.Info("combo", "smart-router selected", "model", choice.Choice, "confidence", conf)
	if !isPerTurn && sessionID != "" {
		h.setSmartStickyModel(comboName, sessionID, choice.Choice)
	}
	return withSmartRouteState(ctx, &smartRouteState{comboName: comboName, sessionID: sessionID, activeModel: choice.Choice}), orderModelsBySmartChoice(models, choice)
}

// withSmartRouteState attaches the smart routing state to the context.
func withSmartRouteState(ctx context.Context, state *smartRouteState) context.Context {
	return context.WithValue(ctx, smartRouteStateKey{}, state)
}

// clearFailedSmartSticky drops a session's pin when the model it was pinned to
// just failed, so the next turn re-evaluates instead of returning to a dead
// model. Upstream clears the pin at every combo failure site.
func (h *ChatHandler) clearFailedSmartSticky(ctx context.Context, failedModel string) {
	state, _ := ctx.Value(smartRouteStateKey{}).(*smartRouteState)
	if state == nil || state.sessionID == "" || state.activeModel == "" || failedModel != state.activeModel {
		return
	}
	h.clearSmartStickySession(state.comboName, state.sessionID)
}

// systemoneTargetURL mirrors the URL rules of the /v1/systemone media lane: a
// connection override or provider SystemoneURL wins, else the chat base URL's
// /chat/completions is swapped for /systemone.
func systemoneTargetURL(cfg *providers.ProviderConfig, connOverride string) string {
	override := strings.TrimSpace(connOverride)
	switch {
	case override != "" && strings.Contains(override, "/chat/completions"):
		return strings.Replace(strings.TrimRight(override, "/"), "/chat/completions", "/systemone", 1)
	case override != "":
		return buildSystemoneURLFromOverride(override, cfg)
	case cfg.SystemoneURL != "":
		return cfg.SystemoneURL
	default:
		base := strings.TrimRight(cfg.BaseURL, "/")
		if strings.Contains(base, "/chat/completions") {
			return strings.Replace(base, "/chat/completions", "/systemone", 1)
		}
		return base + "/systemone"
	}
}

func buildSystemoneURLFromOverride(override string, cfg *providers.ProviderConfig) string {
	if cfg.SystemonePath == "" {
		return strings.TrimRight(override, "/") + "/systemone"
	}
	return strings.TrimRight(override, "/") + cfg.SystemonePath
}
