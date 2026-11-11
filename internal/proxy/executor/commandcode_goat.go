package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

// Command Code's documented Provider API (https://commandcode.ai/docs/provider)
// is a different product surface from the `commandcode` provider beside it.
// That entry speaks the CLI's internal /alpha/generate envelope with spoofed
// CLI identity headers, because it predates the public API. This provider is
// the documented one: standard OpenAI and Anthropic schemas, three lanes, no
// identity spoofing, and the same key.
//
//	/provider/v1/chat/completions  OpenAI Chat Completions
//	/provider/v1/responses         OpenAI Responses
//	/provider/v1/messages          Anthropic Messages
//
// The lane is a property of the MODEL, not a preference: the live catalogue
// publishes each id's routes in supported_endpoints, and
// providers.commandcodeGoatModelFormats is that declaration. Sending a Claude
// id to /chat/completions answers
//
//	400 {"code":"unsupported_model","message":"Model \"…\" must be called via
//	/provider/v1/messages (Anthropic Messages shape)."}
//
// and the reverse is refused the same way, so routing follows the feed.

const (
	commandcodeGoatChatPath      = "/chat/completions"
	commandcodeGoatResponsesPath = "/responses"
	commandcodeGoatMessagesPath  = "/messages"
)

// ForwardCommandcodeGoat routes one request onto the lane its model declares.
func ForwardCommandcodeGoat(w http.ResponseWriter, req *Request) error {
	cleanModel := commandcodeGoatModel(req.Body)
	if cleanModel == "" {
		return fmt.Errorf("ForwardCommandcodeGoat: missing model")
	}
	switch providers.CommandcodeGoatTarget(cleanModel) {
	case providers.FormatClaude:
		return forwardCommandcodeGoatMessages(w, req, cleanModel)
	case providers.FormatOpenAIResponses:
		return forwardCommandcodeGoatResponses(w, req, cleanModel)
	default:
		return forwardCommandcodeGoatChat(w, req, cleanModel)
	}
}

// commandcodeGoatModel reads the model the caller asked for and strips the
// provider prefix plus the thinking suffix, so the upstream receives the bare
// id it published.
func commandcodeGoatModel(body []byte) string {
	var reqObj struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqObj, handlerutil.ClientBody); err != nil {
		return ""
	}
	return stripOpenCodePrefix(reqObj.Model, "commandcode-goat", "cmc-goat")
}

// commandcodeGoatBaseURL resolves the https://api.commandcode.ai/provider/v1
// prefix of the connection's base URL: the registry default, a connection
// override, or whichever lane the saved URL already named.
func commandcodeGoatBaseURL(cfg *providers.ProviderConfig) string {
	base := strings.TrimRight(cfg.BaseURL, "/")
	for _, lane := range []string{commandcodeGoatChatPath, commandcodeGoatResponsesPath, commandcodeGoatMessagesPath} {
		if strings.HasSuffix(base, lane) {
			return strings.TrimSuffix(base, lane)
		}
	}
	return base
}

// commandcodeGoatHeaders is the header set every lane dials with. The docs
// accept the same bearer on all three routes.
//
// A connection that opts into zero data retention sends x-cmd-zdr: 1, which the
// provider enforces upstream — a request whose model has no ZDR-capable
// provider fails with 422 cmd_zdr_no_providers rather than leaking data.
func commandcodeGoatHeaders(req *Request) map[string]string {
	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + req.APIKey,
	}
	if commandcodeGoatZDR(req.ConnData) {
		headers["x-cmd-zdr"] = "1"
	}
	if req.Config != nil {
		for k, v := range req.Config.StaticHeaders {
			headers[k] = v
		}
		for k, v := range req.Config.HeaderOverrides {
			headers[k] = v
		}
	}
	return headers
}

// commandcodeGoatZDR reports whether the connection asked for zero data
// retention. It is read from providerSpecificData so the opt-in travels with
// the account rather than with the process.
func commandcodeGoatZDR(connData map[string]any) bool {
	if connData == nil {
		return false
	}
	switch v := connData["zdr"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true") || v == "1"
	case float64:
		return v != 0
	}
	return false
}

// forwardCommandcodeGoatChat serves /provider/v1/chat/completions: the plain
// OpenAI lane, so an OpenAI client's body travels untouched.
func forwardCommandcodeGoatChat(w http.ResponseWriter, req *Request, cleanModel string) error {
	body := req.Body
	if translator.IsResponsesClient(reqCtx(req)) {
		// A /v1/responses client on a model the Responses lane does not serve:
		// the Chat endpoint rejects the Responses "input" key, so the body is
		// converted and the answer bridged back on the way out.
		converted, err := zenChatLaneBody(body)
		if err != nil {
			return fmt.Errorf("ForwardCommandcodeGoat chat lane conversion: %w", err)
		}
		body = converted
	}
	body = withJSONFields(body, map[string]any{"model": cleanModel})

	url := commandcodeGoatBaseURL(req.Config) + commandcodeGoatChatPath
	resp, err := commandcodeGoatDial(reqCtx(req), req, url, body)
	if err != nil {
		return fmt.Errorf("ForwardCommandcodeGoat (chat): %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		return err
	}
	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	return jsonResponse(req.Ctx, w, resp.Body, req.TranslateResp, req.ResponseBuf)
}

// forwardCommandcodeGoatResponses serves /provider/v1/responses. The docs say a
// Responses client's tool array passes through as sent, so a Responses caller
// is relayed untouched; a Chat caller is converted, because the Responses
// endpoint has no other entry point.
func forwardCommandcodeGoatResponses(w http.ResponseWriter, req *Request, cleanModel string) error {
	body := req.Body
	if !translator.IsResponsesClient(reqCtx(req)) {
		transformed, _, err := buildResponsesBody(body)
		if err != nil {
			return fmt.Errorf("ForwardCommandcodeGoat responses transform: %w", err)
		}
		body = transformed
	}
	body = withJSONFields(body, map[string]any{"model": cleanModel})

	url := commandcodeGoatBaseURL(req.Config) + commandcodeGoatResponsesPath
	resp, err := commandcodeGoatDial(reqCtx(req), req, url, body)
	if err != nil {
		return fmt.Errorf("ForwardCommandcodeGoat (responses): %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		return err
	}
	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(reqCtx(req), resp.Body,
			req.Config.ResolveStallTimeout(proxy.ResolveDefaultStallTimeout()), "commandcode-goat-responses")
		defer stallReader.Close()
		return handleCodexStream(w, req, stallReader)
	}
	return handleCodexStream(w, req, resp.Body)
}

// forwardCommandcodeGoatMessages serves /provider/v1/messages. A /v1/messages
// caller already speaks the wire (the handler keeps its body Claude-shaped for a
// model that declares the Messages target, see executor.ServesMessagesEndpoint);
// every other caller is converted.
func forwardCommandcodeGoatMessages(w http.ResponseWriter, req *Request, cleanModel string) error {
	body := req.Body
	switch {
	case req.ClaudeClient && !translator.IsResponsesClient(reqCtx(req)):
		// The client already speaks the Messages wire format.
	case translator.IsResponsesClient(reqCtx(req)):
		converted, err := zenChatLaneBody(req.Body)
		if err != nil {
			return fmt.Errorf("ForwardCommandcodeGoat messages lane conversion: %w", err)
		}
		body = EnsureClaudeMessagesFor(converted, "commandcode-goat", cleanModel)
	default:
		body = EnsureClaudeMessagesFor(body, "commandcode-goat", cleanModel)
	}
	body = withJSONFields(body, map[string]any{"model": cleanModel})

	url := commandcodeGoatBaseURL(req.Config) + commandcodeGoatMessagesPath
	resp, err := commandcodeGoatDial(reqCtx(req), req, url, body)
	if err != nil {
		return fmt.Errorf("ForwardCommandcodeGoat (messages): %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		return err
	}
	if req.IsStream {
		return handleClaudeMessagesStream(w, req, resp.Body)
	}
	return handleClaudeMessagesNonStream(w, req, resp.Body)
}

// commandcodeGoatDial posts one already-shaped body to one lane.
func commandcodeGoatDial(ctx context.Context, req *Request, url string, body []byte) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range commandcodeGoatHeaders(req) {
		httpReq.Header.Set(k, v)
	}
	if httpReq.Header.Get("Accept") == "" {
		httpReq.Header.Set("Accept", "application/json")
	}
	resp, err := req.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}
	proxy.UnwrapIdentityGzip(httpReq, resp)
	return resp, nil
}
