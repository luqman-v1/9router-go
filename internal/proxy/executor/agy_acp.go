package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"9router/proxy/internal/acp/agy"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// ForwardAGYACP serves OpenAI Chat Completions through the official agy CLI
// over ACP. It is intentionally a separate provider from antigravity: no
// cross-provider aliasing, no shared HTTP transport.
func ForwardAGYACP(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var chat agy.ChatRequest
	if err := json.Unmarshal(req.Body, &chat); err != nil {
		return &proxy.UpstreamError{StatusCode: http.StatusBadRequest, Body: jsonError("agy-acp: invalid request body: " + err.Error())}
	}
	model := chat.Model
	if model == "" {
		model = req.ModelName
	}
	prompt := agy.BuildPrompt(&chat)

	// Resolve credentials. Reference mode reads/refreshes the source
	// antigravity connection; copy/manual mode uses the connection's own token.
	var creds agy.Credentials
	if req.ACPCredentials != nil {
		access, refresh, expiry, err := req.ACPCredentials(ctx, req.ConnData, req.ConnectionID)
		if err != nil {
			return acpError(err)
		}
		creds = agy.Credentials{AccessToken: access, RefreshToken: refresh, ExpiresAt: expiry}
	} else if req.APIKey != "" {
		// Fallback for callers that only have a single token: agy can adopt it
		// as an access token, but a refresh token is required for file storage.
		creds = agy.Credentials{AccessToken: req.APIKey, RefreshToken: req.APIKey}
	}

	accountRoot := acpAccountRoot(req)
	permMode := "accept-edits"
	if v, ok := req.ConnData["permissionMode"].(string); ok && v != "" {
		permMode = v
	}
	opts := agy.TurnOptions{
		AccountRoot:    accountRoot,
		Credentials:    creds,
		PermissionMode: permMode,
		MCPTools:       agy.ToolsFromOpenAI(chat.Tools),
		MCPSeeded:      seedToolResults(chat.Messages),
	}
	if v, ok := req.ConnData["agyPath"].(string); ok && v != "" {
		opts.Binary = v
	}

	release, err := agy.DefaultManager().Acquire(ctx)
	if err != nil {
		return &proxy.UpstreamError{StatusCode: http.StatusServiceUnavailable, Body: jsonError("agy-acp: " + err.Error())}
	}
	defer release()

	responseID := fmt.Sprintf("chatcmpl-agyacp-%d", time.Now().UnixMilli())
	created := time.Now().Unix()
	var text string
	var thinking string
	var turnErr error
	var toolCalls []map[string]any
	onEvent := func(ev agy.TurnEvent) {
		if ev.Err != nil && turnErr == nil {
			turnErr = ev.Err
		}
		switch ev.Kind {
		case agy.UpdateText:
			text += ev.Text
		case agy.UpdateThinking:
			thinking += ev.Text
		case agy.UpdateToolCall:
			if ev.ToolCall != nil {
				toolCalls = append(toolCalls, map[string]any{
					"id":   ev.ToolCall.ID,
					"type": "function",
					"function": map[string]any{
						"name":      ev.ToolCall.Name,
						"arguments": string(ev.ToolCall.Input),
					},
				})
			}
		}
		if req.ResponseBuf != nil && ev.Text != "" {
			_, _ = req.ResponseBuf.Write([]byte(ev.Text))
		}
	}

	if req.IsStream {
		// The chunks this executor builds are OpenAI chunks. A Chat
		// Completions client reads them as they are; a /v1/messages client
		// cannot, so they are translated into Claude events on the way out
		// (req.TranslateResp), and a /v1/responses client gets the Responses
		// bridge. Without this routing a Claude client received OpenAI frames
		// it cannot parse — the same wrong-format turn gemini-web guards
		// against (gemini_web_stream.go:144-152).
		hw := proxy.NewHeartbeatWriter(req.Ctx, w, 0)
		var flusher http.Flusher
		opened := false
		open := func() {
			if !opened {
				opened = true
				flusher = proxy.WriteSSEHeaders(hw)
			}
		}
		sessionKey := translator.NewSessionKey("agy-acp-stream")
		claude := req.TranslateResp
		var bridge *ResponsesBridge
		if translator.NeedsResponsesBridge(req.Ctx) {
			bridge = NewResponsesBridge(
				translator.RequestedModelFromContext(req.Ctx),
				translator.CustomToolNamesFrom(req.Ctx),
				func(frame []byte) error {
					open()
					if req.ResponseBuf != nil {
						_, _ = req.ResponseBuf.Write(frame)
					}
					if _, err := hw.Write(frame); err != nil {
						return err
					}
					if flusher != nil {
						flusher.Flush()
					}
					return nil
				},
			)
		}
		emit := func(chunk map[string]any) error {
			markTTFT(req, req.StartTime)
			b, jerr := json.Marshal(chunk)
			if jerr != nil {
				return jerr
			}
			if bridge != nil {
				bridge.Feed(b)
				return bridge.Err()
			}
			frame := fmt.Appendf(nil, "data: %s\n\n", b)
			if claude {
				translated, terr := translator.TranslateOpenAIToClaudeStreamSession(sessionKey, b)
				if terr != nil {
					return terr
				}
				if translated != nil {
					frame = translated
				}
			}
			open()
			if _, werr := hw.Write(frame); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		}
		base := func(choices []map[string]any) map[string]any {
			return map[string]any{
				"id": responseID, "object": "chat.completion.chunk",
				"created": created, "model": model, "choices": choices,
			}
		}
		if err := emit(base([]map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}})); err != nil {
			return err
		}
		first := true
		streamErr := agy.RunTurn(ctx, opts, model, prompt, func(ev agy.TurnEvent) {
			onEvent(ev)
			if ev.Kind == agy.UpdateThinking && ev.Text != "" && thinking != "" {
				// Thinking is emitted as reasoning_content when it leads the turn.
				_ = emit(base([]map[string]any{{"index": 0, "delta": map[string]any{"reasoning_content": ev.Text}, "finish_reason": nil}}))
				first = false
			}
			if ev.Kind == agy.UpdateText && ev.Text != "" {
				if first {
					markTTFT(req, req.StartTime)
				}
				first = false
				_ = emit(base([]map[string]any{{"index": 0, "delta": map[string]any{"content": ev.Text}, "finish_reason": nil}}))
			}
		})
		if streamErr != nil {
			if !opened {
				// Nothing is on the wire yet, so the failure can still be a
				// status the router can act on instead of a 200 carrying nothing.
				_ = hw.Close()
				return acpError(streamErr)
			}
			status, message := proxy.ClassifyStreamAbort(streamErr)
			format := proxy.SSEFormatOpenAI
			if claude {
				format = proxy.SSEFormatClaude
			}
			_, _ = hw.Write(proxy.BuildStreamErrorBytes(status, message, format))
			if claude {
				translator.ClearStreamState(sessionKey)
			}
			_ = hw.Close()
			return nil // error already delivered in-band
		}
		finish := "stop"
		if len(toolCalls) > 0 {
			finish = "tool_calls"
			// Emit one delta per call with a stable index.
			for i, tc := range toolCalls {
				item := map[string]any{"index": i, "id": tc["id"], "type": "function", "function": tc["function"]}
				if err := emit(base([]map[string]any{{"index": 0, "delta": map[string]any{"tool_calls": []map[string]any{item}}, "finish_reason": nil}})); err != nil {
					return err
				}
			}
		}
		if err := emit(base([]map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finish}})); err != nil {
			return err
		}
		if bridge != nil {
			// A Responses client waits for response.completed, not [DONE].
			bridge.StopCompletionWatchdog()
			bridge.Close()
			return hw.Close()
		}
		if claude {
			// The Claude stream ends with message_delta and message_stop, which
			// the translator emits with the finish reason. Ensure closure even
			// if no finish chunk carried one, or the client waits forever.
			if end := translator.EnsureStreamClosed(sessionKey); len(end) > 0 {
				if _, err := hw.Write(end); err != nil {
					return err
				}
			}
			translator.ClearStreamState(sessionKey)
			return hw.Close()
		}
		if _, err := hw.Write([]byte("data: [DONE]\n\n")); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return hw.Close()
	}

	// Non-streaming: accumulate the turn and return one chat.completion.
	if err := agy.RunTurn(ctx, opts, model, prompt, onEvent); err != nil {
		return acpError(err)
	}
	message := map[string]any{"role": "assistant", "content": text}
	if thinking != "" {
		message["reasoning_content"] = thinking
	}
	finish := "stop"
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		finish = "tool_calls"
	}
	out := map[string]any{
		"id": responseID, "object": "chat.completion", "created": created, "model": model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finish}},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	// Route the Chat Completions body through the shared write path so a
	// /v1/messages client gets a Claude Message (req.TranslateResp) instead of
	// a Chat body it cannot parse. onEvent already mirrored the answer text
	// into req.ResponseBuf, so pass a nil buffer here to avoid double-counting.
	return jsonResponse(req.Ctx, w, bytes.NewReader(b), req.TranslateResp, nil)
}

func acpAccountRoot(req *Request) string {
	base := ""
	if v, ok := req.ConnData["accountRoot"].(string); ok && v != "" {
		base = v
	}
	if base == "" {
		dataRoot := os.Getenv("AGY_ACP_DATA_DIR")
		if dataRoot == "" {
			if xdg, ok := os.LookupEnv("XDG_DATA_HOME"); xdg != "" && ok {
				dataRoot = filepath.Join(xdg, "9router", "agy-acp")
			} else if home, err := os.UserHomeDir(); err == nil {
				dataRoot = filepath.Join(home, ".local", "share", "9router", "agy-acp")
			} else {
				dataRoot = filepath.Join(os.TempDir(), "9router-agy-acp")
			}
		}
		base = filepath.Join(dataRoot, sanitizeID(req.ConnectionID))
	}
	return base
}

func sanitizeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			out = append(out, r)
		} else {
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "default"
	}
	return string(out)
}

func acpError(err error) error {
	if err == nil {
		return nil
	}
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, agy.ErrAuth):
		status = http.StatusUnauthorized
	case errors.Is(err, agy.ErrQuota):
		status = http.StatusTooManyRequests
	case errors.Is(err, agy.ErrPolicy):
		status = http.StatusForbidden
	case errors.Is(err, agy.ErrBinary):
		status = http.StatusServiceUnavailable
	case errors.Is(err, agy.ErrNoModel), errors.Is(err, agy.ErrNoCred), errors.Is(err, agy.ErrStall):
	}
	return &proxy.UpstreamError{StatusCode: status, Body: jsonError("agy-acp: " + err.Error())}
}

func jsonError(msg string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "api_error"}})
	return b
}

// seedToolResults maps tool names to their prior results from the OpenAI
// message history, so a multi-turn tool call gets the real data instead of an
// "awaiting result" placeholder.
func seedToolResults(messages []agy.ChatMessage) map[string]string {
	byID := map[string]string{}
	byName := map[string]string{}
	for _, m := range messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var calls []struct {
				ID       string `json:"id"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			if json.Unmarshal(m.ToolCalls, &calls) == nil {
				for _, c := range calls {
					byID[c.ID] = c.Function.Name
				}
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			if name := byID[m.ToolCallID]; name != "" {
				byName[name] = chatMessageText(m.Content)
			}
		}
	}
	return byName
}

func chatMessageText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var out string
		for _, b := range blocks {
			if b.Type == "text" {
				out += b.Text
			}
		}
		return out
	}
	return ""
}
