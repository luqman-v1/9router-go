package executor

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/proxy"
)

// ForwardCodebuddyCNWithTelemetry is the codebuddy-cn executor entry that wraps
// the chat call with Tencent anti-ban telemetry. codebuddy-cn and codebuddy-intl
// share ForwardCodebuddyCN; only the CN provider gets the Tencent telemetry (the
// intl lane is WorkBuddy, not Tencent, and needs no anti-ban replication).
//
// The telemetry is fire-and-forget and fail-open: it runs in detached
// goroutines with a hard 5s timeout per send, never adds latency to the chat
// request, and never changes its outcome. This is the direct Go port of
// upstream `CodeBuddyExecutor.execute()` in PR decolua/9router #3257.
func ForwardCodebuddyCNWithTelemetry(w http.ResponseWriter, req *Request) error {
	if req == nil || req.Provider != "codebuddy-cn" {
		return ForwardCodebuddyCN(w, req)
	}

	cred := codebuddyCredentialFromRequest(req)
	start := time.Now()
	model := requestModelFromBody(req.Body)
	histCount := messagesCountFromBody(req.Body)
	inputLength := len(req.Body)

	pctx := codebuddyPreChatContext{
		RequestID:      firstBodyID(req.Body, "requestId"),
		ConversationID: firstBodyID(req.Body, "conversationId"),
		MessageID:      firstBodyID(req.Body, "messageId"),
		Model:          model,
		ModelID:        model,
		Mode:           "craft",
		InputLength:    inputLength,
		HistoryCount:   histCount,
	}
	// Fire lifecycle + pre-chat events on a detached goroutine so the request
	// is never held up by telemetry. context.Background() so a client that
	// disconnects mid-request cannot cancel the sends; each send is bounded by
	// its own 5s timeout inside the telemetry helpers.
	go func() {
		codebuddySendSessionLifecycle(context.Background(), cred)
		codebuddySendPreChatEvents(context.Background(), cred, pctx)
	}()

	err := ForwardCodebuddyCN(w, req)

	// Post-chat telemetry (response received or error) — fire-and-forget.
	isSuccessful := err == nil
	elapsed := time.Since(start)
	go func() {
		codebuddySendPostChatEvents(context.Background(), cred, codebuddyPostChatContext{
			Model:            model,
			ModelID:          model,
			RequestID:        pctx.RequestID,
			ConversationID:   pctx.ConversationID,
			IsSuccessful:     isSuccessful,
			MessageErrorCode: codebuddyErrorCode(err),
			DurationMs:       elapsed.Milliseconds(),
		})
		codebuddySendGalileoTrace(context.Background(), cred, elapsed.Milliseconds())
	}()
	return err
}

// codebuddyCredentialFromRequest derives the telemetry credential material from
// an executor request. req.APIKey is the access token / API key of the
// connection; ConnectionID and ConnData carry the rest.
func codebuddyCredentialFromRequest(req *Request) codebuddyCredential {
	c := codebuddyCredential{
		APIKey:      req.APIKey,
		AccessToken: req.APIKey,
		ID:          req.ConnectionID,
	}
	if req.ConnData != nil {
		if uid, ok := req.ConnData["uid"].(string); ok && uid != "" {
			c.UID = uid
		}
	}
	return c
}

// requestModelFromBody extracts the "model" field of a request body, or "".
func requestModelFromBody(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	if len(body) == 0 {
		return ""
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	return strings.TrimSpace(m.Model)
}

// messagesCountFromBody counts the "messages" array length, or 0 when absent.
func messagesCountFromBody(body []byte) int {
	var m struct {
		Messages []any `json:"messages"`
	}
	if len(body) == 0 {
		return 0
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return 0
	}
	return len(m.Messages)
}

// firstBodyID reads a string field from a request body, used as the telemetry
// correlation fallback when the headers carry no explicit value.
func firstBodyID(body []byte, key string) string {
	if len(body) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

// codebuddyErrorCode maps a forwarded error to a per-message error code string
// suitable for telemetry, mirroring upstream's `execError?.status || "error"`.
func codebuddyErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var ue *proxy.UpstreamError
	if errors.As(err, &ue) && ue.StatusCode != 0 {
		return fmt.Sprintf("%d", ue.StatusCode)
	}
	return "error"
}
