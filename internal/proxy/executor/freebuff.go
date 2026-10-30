package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
)

const (
	freebuffRunPath       = "/api/v1/agent-runs"
	freebuffSystemMarker  = "You are Buffy, the strategic coding assistant."
	freebuffChatUserAgent = "ai-sdk/openai-compatible/1.0/codebuff"
)

var freebuffRootAgentByModel = map[string]string{
	"deepseek/deepseek-v4-flash":      "base3-free-deepseek-flash",
	"z-ai/glm-5.2":                    "base3-free-glm",
	"z-ai/glm-5.3-flash":              "base3-free-glm-5-3-flash",
	"mimo/mimo-v2.5":                  "base3-free-mimo",
	"openai/gpt-5.6-luna":             "base3-free-luna",
	"upstage/solar-pro4":              "base3-free-solar-pro4",
	"meta/muse-spark-1.2-contributor": "base3-free-muse-spark",
	"anthropic/claude-fable-5":        "base3-free-fable",
}

var freebuffRootOpenings = []string{
	"You are Buffy, the strategic coding assistant.",
	"You are Buffy, the Freebuff Cloud project planner.",
	"You are Buffy, a strategic assistant that orchestrates complex coding tasks through specialized sub-agents.",
}

func rootAgentIdForModel(model string) string {
	if root, ok := freebuffRootAgentByModel[model]; ok {
		return root
	}
	return "base2-free"
}

func freebuffOrigin(baseURL string) string {
	if baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return "https://www.codebuff.com"
}

func ensureFreebuffMarker(body map[string]any) {
	rawMsgs, ok := body["messages"].([]any)
	if !ok || len(rawMsgs) == 0 {
		body["messages"] = []any{
			map[string]any{"role": "system", "content": freebuffSystemMarker},
		}
		return
	}

	first, ok := rawMsgs[0].(map[string]any)
	if ok && first["role"] == "system" {
		content, _ := first["content"].(string)
		trimmed := strings.TrimSpace(content)
		for _, opening := range freebuffRootOpenings {
			if strings.HasPrefix(trimmed, opening) {
				return // already marked
			}
		}
		first["content"] = freebuffSystemMarker + "\n\n" + content
		rawMsgs[0] = first
		body["messages"] = rawMsgs
		return
	}

	// No leading system message — prepend one
	newMsgs := make([]any, 0, len(rawMsgs)+1)
	newMsgs = append(newMsgs, map[string]any{"role": "system", "content": freebuffSystemMarker})
	newMsgs = append(newMsgs, rawMsgs...)
	body["messages"] = newMsgs
}

func ensureFreebuffEndTurnTool(body map[string]any) {
	rawTools, ok := body["tools"].([]any)
	if !ok || len(rawTools) == 0 {
		return
	}

	for _, t := range rawTools {
		if tm, ok := t.(map[string]any); ok {
			if fn, ok := tm["function"].(map[string]any); ok {
				if fn["name"] == "end_turn" {
					return
				}
			}
		}
	}

	endTurnTool := map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "end_turn",
			"description": "Signal the end of the current task.",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
	body["tools"] = append(rawTools, endTurnTool)
}

func startFreebuffRun(ctx context.Context, client *http.Client, baseURL, token, model string, headers map[string]string) (string, error) {
	origin := freebuffOrigin(baseURL)
	reqURL := origin + freebuffRunPath

	payload := map[string]any{
		"action":         "START",
		"agentId":        rootAgentIdForModel(model),
		"ancestorRunIds": []string{},
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create freebuff run request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", freebuffCLIUserAgent)

	resp, err := DoFreebuffHTTP(ctx, client, req)
	if err != nil {
		return "", fmt.Errorf("start freebuff run failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return "", fmt.Errorf("read freebuff run response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBytes}
	}

	var data struct {
		RunID string `json:"runId"`
	}
	if err := json.Unmarshal(respBytes, &data); err != nil || data.RunID == "" {
		return "", fmt.Errorf("freebuff run start returned no runId: %s", string(respBytes))
	}
	return data.RunID, nil
}

func finishFreebuffRun(ctx context.Context, client *http.Client, baseURL, token, runID, status string, headers map[string]string) {
	if runID == "" {
		return
	}
	origin := freebuffOrigin(baseURL)
	reqURL := origin + freebuffRunPath
	payload := map[string]any{
		"action": "FINISH",
		"runId":  runID,
		"status": status,
	}
	bodyBytes, _ := json.Marshal(payload)

	// Best-effort finish with bounded 5s timeout
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(finishCtx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", freebuffCLIUserAgent)

	resp, err := DoFreebuffHTTP(finishCtx, client, req)
	if err == nil {
		resp.Body.Close()
	}
}

// ForwardFreebuff handles chat requests for the Freebuff provider.
func ForwardFreebuff(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	token := req.APIKey
	if token == "" {
		return fmt.Errorf("freebuff: access token is required")
	}

	var parsedBody map[string]any
	if err := json.Unmarshal(req.Body, &parsedBody); err != nil {
		return fmt.Errorf("freebuff: unmarshal request body: %w", err)
	}

	model, _ := parsedBody["model"].(string)
	if model == "" {
		model = "deepseek/deepseek-v4-flash"
	}

	// 1. Session acquisition: memory L1, cross-process lease L2, coordinated
	// admission (exactly one claimer per token+model across processes).
	store := req.Leases
	sess, ok := getFreebuffSessionWithStore(store, token, model)
	if !ok {
		var err error
		sess, err = requestFreebuffSessionWithStore(ctx, store, client, req.Config.BaseURL, token, model, req.Config.StaticHeaders)
		if err != nil {
			log.Warn("freebuff", "session request failed", "model", model, "error", err)
			var ue *proxy.UpstreamError
			if errors.As(err, &ue) && ue.StatusCode == http.StatusConflict {
				if w != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write(ue.Body)
				}
			}
			return err
		}
	}

	// 2. Start Agent Run
	runID, err := startFreebuffRun(ctx, client, req.Config.BaseURL, token, model, req.Config.StaticHeaders)
	if err != nil {
		log.Warn("freebuff", "run start failed", "model", model, "error", err)
		return err
	}
	runStatus := "failed"
	defer func() {
		finishFreebuffRun(context.Background(), client, req.Config.BaseURL, token, runID, runStatus, req.Config.StaticHeaders)
	}()
	// 3. Transform body
	delete(parsedBody, "reasoning_effort")
	delete(parsedBody, "reasoning")

	ensureFreebuffMarker(parsedBody)
	ensureFreebuffEndTurnTool(parsedBody)

	parsedBody["provider"] = map[string]any{
		"allow_fallbacks": false,
	}

	// client_id cloaking (CLI-shaped fingerprint): the backend logs and
	// rate-limits per client_id, so a per-request "9router-<uuid>" brands
	// every call as non-CLI traffic. Reuse the account's login fingerprintId
	// (handed down via req.ConnData), exactly like the dashboard fork; only
	// mint a fresh random id for connections that predate the stored field.
	clientID := ""
	if req.ConnData != nil {
		clientID, _ = req.ConnData["fingerprintId"].(string)
	}
	if clientID == "" {
		clientID = uuid.New().String()
	}
	codebuffMeta := map[string]any{
		"client_id":        clientID,
		"cost_mode":        "free",
		"run_id":           runID,
		"trace_session_id": uuid.New().String(),
	}
	if sess != nil && sess.InstanceID != "" {
		codebuffMeta["freebuff_instance_id"] = sess.InstanceID
	}
	parsedBody["codebuff_metadata"] = codebuffMeta

	transformedBody, err := json.Marshal(parsedBody)
	if err != nil {
		return fmt.Errorf("freebuff: marshal transformed body: %w", err)
	}

	// 4. Execute Chat Request
	doChat := func(body []byte) (*http.Response, error) {
		headers := map[string]string{
			"Authorization": "Bearer " + token,
			"User-Agent":    freebuffChatUserAgent,
		}
		if req.IsStream {
			headers["Accept"] = "text/event-stream"
		}
		for k, v := range req.Config.StaticHeaders {
			headers[k] = v
		}
		return proxy.DoRequest(ctx, client, http.MethodPost, req.Config.BaseURL, headers, body)
	}

	resp, err := doChat(transformedBody)
	if err != nil {
		return fmt.Errorf("freebuff upstream: %w", err)
	}

	// Stale session check (428 waiting_room_required, 409 session_superseded, 410 session_expired)
	if resp.StatusCode == 428 || resp.StatusCode == 409 || resp.StatusCode == 410 {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()

		var errData struct {
			Status             string `json:"status"`
			Error              string `json:"error"`
			Message            string `json:"message"`
			CurrentModel       string `json:"currentModel"`
			CountryCode        string `json:"countryCode"`
			CountryBlockReason string `json:"countryBlockReason"`
		}
		_ = json.Unmarshal(respBytes, &errData)

		// If model is locked, write 409 and return structured error
		if errData.Status == "model_locked" || errData.Error == "model_locked" || strings.Contains(string(respBytes), "model_locked") {
			currentModel := errData.CurrentModel
			if currentModel == "" {
				currentModel = model
			}
			return newModelLockedError(w, currentModel, model)
		}

		// A blocked region is not a stale seat: re-claiming would loop.
		if freebuffCountryRefusal(respBytes) {
			return newCountryBlockedError(w, errData.CountryCode, errData.CountryBlockReason)
		}

		// If limited IP, fail immediately
		if strings.Contains(strings.ToLower(errData.Message), "limited") {
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBytes}
		}

		// Otherwise force re-claim session once and retry. Drop the lease
		// only when it still holds OUR instance id: a sibling's fresh row
		// must survive (compare-and-delete), or the hijack war restarts.
		if sess != nil {
			dropFreebuffLease(store, token, model, sess.InstanceID)
		}
		clearFreebuffSession(token, model)
		newSess, sessErr := requestFreebuffSessionWithStore(ctx, store, client, req.Config.BaseURL, token, model, req.Config.StaticHeaders)
		if sessErr == nil && newSess != nil {
			sess = newSess
			codebuffMeta["freebuff_instance_id"] = sess.InstanceID
			parsedBody["codebuff_metadata"] = codebuffMeta
			if tb, err := json.Marshal(parsedBody); err == nil {
				transformedBody = tb
				resp, err = doChat(transformedBody)
				if err != nil {
					return fmt.Errorf("freebuff upstream retry: %w", err)
				}
			}
		} else {
			if sessErr != nil {
				var ue *proxy.UpstreamError
				if errors.As(sessErr, &ue) && ue.StatusCode == http.StatusConflict {
					if w != nil {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write(ue.Body)
					}
				}
				return sessErr
			}
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBytes}
		}
	}

	if resp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()

		var errData struct {
			Status             string `json:"status"`
			Error              string `json:"error"`
			CurrentModel       string `json:"currentModel"`
			CountryCode        string `json:"countryCode"`
			CountryBlockReason string `json:"countryBlockReason"`
		}
		_ = json.Unmarshal(respBytes, &errData)
		if errData.Status == "model_locked" || errData.Error == "model_locked" || strings.Contains(string(respBytes), "model_locked") {
			currentModel := errData.CurrentModel
			if currentModel == "" {
				currentModel = model
			}
			return newModelLockedError(w, currentModel, model)
		}
		if freebuffCountryRefusal(respBytes) {
			return newCountryBlockedError(w, errData.CountryCode, errData.CountryBlockReason)
		}
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBytes}
	}

	runStatus = "completed"

	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(ctx, resp.Body, 0, "freebuff")
		defer stallReader.Close()
		return execSSEStream(w, stallReader, req)
	}

	defer resp.Body.Close()
	return jsonResponse(ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}
