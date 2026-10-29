package executor

import (
	"bytes"
	crand "crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"

	"github.com/google/uuid"
)

const codebuddySystemPrompt = "You are CodeBuddy Code."

var channelMarkers = []string{
	"You are Claude Code, Anthropic's official CLI for Claude.",
	"x-anthropic-billing-header",
	"Main branch (you will usually use this for PRs):",
}

var channelMarkerRegex = regexp.MustCompile(`(?i)you are claude code|claude.?code.+official.+cli|anthropic.+official.+cli|anxthxropic.+official.+cli|you are (?:cursor|windsurf|cline|aider|continue|copilot|cody)|cc_entrypoint\s*=\s*(?:cli|vscode|jetbrains|gui)|claude.?code.+issues|give feedback.+claude.?code|OhMyOpenCode`)

var codebuddyRateLimitRegex = regexp.MustCompile(`(?i)超出频率限制|frequency limit|限额`)

// Identity the official WorkBuddy AI 5.8.4 desktop client — the international
// channel of CodeBuddy since the host moved off www.codebuddy.ai — puts on
// POST /v2/chat/completions. Capture-verified upstream (open-sse
// providers/registry/codebuddy-intl.js). The CLI/2.108.1 values in
// codebuddyHeaders below belong to the Tencent CN channel only.
const (
	codebuddyIntlUserAgent = "WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.161.0"
	codebuddyIntlIDEVer    = "5.8.4"
	codebuddyIntlDomain    = "www.workbuddy.ai"

	// codebuddyConversationHeader* name the family the intl gateway's channel
	// verification demands: without it every chat request is answered
	// HTTP 400 code 11128 "conversation id missing" before a model is reached.
	codebuddyConvIDHeader        = "X-Conversation-ID"
	codebuddyConvRequestIDHeader = "X-Conversation-Request-ID"
	codebuddyConvMessageIDHeader = "X-Conversation-Message-ID"
	codebuddyRequestIDHeader     = "X-Request-ID"
	codebuddyRootRequestIDHeader = "X-Root-Request-ID"
)

// codebuddyResetAtRegex reads the upstream's own reset wall clock out of a
// rate-limit message, e.g. "超出频率限制 2026-10-16 10:00:00 UTC+8".
var codebuddyResetAtRegex = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2})(?:\s*UTC\+?([0-9:]+))?`)

// codebuddyAccountBanRegex matches the wording of a risk-control block, never
// the code alone: 11140 also carries ordinary model throttling.
var codebuddyAccountBanRegex = regexp.MustCompile(`(?i)request illegal|account (?:has been )?restricted|unusual activity|账号存在异常行为|帳號存在異常行為`)

// ForwardCodebuddyCN forwards to Tencent CodeBuddy with force-stream
// and reasoning_summary injection.
//
// CodeBuddy is OpenAI-compatible but rejects non-stream chat requests
// (HTTP 400, code 11101 "Non-stream chat request is currently not supported").
// This executor forces stream=true in the request body, mirroring the
// JS version's CodeBuddyExecutor.transformRequest behaviour.
//
// It also handles reasoning_effort:
//   - "none"/"off" → stripped (CodeBuddy gateway has no "none")
//   - any other value → injects reasoning_summary="auto" so CodeBuddy
//     surfaces the model's reasoning output.
func ForwardCodebuddyCN(w http.ResponseWriter, req *Request) error {
	body, err := transformCodebuddyBody(req.Body)
	if err != nil {
		return fmt.Errorf("transform codebuddy body: %w", err)
	}

	ctx := req.Ctx
	cfg := req.Config
	intl := isCodebuddyIntlRequest(req, cfg)
	if cfg != nil {
		merged := make(map[string]string, len(cfg.StaticHeaders)+25)
		for k, v := range cfg.StaticHeaders {
			merged[k] = v
		}
		delete(merged, "x-requested-with")
		delete(merged, "x-codebuddy-request")

		if intl {
			// Fail closed: a request without the family is refused by the
			// gateway every time, so sending one anyway would be a 100%
			// failure with no cause in the log.
			if err := codebuddyIntlHeaders(merged, req, cfg); err != nil {
				return fmt.Errorf("codebuddy channel verification: %w", err)
			}
		} else {
			for k, v := range codebuddyHeaders(req, cfg, body) {
				merged[k] = v
			}
		}
		cfgCopy := *cfg
		cfgCopy.StaticHeaders = merged
		cfg = &cfgCopy
	}

	// Always forward as stream — CodeBuddy rejects stream=false
	resp, err := proxy.ForwardOpenAI(ctx, req.Client, cfg, req.APIKey, body, true)
	if err != nil {
		var ue *proxy.UpstreamError
		if errors.As(err, &ue) {
			applyCodebuddyFailure(ue, intl)
			// fmt.Errorf snapshots the message of what it wraps, and the inner
			// wrapper was built before this rewrite — re-wrapping the stale
			// error would keep reporting the raw gateway JSON in every log
			// line (a 11140 ban then reads as an opaque blob even though the
			// canonical wording decided the cooldown). Wrap the rewritten
			// error instead; errors.As still finds it for classification.
			return fmt.Errorf("ForwardCodebuddyCN upstream (%s): %w", cfgBaseURL(cfg), ue)
		}
		return fmt.Errorf("ForwardCodebuddyCN upstream: %w", err)
	}
	defer resp.Body.Close()

	forwardUpstreamResponseHeaders(w, resp.Header)

	if req.IsStream {
		if !isEventStreamResponse(resp.Header) {
			data, rerr := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
			if rerr != nil {
				return fmt.Errorf("read codebuddy upstream body: %w", rerr)
			}
			if !proxy.LooksLikeSSE(data) {
				if ferr := codebuddyFailureFrom(data, intl); ferr != nil {
					return ferr
				}
				return jsonResponse(req.Ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
			}
			resp.Body = io.NopCloser(bytes.NewReader(data))
		}
		stallReader := proxy.NewStallReaderWithContext(req.Ctx, resp.Body, 0, "codebuddy-cn")
		defer stallReader.Close() // stops the shutdown watcher + stall timer
		return execSSEStream(w, stallReader, req)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return fmt.Errorf("read codebuddy response: %w", err)
	}
	if converted, ok := sseToOpenAIJSON(data); ok {
		data = converted
	} else if ferr := codebuddyFailureFrom(data, intl); ferr != nil {
		return ferr
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
}

func sanitizeChannelMarkers(text string) string {
	if text == "" {
		return text
	}
	result := text
	for _, marker := range channelMarkers {
		if strings.Contains(result, marker) {
			result = strings.ReplaceAll(result, marker, "[external-client-identity]")
		}
	}
	return channelMarkerRegex.ReplaceAllString(result, "[external-client-identity]")
}

// codebuddyChannelGatePhrases are sentence-level rewrites of prompt text the
// gateway rejects on content alone: a system or assistant sentence that matches
// one of these families deterministically answers HTTP 400 code 11128 ("Illegal
// API invocation from an unapproved channel") before a model is reached. Each
// pair keeps the meaning and breaks the fingerprint — the gateway matches
// verbatim, and it normalises invisible characters first, so scattering
// zero-width spaces (an earlier mitigation) does not survive.
//
// The list is the sample set measured by CARVIN94/dsh-router-codebuddy
// (src/body.ts CHANNEL_GATE_PHRASES, 0.3.24): their live A/B on one account with
// max_tokens=4 returned 11128 for the @-path paragraph and 200 for the rewritten
// form. It is a sample, not a schema — the real list lives upstream and will
// grow, so a miss only means the phrase was not in the table.
//
// Only system and assistant text is rewritten. The user's own words are never
// touched: the gateway does not fingerprint the user role, and rewriting it
// would be tampering with the request.
var codebuddyChannelGatePhrases = [...][2]string{
	{"You are an AI agent powered by DeepSeek Harness", "You are an AI agent running inside DeepSeek Harness"},
	{"You are a coding agent powered by the ", "You are a coding agent running on the "},
	{"You are interacting with the user through the DeepSeek Harness Web GUI", "You are working with the user inside the DeepSeek Harness Web GUI"},
	{"Agent Teams is available in this session", "Agent Teams is enabled for this session"},
	{"Tokens prefixed with @ are paths the user explicitly referenced.", "Paths that begin with @ are the ones the user explicitly referenced."},
	{"Relative paths resolve from the workspace root", "Relative paths are resolved against the workspace root"},
	{"absolute paths identify files or directories on the host.", "absolute paths point at files or directories on the host."},
	{"A trailing slash marks a directory:", "A trailing slash indicates a directory:"},
	{"list it when its contents matter.", "list it whenever its contents matter."},
	{"Anything else is a file:", "Everything else is a file:"},
	{"use the read tool when its contents are needed,", "use the read tool whenever you need its contents,"},
	{"and do not claim to have inspected it before reading.", "and never claim to have inspected it before reading it."},
	{`@"..." quotes a path containing spaces.`, `A path containing spaces is quoted with @"...".`},
}

// scrubCodebuddyChannelPhrases rewrites the gate sentences in one text and
// returns it unchanged when nothing matched. Every replacement is disjoint from
// every needle, so a second pass is a no-op.
func scrubCodebuddyChannelPhrases(text string) string {
	if text == "" {
		return text
	}
	out := text
	for _, pair := range codebuddyChannelGatePhrases {
		if strings.Contains(out, pair[0]) {
			out = strings.ReplaceAll(out, pair[0], pair[1])
		}
	}
	return out
}

func extractMessageText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, b := range v {
			if s, ok := b.(string); ok {
				parts = append(parts, s)
			} else if bm, ok := b.(map[string]any); ok {
				if t, ok := bm["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func copyMap(m map[string]any) map[string]any {
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// transformCodebuddyBody forces stream=true, normalizes system prompt and caller
// instructions, sanitizes external client markers, and handles reasoning params.
func transformCodebuddyBody(body []byte) ([]byte, error) {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}

	// Force stream — CodeBuddy rejects non-stream (HTTP 400, code 11101)
	reqMap["stream"] = true

	// Handle reasoning_effort / reasoning_summary
	if eff, ok := reqMap["reasoning_effort"].(string); ok {
		switch eff {
		case "none", "off":
			delete(reqMap, "reasoning_effort")
		default:
			reqMap["reasoning_summary"] = "auto"
		}
	}

	var inputMessages []any
	if arr, ok := reqMap["messages"].([]any); ok {
		inputMessages = arr
	}

	systemParts := []string{codebuddySystemPrompt}
	var otherMessages []any

	for _, raw := range inputMessages {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "system" || role == "developer" {
			rawText := extractMessageText(m["content"])
			cleaned := strings.TrimSpace(scrubCodebuddyChannelPhrases(sanitizeChannelMarkers(rawText)))
			if cleaned != "" && cleaned != codebuddySystemPrompt {
				systemParts = append(systemParts, cleaned)
			}
			continue
		}
		if role == "user" {
			if s, ok := m["content"].(string); ok {
				cp := copyMap(m)
				cp["content"] = []any{map[string]any{"type": "text", "text": s}}
				otherMessages = append(otherMessages, cp)
				continue
			}
		}
		if role == "assistant" {
			cp := copyMap(m)
			switch v := cp["content"].(type) {
			case string:
				cp["content"] = scrubCodebuddyChannelPhrases(sanitizeChannelMarkers(v))
			case []any:
				var newBlocks []any
				for _, b := range v {
					if bm, ok := b.(map[string]any); ok {
						if t, ok := bm["text"].(string); ok {
							cpBlock := copyMap(bm)
							cpBlock["text"] = scrubCodebuddyChannelPhrases(sanitizeChannelMarkers(t))
							newBlocks = append(newBlocks, cpBlock)
							continue
						}
					}
					newBlocks = append(newBlocks, b)
				}
				cp["content"] = newBlocks
			}
			otherMessages = append(otherMessages, cp)
			continue
		}
		otherMessages = append(otherMessages, m)
	}

	leadingSystem := map[string]any{
		"role":    "system",
		"content": strings.Join(systemParts, "\n\n"),
	}
	messages := make([]any, 0, len(otherMessages)+1)
	messages = append(messages, leadingSystem)
	messages = append(messages, otherMessages...)
	reqMap["messages"] = messages

	return json.Marshal(reqMap)
}

// cfgBaseURL returns the request target for log lines without panicking on a
// nil config.
func cfgBaseURL(cfg *providers.ProviderConfig) string {
	if cfg == nil {
		return "<unconfigured>"
	}
	return cfg.BaseURL
}

// codebuddyDomain picks X-Domain for the active channel. A connection-supplied
// override wins; otherwise the host is derived from the base URL.
func codebuddyDomain(req *Request, cfg *providers.ProviderConfig, intl bool) string {
	if req != nil && req.ConnData != nil {
		if d, ok := req.ConnData["domain"].(string); ok && strings.TrimSpace(d) != "" {
			return d
		}
	}
	if intl {
		return codebuddyIntlDomain
	}
	if cfg != nil && (strings.Contains(cfg.BaseURL, "tencent.com") || strings.Contains(cfg.BaseURL, "copilot.")) {
		return "copilot.tencent.com"
	}
	return "www.codebuddy.ai"
}

// codebuddyIdentityHeaders is the CN CLI's non-id half: everything the Tencent
// channel expects on a chat request except the per-request id family.
func codebuddyIdentityHeaders(req *Request, cfg *providers.ProviderConfig) map[string]string {
	return map[string]string{
		"Content-Type":                "application/json; charset=utf-8",
		"User-Agent":                  "CLI/2.108.1 CodeBuddy/2.108.1",
		"X-Product":                   "SaaS",
		"X-App":                       "cli",
		"X-IDE-Type":                  "CLI",
		"X-IDE-Name":                  "CLI",
		"X-IDE-Version":               "2.108.1",
		"X-Private-Data":              "false",
		"X-Requested-With":            "XMLHttpRequest",
		"X-CodeBuddy-Request":         "1",
		"X-Domain":                    codebuddyDomain(req, cfg, false),
		"X-Stainless-Runtime":         "node",
		"X-Stainless-Runtime-Version": "v24.11.1",
		"X-Stainless-Lang":            "js",
		"X-Stainless-Arch":            "x64",
		"X-Stainless-Os":              "Windows",
		"X-Stainless-Package-Version": "6.25.0",
		"X-Stainless-Helper-Method":   "stream",
		"X-Stainless-Retry-Count":     "0",
		"X-Agent-Intent":              "craft",
		"X-Agent-Purpose":             "conversation",
	}
}

// codebuddyIntlIdentityHeaders is the WorkBuddy 5.8.4 desktop client's non-id
// half. The intl client is not a Stainless/OpenAI-SDK CLI, so it sends no
// Stainless headers and marks private data as present.
func codebuddyIntlIdentityHeaders(req *Request, cfg *providers.ProviderConfig) map[string]string {
	h := map[string]string{
		"Content-Type":        "application/json; charset=utf-8",
		"User-Agent":          codebuddyIntlUserAgent,
		"X-Product":           "SaaS",
		"X-Product-Version":   codebuddyIntlIDEVer,
		"X-IDE-Type":          "WorkBuddy",
		"X-IDE-Name":          "WorkBuddy",
		"X-IDE-Version":       codebuddyIntlIDEVer,
		"X-Private-Data":      "true",
		"X-Requested-With":    "XMLHttpRequest",
		"X-CodeBuddy-Request": "1",
		"X-Domain":            codebuddyDomain(req, cfg, true),
		"X-Agent-Intent":      "craft",
		"X-Agent-Purpose":     "conversation",
		"X-Agent-Type":        "main",
	}
	// The desktop client names the model it is asking for beside the body field.
	if model := codebuddyRequestModel(req); model != "" {
		h["X-Model-ID"] = model
	}
	// X-User-Id is the signed-in account. The credential is the access token and
	// its `sub` claim is exactly that id; anything that is not a parseable JWT
	// omits the header instead of sending an empty one — an empty identity header
	// is a mismatch the gateway can see, which is worse than its absence.
	if sub := codebuddyTokenSubject(req); sub != "" {
		h["X-User-Id"] = sub
	}
	return h
}

// codebuddyRequestModel reads the model a request name for the X-Model-ID
// header, tolerating a nil request (tests build headers from a bare body).
func codebuddyRequestModel(req *Request) string {
	if req == nil {
		return ""
	}
	return strings.TrimSpace(req.ModelName)
}

// codebuddyTokenSubject extracts the `sub` claim from a bearer access token.
// Returns "" for anything that is not a two-dot JWT with a string sub, so a
// plain API key never produces a half-filled identity header.
func codebuddyTokenSubject(req *Request) string {
	if req == nil {
		return ""
	}
	token := strings.TrimSpace(req.APIKey)
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Sub)
}

// codebuddyIntlHeaders fills bag with the intl channel's headers: the WorkBuddy
// identity plus the conversation and trace family. Values already in bag (the
// connection's static headers) are kept, so an operator-fixed conversation id
// survives.
//
// The official CLI (dist/codebuddy-lite-wb.mjs, 5.8.4) sets
// X-Conversation-ID from the session id, so one conversation keeps one id
// across turns. Mirror that when the caller named a session — it preserves
// prompt-cache affinity without the cross-session collision a prompt hash
// produced — and fall back to a fresh id when there is no session. An explicit
// caller-supplied id always wins over both.
func codebuddyIntlHeaders(bag map[string]string, req *Request, cfg *providers.ProviderConfig) error {
	if bag == nil {
		return errors.New("codebuddy intl headers: nil header bag")
	}
	for k, v := range codebuddyIntlIdentityHeaders(req, cfg) {
		setHeaderFold(bag, k, v)
	}
	if key := codebuddySessionKey(req); key != "" && headerFold(bag, codebuddyConvIDHeader) == "" {
		setHeaderFold(bag, codebuddyConvIDHeader, stableConversationUUID("codebuddy-intl:"+key))
	}
	return applyCodebuddyConversationHeaders(bag)
}

// codebuddyHeaders is the CN channel's full header set. Its conversation id is
// derived from the session so retries and account failovers of one turn stay on
// one conversation; the intl channel must not do this (see
// applyCodebuddyConversationHeaders).
func codebuddyHeaders(req *Request, cfg *providers.ProviderConfig, body []byte) map[string]string {
	h := codebuddyIdentityHeaders(req, cfg)
	reqID := strings.ReplaceAll(uuid.NewString(), "-", "")
	h[codebuddyRequestIDHeader] = reqID
	h[codebuddyConvIDHeader] = codebuddySessionConversationID(req, body)
	h[codebuddyConvRequestIDHeader] = strings.ReplaceAll(uuid.NewString(), "-", "")
	h[codebuddyConvMessageIDHeader] = reqID
	return h
}

// codebuddySessionKey returns the client session key for this request, or "".
// It never falls back to prompt text: a hash of the opening message makes two
// unrelated sessions that happen to start alike share one conversation id,
// which is exactly the anomaly the intl gateway can see.
func codebuddySessionKey(req *Request) string {
	if req == nil {
		return ""
	}
	if req.SessionID != "" {
		return req.SessionID
	}
	if req.ConnData != nil {
		if m, ok := req.ConnData["sessionId"].(string); ok {
			return m
		}
	}
	return ""
}

// codebuddySessionConversationID derives the CN conversation id from the
// session key, falling back to the opening user message and then a random UUID.
func codebuddySessionConversationID(req *Request, body []byte) string {
	sessionKey := codebuddySessionKey(req)
	if sessionKey == "" {
		sessionKey = firstUserText(body)
	}
	if sessionKey != "" {
		return stableConversationUUID("codebuddy:" + sessionKey)
	}
	return uuid.NewString()
}

func codebuddyConversationHeaders(req *Request, body []byte) map[string]string {
	return codebuddyHeaders(req, nil, body)
}

// isCodebuddyIntlRequest picks the channel. The provider id wins because an
// edge relay rewrites cfg.BaseURL to the relay URL and keeps the real target
// only in x-relay-target (internal/proxy/transport.go BuildEdgeRelayHeaders), so
// a relayed intl connection would otherwise be routed as CN — losing the
// WorkBuddy identity and the header family the gateway demands. The relay target
// and the base URL are fallbacks for configs that carry no provider id (tests,
// custom entries).
func isCodebuddyIntlRequest(req *Request, cfg *providers.ProviderConfig) bool {
	if req != nil && req.Provider != "" {
		return req.Provider == "codebuddy-intl" || req.Provider == "cbai"
	}
	if cfg == nil {
		return false
	}
	if target := headerFold(cfg.StaticHeaders, "x-relay-target"); target != "" && isCodebuddyIntlBaseURL(target) {
		return true
	}
	return isCodebuddyIntlBaseURL(cfg.BaseURL)
}

// isCodebuddyIntlBaseURL reports whether the connection talks to the
// international gateway. The intl host moved from www.codebuddy.ai to
// www.workbuddy.ai; a custom base URL may still name either.
func isCodebuddyIntlBaseURL(baseURL string) bool {
	b := strings.ToLower(baseURL)
	return strings.Contains(b, "codebuddy.ai") || strings.Contains(b, "workbuddy.ai")
}

// firstNonEmpty returns the first value with non-blank content.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// findHeaderFold locates a header regardless of case. HTTP header names are
// case-insensitive, so testing the exact key would miss a caller's lowercase
// one, add a second key, and let the transport join both into one
// comma-separated value — corrupting the id we were supposed to preserve.
func findHeaderFold(h map[string]string, name string) (string, string, bool) {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return k, v, true
		}
	}
	return "", "", false
}

func headerFold(h map[string]string, name string) string {
	if _, v, ok := findHeaderFold(h, name); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// setHeaderFold writes value under the caller's existing key when only the case
// differs, so the bag never carries two spellings of one header.
func setHeaderFold(h map[string]string, name, value string) {
	if k, _, ok := findHeaderFold(h, name); ok {
		h[k] = value
		return
	}
	h[name] = value
}

// freshUUID7 returns a dashed RFC 9562 UUIDv7 — the shape the official intl
// client sends as X-Conversation-ID.
func freshUUID7() (string, error) {
	u, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// freshHex32 is the undashed form the client uses for the request and message
// ids.
func freshHex32() (string, error) {
	s, err := freshUUID7()
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(s, "-", ""), nil
}

// applyCodebuddyConversationHeaders attaches the header family the intl
// gateway's channel verification demands (HTTP 400 code 11128 "conversation id
// missing" without it), mirroring the WorkBuddy AI 5.8.4 client:
//
//	X-Conversation-ID          dashed UUIDv7
//	X-Conversation-Request-ID  32 hex   (mirrored by X-Root-Request-ID)
//	X-Conversation-Message-ID  32 hex   (mirrored by X-Request-ID)
//	X-Trace-ID / X-B3-* / traceparent / b3 — one trace id per request
//
// This function fills only what is missing, so whoever calls it owns the
// conversation id's scope. codebuddyIntlHeaders pre-seeds it from a named
// session — the CLI (codebuddy-lite-wb 5.8.4) keeps one id across a
// conversation, and prompt-cache affinity is worth keeping — and leaves it to
// this function when the caller names no session, which yields a fresh id per
// request, the shape the 5.8.4 desktop client sends. What must never happen is
// deriving it from prompt text: the opening user message is not a session, so
// two unrelated conversations that start alike would share one id, an anomaly
// the gateway can see. Non-empty values already present are preserved so an
// operator-fixed or caller-supplied id wins; blank counts as absent. The
// function is idempotent.
//
// Errors are returned, never swallowed: a request without the family is refused
// by the gateway every time, so a silent fall-through would be a 100% failure
// with no cause in the log.
func applyCodebuddyConversationHeaders(h map[string]string) error {
	if headerFold(h, codebuddyConvIDHeader) == "" {
		v, err := freshUUID7()
		if err != nil {
			return fmt.Errorf("generate %s: %w", codebuddyConvIDHeader, err)
		}
		setHeaderFold(h, codebuddyConvIDHeader, v)
	}

	convReq := headerFold(h, codebuddyConvRequestIDHeader)
	if convReq == "" {
		v, err := freshHex32()
		if err != nil {
			return fmt.Errorf("generate %s: %w", codebuddyConvRequestIDHeader, err)
		}
		setHeaderFold(h, codebuddyConvRequestIDHeader, v)
		convReq = v
	}
	if headerFold(h, codebuddyRootRequestIDHeader) == "" {
		setHeaderFold(h, codebuddyRootRequestIDHeader, convReq)
	}

	// The request id mirrors the message id: reuse whichever side exists.
	msgID := headerFold(h, codebuddyConvMessageIDHeader)
	if msgID == "" {
		msgID = headerFold(h, codebuddyRequestIDHeader)
	}
	if msgID == "" {
		v, err := freshHex32()
		if err != nil {
			return fmt.Errorf("generate %s: %w", codebuddyConvMessageIDHeader, err)
		}
		msgID = v
	}
	// Only fill the gaps: a caller that already set both keeps both, even when
	// they disagree (the client sends them equal, but it is the caller's call).
	if headerFold(h, codebuddyConvMessageIDHeader) == "" {
		setHeaderFold(h, codebuddyConvMessageIDHeader, msgID)
	}
	if headerFold(h, codebuddyRequestIDHeader) == "" {
		setHeaderFold(h, codebuddyRequestIDHeader, msgID)
	}

	// Trace family — W3C traceparent and B3, all derived from one trace id.
	trace := headerFold(h, "X-Trace-ID")
	if trace == "" {
		trace = headerFold(h, "X-B3-TraceId")
	}
	if trace == "" {
		v, err := freshHex32()
		if err != nil {
			return fmt.Errorf("generate trace id: %w", err)
		}
		trace = v
	}
	span := headerFold(h, "X-B3-SpanId")
	if span == "" {
		b := make([]byte, 8)
		if _, err := crand.Read(b); err != nil {
			return fmt.Errorf("generate span id: %w", err)
		}
		span = hex.EncodeToString(b)
	}
	setHeaderFold(h, "X-Trace-ID", trace)
	setHeaderFold(h, "X-B3-TraceId", trace)
	setHeaderFold(h, "X-B3-SpanId", span)
	if headerFold(h, "traceparent") == "" {
		setHeaderFold(h, "traceparent", "00-"+trace+"-"+span+"-01")
	}
	if headerFold(h, "b3") == "" {
		setHeaderFold(h, "b3", trace+"-"+span+"-1")
	}
	if headerFold(h, "X-B3-Sampled") == "" {
		setHeaderFold(h, "X-B3-Sampled", "1")
	}
	return nil
}

// codebuddyFailure is the executor's verdict on an upstream error body.
type codebuddyFailure struct {
	status int
	// message is the canonical wording. It is what the cooldown rules match on
	// (providers.ErrorRules), so a ban must say "account restricted upstream"
	// and a rate limit must say "rate limit".
	message string
	// retryAfter is the wait the upstream named for itself, when it named one.
	retryAfter time.Duration
}

// classifyCodebuddyFailure reads a CodeBuddy gateway error body.
//
// Code 11140 is overloaded: it carries both an account-level risk-control ban
// ("request illegal" + a restricted display message) and ordinary model
// throttling. Classifying on the code alone turns normal throttling into a 2h
// account lock, so the wording decides and the ambiguous remainder stays 429.
// Code 11128 is overloaded too: it also reports "first message is not system
// prompt", a body-shape problem this executor already fixes, so only the
// conversation-id variant is channel verification.
func classifyCodebuddyFailure(body []byte, intl bool) (codebuddyFailure, bool) {
	if len(body) == 0 {
		return codebuddyFailure{}, false
	}
	var data struct {
		Code        int               `json:"code"`
		Msg         string            `json:"msg"`
		Message     string            `json:"message"`
		RequestID   string            `json:"requestId"`
		DisplayMsg  map[string]string `json:"displayMsg"`
		DisplayTips map[string]string `json:"displayTips"`
		Error       struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return codebuddyFailure{}, false
	}
	msg := data.Msg
	if msg == "" {
		msg = data.Message
	}
	if msg == "" {
		msg = data.Error.Message
	}
	display := data.DisplayMsg["en"]
	if display == "" {
		display = data.DisplayMsg["zh-hans"]
	}

	if data.Code == 6004 || codebuddyRateLimitRegex.MatchString(msg) {
		m := msg
		if m == "" {
			m = "CodeBuddy frequency limit (6004)"
		}
		return codebuddyFailure{
			status:     http.StatusTooManyRequests,
			message:    "CodeBuddy rate limit: " + m,
			retryAfter: codebuddyRetryAfter(msg),
		}, true
	}
	if !intl {
		return codebuddyFailure{}, false
	}
	if data.Code == 11140 {
		if codebuddyAccountBanRegex.MatchString(msg + " " + display) {
			out := `CodeBuddy Intl account restricted upstream (11140 "request illegal")`
			if detail := firstNonEmpty(display, msg); detail != "" {
				out += ": " + detail
			}
			out += ". This is a risk-control block on the account, not the request: re-sign in or appeal in the official WorkBuddy app. It does not clear on a short cooldown."
			if data.RequestID != "" {
				out += " requestId=" + data.RequestID
			}
			return codebuddyFailure{status: http.StatusForbidden, message: out}, true
		}
		m := firstNonEmpty(msg, display, "CodeBuddy rate limit (11140)")
		return codebuddyFailure{
			status:     http.StatusTooManyRequests,
			message:    "CodeBuddy rate limit: " + m,
			retryAfter: codebuddyRetryAfter(msg),
		}, true
	}
	if data.Code == 11128 {
		if gate := codebuddyGateKind(msg + " " + data.DisplayTips["en"]); gate != codebuddyGateNone {
			return codebuddyFailure{
				status:  http.StatusBadRequest,
				message: codebuddyGateMessage(gate, firstNonEmpty(data.DisplayTips["en"], display)),
			}, true
		}
	}
	// Last resort for the shape the gateway actually sent: its error object
	// quoted inside `message`, so the parsed Code is 0 and only the text says
	// what happened.
	if gate := codebuddyRawChannelGate(body); gate != codebuddyGateNone {
		return codebuddyFailure{
			status:  http.StatusBadRequest,
			message: codebuddyGateMessage(gate, firstNonEmpty(display, msg)),
		}, true
	}
	return codebuddyFailure{}, false
}

// codebuddyChannelGate names which channel-verification gate a failure reports.
// Two distinct diagnoses hide behind one gate: a missing header family (fixable
// here) and a client-fingerprint rejection (the identity headers disagree with
// the client the channel currently serves).
type codebuddyChannelGate int

const (
	codebuddyGateNone codebuddyChannelGate = iota
	codebuddyGateConversationID
	codebuddyGateMissingFamily
	codebuddyGateClientIdentity
)

// codebuddyBlockedMarkers is the third recorded shape: 11128 with "request
// illegal" and "Request blocked. Please send it again". A/B against the live
// gateway showed it is the missing-family verdict — the same credential, body
// and host answered 200 once the X-Conversation-* and trace headers were added,
// so it is not an account or fingerprint verdict.
var codebuddyBlockedMarkers = []string{"request illegal", "request blocked"}

// codebuddyChannelMarkers are the phrasings the gateway uses for the
// fingerprint rejection. Recorded from a real 11128 body
// ("Illegal API invocation from an unapproved channel" + "This call failed
// channel verification, possibly because it was not sent from an official
// client"); the bare "channel verification" keeps picking it up when only the
// displayTips survive translation.
var codebuddyChannelMarkers = []string{
	"illegal api invocation from an unapproved channel",
	"unapproved channel",
	"channel verification",
	"not sent from an official client",
	"渠道校验",
}

// codebuddyGateCodeRegex finds the business code in a body that carries the
// upstream error as a quoted string — "{'code': 11128, ...}", single quotes and
// escaped quotes included — rather than as JSON. The word boundary sits right
// after the digits, before any closing quote, so 111280 is rejected while
// "11-128" with a quoted value still matches.
var codebuddyGateCodeRegex = regexp.MustCompile(`(?i)['"]?code\\*['"]?\s*:\s*\\*['"]?(?:11128|11-128)\b`)

// codebuddyGateKind reads the diagnosis out of the gate's own wording. The code
// alone never decides: 11128 also carries "first message is not system prompt".
func codebuddyGateKind(text string) codebuddyChannelGate {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "conversation id missing") {
		return codebuddyGateConversationID
	}
	for _, marker := range codebuddyBlockedMarkers {
		if strings.Contains(lower, marker) {
			return codebuddyGateMissingFamily
		}
	}
	for _, marker := range codebuddyChannelMarkers {
		if strings.Contains(lower, marker) {
			return codebuddyGateClientIdentity
		}
	}
	return codebuddyGateNone
}

// codebuddyRawChannelGate detects the gate in a body the JSON parser could not
// attribute to a code. Both halves are required — the code and the wording — so
// a request id or an unrelated refusal that happens to contain "11128" is not
// reported as a channel block.
func codebuddyRawChannelGate(body []byte) codebuddyChannelGate {
	if !codebuddyGateCodeRegex.Match(body) {
		return codebuddyGateNone
	}
	return codebuddyGateKind(string(body))
}

// codebuddyGateMessage turns a gate into an operator-facing cause.
func codebuddyGateMessage(gate codebuddyChannelGate, detail string) string {
	var out string
	switch gate {
	case codebuddyGateConversationID:
		out = `CodeBuddy channel verification failed (11128 "conversation id missing"): the upstream gateway requires the X-Conversation-* header family the official IDE sends, supplied by applyCodebuddyConversationHeaders(). If this appears, the gate likely changed (e.g. a new required header).`
	case codebuddyGateMissingFamily:
		out = `CodeBuddy channel verification failed (11128 "request illegal" / "Request blocked"): the request reached the gateway without the X-Conversation-* and trace headers the official client sends. Measured: the same credential, host and body answer 200 once the family is attached, so treat a missing family first and only then the identity headers (User-Agent, X-IDE-Type/Name/Version).`
	default:
		out = `CodeBuddy channel verification failed ("illegal api invocation from an unapproved channel"): the gateway rejected the client fingerprint, not the request — it does not accept this caller as the official client. Re-check the intl identity headers (User-Agent, X-IDE-Type/Name/Version, X-Private-Data) against the client version the channel serves now.`
	}
	if detail != "" {
		out += " Upstream said: " + detail
	}
	return out
}

// codebuddyRetryAfter reads the upstream's own reset wall clock out of a
// rate-limit message ("... 2026-10-16 10:00:00 UTC+8"). Absent or malformed
// returns 0 — the caller then falls back to its normal cooldown, never a
// guessed timestamp.
func codebuddyRetryAfter(msg string) time.Duration {
	m := codebuddyResetAtRegex.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	zone := "+08:00"
	if z := m[3]; z != "" {
		switch {
		case strings.Contains(z, ":"):
			if !strings.HasPrefix(z, "+") && !strings.HasPrefix(z, "-") {
				z = "+" + z
			}
		case len(z) == 1:
			z = "+0" + z + ":00"
		case len(z) == 2:
			z = "+" + z + ":00"
		case len(z) == 4:
			z = "+" + z[:2] + ":" + z[2:]
		}
		zone = z
	}
	at, err := time.Parse("2006-01-02 15:04:05Z07:00", m[1]+" "+m[2]+zone)
	if err != nil {
		return 0
	}
	d := time.Until(at)
	if d <= 0 {
		return 0
	}
	return d
}

// codebuddyFailureError renders a classified failure as an UpstreamError so the
// request path, the cooldown rules and the Retry-After forwarder all see it.
func codebuddyFailureError(f codebuddyFailure) error {
	err := proxy.UpstreamFailure(f.status, f.message)
	var ue *proxy.UpstreamError
	if errors.As(err, &ue) && f.retryAfter > 0 {
		if ue.Header == nil {
			ue.Header = http.Header{}
		}
		ue.Header.Set("Retry-After", strconv.Itoa(int(math.Ceil(f.retryAfter.Seconds()))))
	}
	return err
}

// codebuddyFailureFrom classifies a 2xx response that is actually an error body.
// Returns nil when the body is not a recognised failure and should pass through.
func codebuddyFailureFrom(body []byte, intl bool) error {
	f, ok := classifyCodebuddyFailure(body, intl)
	if !ok {
		return nil
	}
	return codebuddyFailureError(f)
}

// applyCodebuddyFailure rewrites a non-2xx UpstreamError in place: the status
// the classifier decided, a canonical message the rules can match, and the
// upstream's announced reset as Retry-After. The raw gateway JSON is preserved
// under "upstream" for diagnosis, but it is never the message — it reads like a
// gateway bug rather than a cause.
func applyCodebuddyFailure(ue *proxy.UpstreamError, intl bool) {
	if ue == nil {
		return
	}
	f, ok := classifyCodebuddyFailure(ue.Body, intl)
	if !ok {
		return
	}
	ue.StatusCode = f.status
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": f.message,
			"type":    "upstream_error",
			"code":    f.status,
		},
		"upstream": string(ue.Body),
	})
	if err == nil {
		ue.Body = body
	}
	if f.retryAfter > 0 {
		if ue.Header == nil {
			ue.Header = http.Header{}
		}
		ue.Header.Set("Retry-After", strconv.Itoa(int(math.Ceil(f.retryAfter.Seconds()))))
	}
}

// stableConversationUUID derives an RFC-4122-v5-shaped UUID from key via
// SHA-1, matching the JS executor's derivation so the same session key maps
// to the same conversation id across implementations.
func stableConversationUUID(key string) string {
	sum := sha1.Sum([]byte(key))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x50 // version 5
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	u, err := uuid.FromBytes(b[:])
	if err != nil {
		return uuid.NewString()
	}
	return u.String()
}

// firstUserText extracts the opening user message text from an OpenAI-shaped
// body (string or typed blocks) to use as a conversation-key fallback.
func firstUserText(body []byte) string {
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		return ""
	}
	msgs, _ := reqMap["messages"].([]any)
	for _, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role != "user" {
			continue
		}
		switch content := m["content"].(type) {
		case string:
			return content
		case []any:
			var sb strings.Builder
			for _, blk := range content {
				if bm, ok := blk.(map[string]any); ok {
					if t, ok := bm["text"].(string); ok {
						sb.WriteString(t)
					}
				}
			}
			return sb.String()
		}
		return ""
	}
	return ""
}

// sseToOpenAIJSON re-aggregates OpenAI chat-completions SSE chunks into a
// single chat.completion JSON object. Returns (nil, false) when raw is not
// SSE-shaped (e.g. an upstream error JSON that should pass through).
func sseToOpenAIJSON(raw []byte) ([]byte, bool) {
	var chunks []map[string]any
	var streamErr map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(trimmed[5:])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if e, ok := chunk["error"]; ok {
			streamErr, _ = e.(map[string]any)
			continue
		}
		chunks = append(chunks, chunk)
	}
	if streamErr != nil {
		b, _ := json.Marshal(map[string]any{"error": streamErr})
		return b, true
	}
	if len(chunks) == 0 {
		return nil, false
	}

	var contentParts, reasoningParts []string
	toolCallIdx := map[int]map[string]any{}
	var toolCalls []map[string]any
	finishReason := "stop"
	var usage any
	var first map[string]any

	for _, chunk := range chunks {
		if first == nil {
			first = chunk
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		if c, ok := delta["content"].(string); ok && c != "" {
			contentParts = append(contentParts, c)
		}
		// Vendor aliases (`reasoning`, `reasoning_details[]`) count as
		// reasoning too: without this an alias lane's thinking is silently
		// dropped before the empty-content rescue below could keep it.
		if r := reasoningTextFromDeltaMap(delta); r != "" {
			reasoningParts = append(reasoningParts, r)
		}
		if fr, ok := choice["finish_reason"].(string); ok {
			finishReason = fr
		}
		if u, ok := chunk["usage"]; ok {
			usage = u
		}
		if tcs, ok := delta["tool_calls"].([]any); ok {
			for _, tcAny := range tcs {
				tc, _ := tcAny.(map[string]any)
				idx, _ := tc["index"].(float64)
				entry, ok := toolCallIdx[int(idx)]
				if !ok {
					entry = map[string]any{
						"id":       "",
						"type":     "function",
						"function": map[string]any{"name": "", "arguments": ""},
					}
					toolCallIdx[int(idx)] = entry
					toolCalls = append(toolCalls, entry)
				}
				if id, ok := tc["id"].(string); ok && id != "" {
					entry["id"] = id
				}
				if fn, ok := tc["function"].(map[string]any); ok {
					fEntry, _ := entry["function"].(map[string]any)
					if n, ok := fn["name"].(string); ok && n != "" {
						if existing, _ := fEntry["name"].(string); existing == "" {
							fEntry["name"] = n
						} else {
							fEntry["name"] = existing + n
						}
					}
					if a, ok := fn["arguments"].(string); ok && a != "" {
						existing, _ := fEntry["arguments"].(string)
						fEntry["arguments"] = existing + a
					}
				}
			}
		}
	}

	msg := map[string]any{"role": "assistant"}
	content := strings.Join(contentParts, "")
	if content == "" {
		if len(toolCalls) > 0 {
			msg["content"] = nil
		} else {
			msg["content"] = ""
		}
	} else {
		msg["content"] = content
	}
	var validToolCalls []map[string]any
	for _, tc := range toolCalls {
		if fn, ok := tc["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok && strings.TrimSpace(n) != "" {
				validToolCalls = append(validToolCalls, tc)
			}
		}
	}
	if len(validToolCalls) > 0 {
		msg["tool_calls"] = validToolCalls
	}
	if len(reasoningParts) > 0 {
		msg["reasoning_content"] = strings.Join(reasoningParts, "")
	}

	result := map[string]any{
		"id":      first["id"],
		"object":  "chat.completion",
		"created": first["created"],
		"model":   first["model"],
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
	}
	if usage != nil {
		result["usage"] = usage
	}
	if result["id"] == nil {
		result["id"] = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if result["created"] == nil {
		result["created"] = time.Now().Unix()
	}
	if result["model"] == nil {
		result["model"] = ""
	}
	b, err := json.Marshal(result)
	if err != nil {
		return nil, false
	}
	return b, true
}
