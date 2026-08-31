package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
)

// ForwardWindsurf routes completions through Codeium's gRPC-web chat endpoint
// (port of open-sse/executors/windsurf.js).
//
// Wire protocol: gRPC-web over HTTPS (Content-Type: application/grpc-web+proto),
// service exa.language_server_pb.LanguageServerService / GetChatMessage. The
// request body is a hand-rolled protobuf GetChatMessageRequest wrapped in a
// gRPC-web frame (0x00 flag + big-endian length + payload); the response is a
// stream of gRPC-web frames carrying CompletionChunk messages (field 1 = text
// content, field 3 = done+usage, field 4 = error). Auth is the Codeium apiKey,
// placed both in the Bearer header and in Metadata.api_key.

const (
	wsChatURL  = "https://server.codeium.com/exa.language_server_pb.LanguageServerService/GetChatMessage"
	wsIDEName  = "windsurf"
	wsIDEVers  = "3.14.0"
	wsExtVers  = "3.14.0"
	wsLocale   = "en-US"
	wsReadSize = 32 << 10
)

// wsModelAliases maps dashboard catalog model names to Windsurf wire names.
func wsModelAliases() map[string]string {
	return map[string]string{
		"swe-1.6-fast": "swe-1-6-fast", "swe-1.6": "swe-1-6",
		"swe-1.5-fast": "swe-1-5-fast", "swe-1.5": "swe-1-5",
		"claude-opus-4.7-max": "claude-opus-4-7-max", "claude-opus-4.7-xhigh": "claude-opus-4-7-xhigh",
		"claude-opus-4.7-high": "claude-opus-4-7-high", "claude-opus-4.7-medium": "claude-opus-4-7-medium",
		"claude-opus-4.7-low": "claude-opus-4-7-low", "claude-opus-4.7-review": "opus-4-7-review",
		"claude-sonnet-4.6-thinking-1m": "claude-sonnet-4-6-thinking-1m", "claude-sonnet-4.6-1m": "claude-sonnet-4-6-1m",
		"claude-sonnet-4.6-thinking": "claude-sonnet-4-6-thinking", "claude-sonnet-4.6": "claude-sonnet-4-6",
		"claude-opus-4.6-thinking": "claude-opus-4-6-thinking", "claude-opus-4.6": "claude-opus-4-6",
		"claude-opus-4.5-thinking": "MODEL_CLAUDE_4_5_OPUS_THINKING", "claude-opus-4.5": "MODEL_CLAUDE_4_5_OPUS",
		"claude-sonnet-4.5-thinking": "MODEL_PRIVATE_3", "claude-sonnet-4.5": "MODEL_PRIVATE_2",
		"claude-haiku-4.5":   "MODEL_PRIVATE_11",
		"gpt-5.5-xhigh-fast": "gpt-5-5-xhigh-priority", "gpt-5.5-high-fast": "gpt-5-5-high-priority",
		"gpt-5.5-medium-fast": "gpt-5-5-medium-priority", "gpt-5.5-low-fast": "gpt-5-5-low-priority",
		"gpt-5.5-none-fast": "gpt-5-5-none-priority", "gpt-5.5-xhigh": "gpt-5-5-xhigh",
		"gpt-5.5-high": "gpt-5-5-high", "gpt-5.5-medium": "gpt-5-5-medium",
		"gpt-5.5-low": "gpt-5-5-low", "gpt-5.5-none": "gpt-5-5-none",
		"gpt-5.5-review": "gpt-5-5-review", "gpt-5.5": "gpt-5-5-medium",
		"gpt-5.4-xhigh-fast": "gpt-5-4-xhigh-priority", "gpt-5.4-high-fast": "gpt-5-4-high-priority",
		"gpt-5.4-medium-fast": "gpt-5-4-medium-priority", "gpt-5.4-low-fast": "gpt-5-4-low-priority",
		"gpt-5.4-none-fast": "gpt-5-4-none-priority", "gpt-5.4-xhigh": "gpt-5-4-xhigh",
		"gpt-5.4-high": "gpt-5-4-high", "gpt-5.4-medium": "gpt-5-4-medium",
		"gpt-5.4-low": "gpt-5-4-low", "gpt-5.4-none": "gpt-5-4-none",
		"gpt-5.4-mini-xhigh": "gpt-5-4-mini-xhigh", "gpt-5.4-mini-high": "gpt-5-4-mini-high",
		"gpt-5.4-mini-medium": "gpt-5-4-mini-medium", "gpt-5.4-mini-low": "gpt-5-4-mini-low",
		"gpt-5.4":                  "gpt-5-4-medium",
		"gpt-5.3-codex-xhigh-fast": "gpt-5-3-codex-xhigh-priority", "gpt-5.3-codex-high-fast": "gpt-5-3-codex-high-priority",
		"gpt-5.3-codex-medium-fast": "gpt-5-3-codex-medium-priority", "gpt-5.3-codex-low-fast": "gpt-5-3-codex-low-priority",
		"gpt-5.3-codex-xhigh": "gpt-5-3-codex-xhigh", "gpt-5.3-codex-high": "gpt-5-3-codex-high",
		"gpt-5.3-codex-medium": "gpt-5-3-codex-medium", "gpt-5.3-codex-low": "gpt-5-3-codex-low",
		"gpt-5.3-codex": "gpt-5-3-codex-medium",
		"gpt-5.2-xhigh": "MODEL_GPT_5_2_XHIGH", "gpt-5.2-high": "MODEL_GPT_5_2_HIGH",
		"gpt-5.2-medium": "MODEL_GPT_5_2_MEDIUM", "gpt-5.2-low": "MODEL_GPT_5_2_LOW",
		"gpt-5.2-none": "MODEL_GPT_5_2_NONE", "gpt-5.2": "MODEL_GPT_5_2_MEDIUM",
		"gpt-5":   "gpt-5",
		"gpt-4.1": "MODEL_CHAT_GPT_4_1_2025_04_14", "gpt-4.1-mini": "gpt-4.1-mini",
		"gpt-4o":              "MODEL_CHAT_GPT_4O_2024_08_06",
		"gemini-3.1-pro-high": "gemini-3-1-pro-high", "gemini-3.1-pro-low": "gemini-3-1-pro-low",
		"gemini-3.1-pro":        "gemini-3-1-pro-high",
		"gemini-3.0-flash-high": "MODEL_GOOGLE_GEMINI_3_0_FLASH_HIGH", "gemini-3.0-flash-medium": "MODEL_GOOGLE_GEMINI_3_0_FLASH_MEDIUM",
		"gemini-3.0-flash-low": "MODEL_GOOGLE_GEMINI_3_0_FLASH_LOW", "gemini-3.0-flash-minimal": "MODEL_GOOGLE_GEMINI_3_0_FLASH_MINIMAL",
		"gemini-3.0-flash": "MODEL_GOOGLE_GEMINI_3_0_FLASH_HIGH", "gemini-2.5-pro": "MODEL_GOOGLE_GEMINI_2_5_PRO",
		"deepseek-v4": "deepseek-v4", "kimi-k2.6": "kimi-k2-6", "kimi-k2.5": "kimi-k2-5",
		"glm-5.1": "glm-5-1",
	}
}

func resolveWsModelID(model string) string {
	if v, ok := wsModelAliases()[model]; ok {
		return v
	}
	return model
}

// ─── Minimal protobuf encoder (wire types 0=varint, 2=length-delimited) ─────

func wsVarint(v uint64) []byte {
	var out []byte
	for v >= 0x80 {
		out = append(out, byte(v)|0x80)
		v >>= 7
	}
	out = append(out, byte(v))
	return out
}

func wsTag(fieldNum, wireType int) []byte { return wsVarint(uint64(fieldNum<<3 | wireType)) }

func wsField(tag, payload []byte) []byte {
	out := append([]byte{}, tag...)
	out = append(out, wsVarint(uint64(len(payload)))...)
	return append(out, payload...)
}

func wsStr(fieldNum int, s string) []byte   { return wsField(wsTag(fieldNum, 2), []byte(s)) }
func wsMsg(fieldNum int, msg []byte) []byte { return wsField(wsTag(fieldNum, 2), msg) }
func wsVarintField(fieldNum int, v uint64) []byte {
	return append(wsTag(fieldNum, 0), wsVarint(v)...)
}

func wsMetadata(apiKey, sessionID string) []byte {
	return bytes.Join([][]byte{
		wsStr(1, apiKey), wsStr(2, wsIDEName), wsStr(3, wsIDEVers),
		wsStr(4, wsExtVers), wsStr(5, sessionID), wsStr(6, wsLocale),
	}, nil)
}

func wsChatMessage(role, content, toolCallID string) []byte {
	parts := [][]byte{wsStr(1, role), wsStr(2, content)}
	if toolCallID != "" {
		parts = append(parts, wsStr(3, toolCallID))
	}
	return bytes.Join(parts, nil)
}

func wsGetChatMessageRequest(apiKey, model string, messages [][]byte) []byte {
	parts := [][]byte{
		wsMsg(1, wsMetadata(apiKey, wsRandHex(16))), // metadata
		wsStr(2, wsRandHex(16)),                     // cascade_id
		wsMsg(3, wsStr(1, model)),                   // model_or_alias
	}
	for _, m := range messages {
		parts = append(parts, wsMsg(4, m)) // repeated messages
	}
	return bytes.Join(parts, nil)
}

// wsGRPCWebFrame wraps a protobuf payload in a gRPC-web frame:
// 1 flag byte (0x00 = no compression) + big-endian 32-bit length + payload.
func wsGRPCWebFrame(payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = 0x00
	frame[1] = byte(len(payload) >> 24)
	frame[2] = byte(len(payload) >> 16)
	frame[3] = byte(len(payload) >> 8)
	frame[4] = byte(len(payload))
	copy(frame[5:], payload)
	return frame
}

// ─── Protobuf CompletionChunk decoder ───────────────────────────────────────

type wsChunk struct {
	kind             string // "content" | "done" | "error" | "unknown"
	text             string
	promptTokens     int
	completionTokens int
	message          string
}

func wsReadVarint(buf []byte, off int) (uint64, int) {
	var result uint64
	var shift uint
	for off < len(buf) {
		b := buf[off]
		off++
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
	}
	return result, off
}

// wsDecodeStringField walks a message buffer looking for the first field with
// fieldNum==target, returning its string payload. Empty/absent → "".
func wsDecodeStringField(buf []byte, target int) string {
	off := 0
	for off < len(buf) {
		tag, o := wsReadVarint(buf, off)
		off = o
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x07)
		switch wireType {
		case 2:
			ln, o := wsReadVarint(buf, off)
			off = o
			if ln > uint64(len(buf)-off) {
				return ""
			}
			if fieldNum == target {
				return string(buf[off : off+int(ln)])
			}
			off += int(ln)
		case 0:
			_, off = wsReadVarint(buf, off)
		case 1:
			off += 8
		case 5:
			off += 4
		default:
			return ""
		}
	}
	return ""
}

// wsDecodeDoneChunk parses DoneChunk{field 1 = UsageStats{1=prompt, 2=completion}}.
func wsDecodeDoneChunk(buf []byte) (int, int) {
	off := 0
	var usage []byte
	for off < len(buf) {
		tag, o := wsReadVarint(buf, off)
		off = o
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x07)
		if wireType == 2 {
			ln, o := wsReadVarint(buf, off)
			off = o
			if ln > uint64(len(buf)-off) {
				break
			}
			if fieldNum == 1 {
				usage = buf[off : off+int(ln)]
			}
			off += int(ln)
		} else if wireType == 0 {
			_, off = wsReadVarint(buf, off)
		} else {
			break
		}
	}
	if usage == nil {
		return 0, 0
	}
	var prompt, completion int
	off = 0
	for off < len(usage) {
		tag, o := wsReadVarint(usage, off)
		off = o
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x07)
		if wireType == 0 {
			v, o := wsReadVarint(usage, off)
			off = o
			if fieldNum == 1 {
				prompt = int(v)
			} else if fieldNum == 2 {
				completion = int(v)
			}
		} else if wireType == 2 {
			ln, o := wsReadVarint(usage, off)
			off = o
			off += int(ln)
		} else {
			break
		}
	}
	return prompt, completion
}

// wsDecodeCompletionChunk decodes one CompletionChunk (oneof: 1=ContentChunk,
// 2=ToolCallChunk skipped, 3=DoneChunk, 4=ErrorChunk).
func wsDecodeCompletionChunk(buf []byte) wsChunk {
	off := 0
	for off < len(buf) {
		tag, o := wsReadVarint(buf, off)
		off = o
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x07)
		if wireType == 2 {
			ln, o := wsReadVarint(buf, off)
			off = o
			if ln > uint64(len(buf)-off) {
				break
			}
			payload := buf[off : off+int(ln)]
			off += int(ln)
			switch fieldNum {
			case 1:
				if text := wsDecodeStringField(payload, 1); text != "" {
					return wsChunk{kind: "content", text: text}
				}
			case 3:
				p, c := wsDecodeDoneChunk(payload)
				return wsChunk{kind: "done", promptTokens: p, completionTokens: c}
			case 4:
				msg := wsDecodeStringField(payload, 1)
				if msg == "" {
					msg = "unknown windsurf error"
				}
				return wsChunk{kind: "error", message: msg}
			}
		} else if wireType == 0 {
			_, off = wsReadVarint(buf, off)
		} else if wireType == 1 {
			off += 8
		} else if wireType == 5 {
			off += 4
		} else {
			break
		}
	}
	return wsChunk{kind: "unknown"}
}

// wsRandHex returns n random bytes hex-encoded (session/cascade ids).
func wsRandHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is unrecoverable; fall back to time-based so the
		// executor still runs (id is opaque to the upstream).
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// wsOpenAIMessagesToWs converts OpenAI messages to the wire form Windsurf needs.
func wsOpenAIMessagesToWs(messages []jsontext.Value) [][]byte {
	out := [][]byte{}
	for _, m := range messages {
		var msg struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		}
		if err := json.Unmarshal(m, &msg); err != nil {
			continue
		}
		var content string
		switch c := msg.Content.(type) {
		case string:
			content = c
		case []any:
			for _, p := range c {
				if pm, ok := p.(map[string]any); ok {
					if t, ok := pm["text"].(string); ok {
						content += t
					}
				}
			}
		}
		role := msg.Role
		if role == "" {
			role = "user"
		}
		out = append(out, wsChatMessage(role, content, msg.ToolCallID))
	}
	return out
}

// ForwardWindsurf implements the Executor signature.
func ForwardWindsurf(w http.ResponseWriter, req *Request) error {
	var oreq struct {
		Model    string           `json:"model"`
		Messages []jsontext.Value `json:"messages"`
	}
	if err := json.Unmarshal(req.Body, &oreq); err != nil {
		return fmt.Errorf("parse body: %w", err)
	}

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	url := strings.TrimRight(req.Config.BaseURL, "/")
	if url == "" {
		url = wsChatURL
	}

	wsMessages := wsOpenAIMessagesToWs(oreq.Messages)
	if len(wsMessages) == 0 {
		wsMessages = [][]byte{wsChatMessage("user", "", "")}
	}
	proto := wsGetChatMessageRequest(req.APIKey, resolveWsModelID(oreq.Model), wsMessages)
	framed := wsGRPCWebFrame(proto)

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(framed))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/grpc-web+proto")
	hreq.Header.Set("Accept", "application/grpc-web+proto")
	if req.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+req.APIKey)
	}
	hreq.Header.Set("User-Agent", "windsurf/"+wsIDEVers)
	hreq.Header.Set("X-Grpc-Web", "1")

	resp, err := req.Client.Do(hreq)
	if err != nil {
		return &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":{"message":"windsurf: ` + err.Error() + `","type":"api_error"}}`)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body}
	}

	responseID := fmt.Sprintf("chatcmpl-ws-%d", time.Now().UnixMilli())
	created := time.Now().Unix()
	roleEmitted := false
	var totalText, hadError string
	var promptTokens, completionTokens int

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
		onFrame := func(flag byte, payload []byte) bool {
			if flag == 0x80 {
				if m := wsGRPCStatus(payload); m != "" {
					hadError = m
				}
				return true
			}
			if flag != 0x00 {
				return true
			}
			c := wsDecodeCompletionChunk(payload)
			switch c.kind {
			case "content":
				if c.text == "" {
					return true
				}
				if !roleEmitted {
					if err := emit(map[string]any{"id": responseID, "object": "chat.completion.chunk", "created": created, "model": oreq.Model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil}}}); err != nil {
						hadError = "windsurf stream: " + err.Error()
						return false
					}
					roleEmitted = true
				}
				if err := emit(map[string]any{"id": responseID, "object": "chat.completion.chunk", "created": created, "model": oreq.Model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": c.text}, "finish_reason": nil}}}); err != nil {
					hadError = "windsurf stream: " + err.Error()
					return false
				}
			case "done":
				promptTokens = c.promptTokens
				completionTokens = c.completionTokens
			case "error":
				hadError = c.message
			}
			return true
		}
		if err := wsReadFrames(ctx, resp.Body, onFrame); err != nil {
			_ = emit(map[string]any{"id": responseID, "object": "chat.completion.chunk", "created": created, "model": oreq.Model, "error": map[string]any{"message": "windsurf: " + err.Error(), "type": "windsurf_error"}})
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return nil
		}

		if hadError != "" {
			_ = emit(map[string]any{"error": map[string]any{"message": hadError, "type": "windsurf_error", "code": "upstream_error"}})
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return nil
		}
		finish := map[string]any{"id": responseID, "object": "chat.completion.chunk", "created": created, "model": oreq.Model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}
		if promptTokens > 0 || completionTokens > 0 {
			finish["usage"] = map[string]any{"prompt_tokens": promptTokens, "completion_tokens": completionTokens, "total_tokens": promptTokens + completionTokens}
		}
		if err := emit(finish); err != nil {
			return err
		}
		_, err = w.Write([]byte("data: [DONE]\n\n"))
		return err
	}

	// Non-streaming: aggregate text across frames into a single chat.completion.
	if err := wsReadFrames(ctx, resp.Body, func(flag byte, payload []byte) bool {
		if flag == 0x80 {
			if m := wsGRPCStatus(payload); m != "" {
				hadError = m
			}
			return true
		}
		if flag != 0x00 {
			return true
		}
		c := wsDecodeCompletionChunk(payload)
		switch c.kind {
		case "content":
			totalText += c.text
		case "done":
			promptTokens = c.promptTokens
			completionTokens = c.completionTokens
		case "error":
			hadError = c.message
		}
		return true
	}); err != nil {
		return &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: wsJSONError("windsurf: " + err.Error())}
	}
	if hadError != "" {
		return &proxy.UpstreamError{StatusCode: http.StatusBadGateway, Body: wsJSONError(hadError)}
	}
	out := map[string]any{
		"id":      responseID,
		"object":  "chat.completion",
		"created": created,
		"model":   oreq.Model,
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": totalText}, "finish_reason": "stop"}},
	}
	if promptTokens > 0 || completionTokens > 0 {
		out["usage"] = map[string]any{"prompt_tokens": promptTokens, "completion_tokens": completionTokens, "total_tokens": promptTokens + completionTokens}
	}
	b, jerr := json.Marshal(out)
	if jerr != nil {
		return jerr
	}
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(b)
	return err
}

// wsGRPCStatus parses a gRPC-web trailer frame payload; returns "" when OK.
func wsGRPCStatus(trailer []byte) string {
	s := string(trailer)
	var status, msg string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "grpc-status:"); ok {
			status = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "grpc-message:"); ok {
			msg = strings.TrimSpace(v)
		}
	}
	if status != "" && status != "0" {
		if msg != "" {
			return msg
		}
		return "gRPC status " + status
	}
	return ""
}

// wsReadFrames reads the upstream body, draining complete gRPC-web frames into
// onFrame (returning false stops reading). Handles trailer (0x80) and multi-flag
// frames accumulated across reads.
func wsReadFrames(ctx context.Context, body io.Reader, onFrame func(flag byte, payload []byte) bool) error {
	reader := bufio.NewReader(body)
	pending := []byte{}
	for {
		buf := make([]byte, wsReadSize)
		n, err := reader.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			var continueReading bool
			pending, continueReading = wsDrainFrames(pending, onFrame)
			if !continueReading {
				return nil
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	_, _ = wsDrainFrames(pending, onFrame)
	return nil
}

// wsDrainFrames pulls every complete frame from pending (5-byte header + payload),
// returning the leftover bytes and false if onFrame said stop.
func wsDrainFrames(pending []byte, onFrame func(flag byte, payload []byte) bool) ([]byte, bool) {
	off := 0
	for off+5 <= len(pending) {
		flag := pending[off]
		ln := int(pending[off+1])<<24 | int(pending[off+2])<<16 | int(pending[off+3])<<8 | int(pending[off+4])
		if ln < 0 || off+5+ln > len(pending) {
			break
		}
		if !onFrame(flag, pending[off+5:off+5+ln]) {
			return pending[off:], false
		}
		off += 5 + ln
	}
	return pending[off:], true
}

func wsJSONError(message string) []byte {
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "windsurf_error", "code": ""},
	})
	return b
}
