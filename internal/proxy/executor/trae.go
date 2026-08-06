package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
)

// ForwardTrae routes completions through Trae's SOLO remote agent API
// (port of open-sse/executors/trae.js).
//
// Flow: POST {base}/chat_sessions creates a session and submits the first turn,
// then GET {base}/chat_sessions/{id}/events?reply_to_message_id={messageId}
// streams it back as SSE. Assistant text arrives in `plan_item` events under
// the `thought` field (cumulative per plan-item id, longest wins); `token_usage`
// carries usage; `done` ends the turn. Auth: `Authorization: Cloud-IDE-JWT`.
func ForwardTrae(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	baseURL := strings.TrimRight(req.Config.BaseURL, "/")
	headers := traeHeaders(req.APIKey, req.IsStream)

	mode, strategy, modelName := resolveTraeMode(oreq.Model)
	sessionID, messageID, err := traeCreateSession(ctx, req.Client, baseURL, headers, map[string]any{
		"mode":           mode,
		"environment_id": "default",
		"initial_message": map[string]any{
			"chat_session_id":          "",
			"content":                  []any{},
			"query":                    traeFlattenQuery(oreq.Messages),
			"model_name":               modelName,
			"agent_type":               "solo_agent_remote",
			"model_selection_strategy": strategy,
			"common_params":            traeCommonParams(mode),
		},
		"env":                 "remote",
		"auto_create_project": false,
		"origin":              "web",
	})
	if err != nil {
		return err
	}

	responseID := fmt.Sprintf("chatcmpl-trae-%d", time.Now().UnixMilli())
	created := time.Now().Unix()
	state := &traeTextState{thoughts: map[string]string{}}
	var usage *traeUsage
	var errEvent *traeErr

	// Shared per-turn event handler; stream path emits content chunks, non-stream
	// accumulates text only. Returns stop=true when the turn is done.
	base := func(choices []map[string]any) map[string]any {
		if choices == nil {
			choices = []map[string]any{}
		}
		return map[string]any{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   oreq.Model,
			"choices": choices,
		}
	}
	handleEvent := func(emit func(map[string]any) error) func(string, json.RawMessage) (bool, error) {
		return func(ev string, data json.RawMessage) (bool, error) {
			switch ev {
			case "error":
				var e traeErr
				if jerr := json.Unmarshal(data, &e); jerr != nil {
					return true, jerr
				}
				errEvent = &e
				return true, nil
			case "token_usage":
				var u traeUsage
				if jerr := json.Unmarshal(data, &u); jerr == nil {
					usage = &u
				}
			case "plan_item":
				var p struct {
					ID      string `json:"id"`
					Thought string `json:"thought"`
				}
				if jerr := json.Unmarshal(data, &p); jerr != nil {
					return false, jerr
				}
				piece := state.render(p.ID, p.Thought)
				if emit != nil && piece != "" {
					if jerr := emit(base([]map[string]any{{"index": 0, "delta": map[string]any{"content": piece}, "finish_reason": nil}})); jerr != nil {
						return false, jerr
					}
				}
			}
			return ev == "done", nil
		}
	}

	if req.IsStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)
		emit := func(chunk map[string]any) error {
			if req.TTFT != nil && *req.TTFT == 0 {
				*req.TTFT = time.Since(req.StartTime).Milliseconds()
			}
			b, jerr := json.Marshal(chunk)
			if jerr != nil {
				return jerr
			}
			if _, werr := w.Write([]byte("data: " + string(b) + "\n\n")); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		}

		if err := emit(base([]map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}})); err != nil {
			return err
		}
		if err := traeStreamUpstream(ctx, req.Client, baseURL, headers, sessionID, messageID, handleEvent(emit)); err != nil {
			chunk := base(nil)
			chunk["error"] = map[string]any{"message": "trae: " + err.Error(), "type": "api_error"}
			_ = emit(chunk)
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return nil
		}
		if errEvent != nil {
			chunk := base(nil)
			chunk["error"] = map[string]any{"message": "trae " + errEvent.Code + ": " + errEvent.Message, "type": "api_error"}
			return emit(chunk)
		}
		if err := emit(base([]map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}})); err != nil {
			return err
		}
		if usage != nil {
			chunk := base(nil)
			chunk["usage"] = usage
			if err := emit(chunk); err != nil {
				return err
			}
		}
		_, err = w.Write([]byte("data: [DONE]\n\n"))
		return err
	}

	// Non-streaming: drive the turn to completion, return chat.completion JSON.
	if err := traeStreamUpstream(ctx, req.Client, baseURL, headers, sessionID, messageID, handleEvent(nil)); err != nil {
		return &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: traeJSONError("trae: " + err.Error())}
	}
	if errEvent != nil {
		return &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: traeJSONError("trae " + errEvent.Code + ": " + errEvent.Message)}
	}
	out := map[string]any{
		"id":      responseID,
		"object":  "chat.completion",
		"created": created,
		"model":   oreq.Model,
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": state.full()}, "finish_reason": "stop"}},
	}
	if usage != nil {
		out["usage"] = usage
	}
	b, jerr := json.Marshal(out)
	if jerr != nil {
		return jerr
	}
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(b)
	return err
}

type traeUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type traeErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// traeTextState accumulates plan_item thoughts. Per plan-item id the longest
// thought wins (upstream sends cumulative text per id); pieces are emitted only
// for the delta beyond what was already sent.
type traeTextState struct {
	order    []string
	thoughts map[string]string
	sent     int
}

func (s *traeTextState) render(id, thought string) string {
	if id == "" {
		return ""
	}
	if _, ok := s.thoughts[id]; !ok {
		s.order = append(s.order, id)
	}
	if thought != "" && len(thought) >= len(s.thoughts[id]) {
		s.thoughts[id] = thought
	}
	full := s.join()
	if s.sent > len(full) {
		s.sent = len(full)
	}
	piece := full[s.sent:]
	s.sent = len(full)
	return piece
}

func (s *traeTextState) full() string { return s.join() }

func (s *traeTextState) join() string {
	var b strings.Builder
	for _, id := range s.order {
		b.WriteString(s.thoughts[id])
	}
	return b.String()
}

// traeFlattenQuery joins messages into the JSON-encoded query Trae expects.
func traeFlattenQuery(messages []json.RawMessage) string {
	var parts []string
	for _, m := range messages {
		var msg struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}
		if err := json.Unmarshal(m, &msg); err != nil {
			continue
		}
		var content string
		switch c := msg.Content.(type) {
		case string:
			content = c
		case []any:
			var b strings.Builder
			for _, p := range c {
				switch pt := p.(type) {
				case string:
					b.WriteString(pt)
				case map[string]any:
					if t, ok := pt["text"].(string); ok {
						b.WriteString(t)
					}
				}
			}
			content = b.String()
		}
		switch msg.Role {
		case "system":
			parts = append(parts, "[System]\n"+content)
		case "assistant":
			parts = append(parts, "[Assistant]\n"+content)
		default:
			parts = append(parts, content)
		}
	}
	q, _ := json.Marshal([]map[string]any{{"type": "text", "data": map[string]any{"content": strings.Join(parts, "\n\n")}}})
	return string(q)
}

// resolveTraeMode maps the OpenAI model field to a SOLO session mode:
// work / auto-work / solo-work select the fast auto lane; empty/auto select the
// model picker with auto strategy; anything else pins a specific model.
func resolveTraeMode(model string) (mode, strategy, modelName string) {
	m := strings.ToLower(strings.TrimSpace(model))
	switch m {
	case "work", "auto-work", "solo-work":
		return "work", "auto", ""
	}
	if m == "" || m == "auto" {
		return "code", "auto", ""
	}
	return "code", "manual", model
}

// traeCommonParams builds the JSON-encoded common_params identity blob.
// providerSpecificData is not carried by the executor Request, so identity
// fields fall back to the reference defaults (ponytail: add per-user psd
// overrides if paid-tier identity ever matters upstream).
func traeCommonParams(mode string) string {
	cp := map[string]any{
		"language":        "en-us",
		"app_language":    "en",
		"quality":         "stable",
		"app_version":     "1.0.0.1229",
		"web_id":          "",
		"user_identity":   "Free",
		"is_freshman":     "0",
		"biz_user_id":     "",
		"user_unique_id":  "",
		"scope":           "marscode-us",
		"tenant":          "marscode",
		"region":          "US-East",
		"aiRegion":        "US-East",
		"is_privacy_mode": 0,
		"privacy_mode":    "off",
		"solo_chat_mode":  mode,
	}
	b, _ := json.Marshal(cp)
	return string(b)
}

func traeHeaders(token string, stream bool) map[string]string {
	accept := "application/json"
	if stream {
		accept = "text/event-stream"
	}
	return map[string]string{
		"Authorization":          "Cloud-IDE-JWT " + token,
		"Content-Type":           "application/json",
		"X-Trae-Client-Type":     "web",
		"X-Preferenced-Language": "en",
		"x-user-region":          "US",
		"Referer":                "https://solo.trae.ai/",
		"User-Agent":             "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36",
		"Accept":                 accept,
	}
}

// traeCreateSession POSTs the first turn and returns the session/message ids.
func traeCreateSession(ctx context.Context, client *http.Client, baseURL string, headers map[string]string, body map[string]any) (string, string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat_sessions", bytes.NewReader(b))
	if err != nil {
		return "", "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: traeJSONError(err.Error())}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: respBody}
	}
	var out struct {
		Code int `json:"code"`
		Data struct {
			ChatSessionID string `json:"chat_session_id"`
			MessageID     string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil || out.Code != 0 || out.Data.ChatSessionID == "" || out.Data.MessageID == "" {
		return "", "", &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: respBody}
	}
	return out.Data.ChatSessionID, out.Data.MessageID, nil
}

// traeStreamUpstream reads the events SSE until onEvent returns stop=true, the
// stream ends, or an error. Non-parseable data lines are passed through as
// {"_raw": ...} (mirroring the reference implementation).
func traeStreamUpstream(ctx context.Context, client *http.Client, baseURL string, headers map[string]string, sessionID, messageID string, onEvent func(string, json.RawMessage) (bool, error)) error {
	u := fmt.Sprintf("%s/chat_sessions/%s/events?reply_to_message_id=%s", baseURL, url.PathEscape(sessionID), url.QueryEscape(messageID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body}
	}

	reader := bufio.NewReader(resp.Body)
	ev := ""
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event:"):
			ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				break
			}
			var data json.RawMessage
			if jerr := json.Unmarshal([]byte(payload), &data); jerr != nil {
				raw, _ := json.Marshal(payload)
				data = json.RawMessage(`{"_raw":` + string(raw) + `}`)
			}
			stop, eerr := onEvent(ev, data)
			if eerr != nil {
				return eerr
			}
			if stop {
				return nil
			}
		case line == "":
			ev = ""
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

func traeJSONError(message string) []byte {
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "api_error", "code": ""},
	})
	return b
}
