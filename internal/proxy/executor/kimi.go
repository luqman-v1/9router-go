package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/thinking"
)

// Kimi Code is the second provider whose transport is chosen per request rather
// than per provider (open-sse/providers/registry/kimi.js transports[]). The same
// key answers three endpoints:
//
//	/coding/v1/chat/completions  Chat Completions
//	/coding/v1/messages          Claude Messages
//	/coding/v1/responses         OpenAI Responses
//
// Upstream picks the transport matching the client's sourceFormat so the request
// travels lossless (open-sse/handlers/chatCore.js resolveTransport +
// open-sse/services/provider.js resolveTransport). A /v1/responses client is
// therefore forwarded natively to /responses instead of being downgraded to
// chat, which is what a single-URL Chat Completions executor used to do.
//
// The Claude Messages entry is not a lane here: a /v1/messages caller reaches an
// executor already converted to OpenAI unless the model declares a Messages
// target (see ServesMessagesEndpoint), and no Kimi model declares one.

const (
	kimiChatPath      = "/chat/completions"
	kimiResponsesPath = "/responses"
)

// ForwardKimi handles requests for the kimi and kimi-coding providers.
//
// A client-supplied reasoning_effort is normalized onto the values Kimi's
// backend accepts before forwarding, so "auto", "none", "off", "xhigh" and
// "max" are not answered with HTTP 400 (upstream decolua/9router#2427).
func ForwardKimi(w http.ResponseWriter, req *Request) error {
	body := normalizeKimiReasoningEffort(req.Body)
	cleanModel := kimiModel(body)
	if cleanModel == "" {
		return fmt.Errorf("ForwardKimi: missing model")
	}
	if kimiTargetFormat(reqCtx(req), cleanModel) == providers.FormatOpenAIResponses {
		return forwardKimiResponses(w, req, cleanModel, body)
	}
	return forwardKimiChat(w, req, body)
}

// kimiTargetFormat reports which lane a request must travel on. Both lanes
// answer on the same key, so a client that already speaks one of them is
// forwarded as-is; only a format the model does not declare falls back to the
// model's target lane. An id the registry does not publish takes the Responses
// lane, which every Kimi Code model serves natively.
func kimiTargetFormat(ctx context.Context, model string) string {
	formats, declared := providers.GetModelFormats("kimi", model)
	if !declared {
		return providers.FormatOpenAIResponses
	}
	// A client that already speaks a lane the model serves is forwarded as-is;
	// upstream prefers that match over the model-level target format.
	if formats.SupportsFormat(clientFormatFor(ctx)) {
		return clientFormatFor(ctx)
	}
	return formats.TargetFormat
}

// kimiModel reads the model id the caller asked for and strips the provider
// prefix ("kimi/kimi-k3") plus the thinking suffix, so upstream receives the
// bare id it published.
func kimiModel(body []byte) string {
	var reqObj struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqObj, handlerutil.ClientBody); err != nil {
		return ""
	}
	return stripOpenCodePrefix(reqObj.Model, "kimi-coding", "kimi", "kmc", "km")
}

// kimiBaseURL resolves the https://api.kimi.com/coding/v1 prefix of the
// request's base URL: the registry default or a connection override, whichever
// lane that base URL named.
func kimiBaseURL(cfg *providers.ProviderConfig) string {
	base := strings.TrimRight(cfg.BaseURL, "/")
	base = strings.TrimSuffix(base, kimiChatPath)
	return strings.TrimSuffix(base, kimiResponsesPath)
}

// kimiConfig copies the provider config and points it at the chosen lane. The
// registry header map is shared by every request, so the copy is what keeps a
// lane swap from writing through to the registry entry.
func kimiConfig(req *Request, lanePath string) providers.ProviderConfig {
	cfg := *req.Config
	cfg.BaseURL = kimiBaseURL(&cfg) + lanePath
	return cfg
}

// forwardKimiChat serves the /coding/v1/chat/completions lane.
func forwardKimiChat(w http.ResponseWriter, req *Request, body []byte) error {
	if clientFormatFor(reqCtx(req)) == providers.FormatOpenAIResponses {
		// A /v1/responses client that landed here because its model does not
		// declare the Responses lane: the Chat endpoint rejects the Responses
		// "input" key.
		converted, err := zenChatLaneBody(body)
		if err != nil {
			return fmt.Errorf("ForwardKimi chat lane conversion: %w", err)
		}
		body = converted
	}
	cfg := kimiConfig(req, kimiChatPath)
	resp, err := proxy.ForwardOpenAI(reqCtx(req), req.Client, &cfg, req.APIKey, body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardKimi: %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		return err
	}
	if req.IsStream {
		return execSSEStream(w, resp.Body, req)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read kimi response: %w", err)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
}

// forwardKimiResponses serves the /coding/v1/responses lane Kimi Code serves
// natively. A Responses client is forwarded untouched; a Chat Completions caller
// is converted, because the Responses endpoint has no other entry point.
func forwardKimiResponses(w http.ResponseWriter, req *Request, cleanModel string, body []byte) error {
	if clientFormatFor(reqCtx(req)) != providers.FormatOpenAIResponses {
		transformed, _, err := buildResponsesBody(body)
		if err != nil {
			return fmt.Errorf("ForwardKimi transform body: %w", err)
		}
		body = transformed
	}
	// Upstream publishes the bare id; the provider prefix the caller may have
	// typed must not ride along with it.
	body = withJSONFields(body, map[string]any{"model": cleanModel})
	body, err := normalizeZenResponsesBody(body, cleanModel)
	if err != nil {
		return fmt.Errorf("normalize kimi responses body: %w", err)
	}

	cfg := kimiConfig(req, kimiResponsesPath)
	// The Responses lane is consumed frame by frame, so a non-streaming caller
	// asks for the event stream and gets it re-aggregated on the way out.
	resp, err := proxy.ForwardOpenAI(reqCtx(req), req.Client, &cfg, req.APIKey, body, true)
	if err != nil {
		return fmt.Errorf("ForwardKimi (responses): %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		return err
	}
	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(reqCtx(req), resp.Body, req.Config.ResolveStallTimeout(proxy.ResolveDefaultStallTimeout()), "kimi-responses")
		defer stallReader.Close()
		return handleCodexStream(w, req, stallReader)
	}
	return handleCodexStream(w, req, resp.Body)
}

// normalizeKimiReasoningEffort folds a client-supplied reasoning_effort onto
// the values Kimi's backend accepts, so "auto", "none", "off", "xhigh" and
// "max" are not forwarded verbatim and answered with HTTP 400 (upstream
// decolua/9router#2427). "none"/"off" are dropped; the rest go through
// thinking.ToKimiReasoningEffort (auto→high, minimal→low, xhigh→max,
// low/medium/high/max pass through).
func normalizeKimiReasoningEffort(body []byte) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body
	}
	raw, ok := m["reasoning_effort"].(string)
	if !ok || raw == "" {
		return body
	}
	eff := thinking.ToKimiReasoningEffort(kimiReasoningIntent(raw))
	if eff == "" {
		delete(m, "reasoning_effort")
	} else {
		m["reasoning_effort"] = eff
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// kimiReasoningIntent converts a raw reasoning_effort string into the intent
// thinking.ToKimiReasoningEffort maps onto Kimi's accepted enum. It mirrors
// effortString: none/off disable reasoning, auto leaves it to the upstream,
// anything else is a discrete level.
func kimiReasoningIntent(raw string) *thinking.Intent {
	e := strings.ToLower(strings.TrimSpace(raw))
	switch e {
	case "none", "off":
		return &thinking.Intent{Mode: thinking.ModeNone}
	case "auto":
		return &thinking.Intent{Mode: thinking.ModeAuto}
	default:
		return &thinking.Intent{Mode: thinking.ModeLevel, Level: e}
	}
}
