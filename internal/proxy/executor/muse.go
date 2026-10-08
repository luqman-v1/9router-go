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
)

// Muse is Meta's Model API (open-sse/providers/registry/muse.js). It is the
// one provider whose transport is chosen per model rather than per provider:
// /v1/chat/completions and /v1/responses both accept the same key, but every
// Muse Spark model pins targetFormat "openai-responses" because its reasoning
// (encrypted_content replay) only round-trips on the Responses lane.
//
// A Muse Code subscription key (minted from a Meta account device-code login)
// additionally needs x-api-version, which the registry's museHeaders hook adds
// only when the credential is an access token rather than a pasted API key.
// Meta never issues a refresh token, so a rejected subscription key means the
// user signs in again — there is nothing to refresh here.

const (
	// museAPIVersion is the Meta protocol version every Muse request carries.
	museAPIVersion = "1.0.0"

	museChatPath      = "/chat/completions"
	museResponsesPath = "/responses"
)

// ForwardMuse handles requests for the muse provider.
func ForwardMuse(w http.ResponseWriter, req *Request) error {
	cleanModel := museModel(req.Body)
	if cleanModel == "" {
		return fmt.Errorf("ForwardMuse: missing model")
	}
	if museTargetFormat(reqCtx(req), cleanModel) == providers.FormatOpenAIResponses {
		return forwardMuseResponses(w, req, cleanModel)
	}
	return forwardMuseChat(w, req)
}

// museTargetFormat reports which of the two lanes a request must travel on.
// Every id the registry declares pins Responses; an undeclared one (upstream
// passthroughModels accepts any id the account has) falls back to the same
// lane rather than guessing the endpoint that 404s.
func museTargetFormat(ctx context.Context, model string) string {
	formats, declared := providers.GetModelFormats("muse", model)
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

// museModel reads the model id the caller asked for and strips the provider
// prefix ("muse/muse-spark-1.3") plus the thinking suffix, so upstream receives
// the bare id it published.
func museModel(body []byte) string {
	var reqObj struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqObj, handlerutil.ClientBody); err != nil {
		return ""
	}
	return stripOpenCodePrefix(reqObj.Model, "muse")
}

// museBaseURL resolves the https://api.meta.ai/v1 prefix of the request's base
// URL: the registry default or a connection override.
func museBaseURL(cfg *providers.ProviderConfig) string {
	base := strings.TrimRight(cfg.BaseURL, "/")
	base = strings.TrimSuffix(base, museChatPath)
	return strings.TrimSuffix(base, museResponsesPath)
}

// museConfig copies the provider config and points it at the chosen lane. The
// registry header map is shared by every request, so the copy is what carries
// the lane's protocol version header.
func museConfig(req *Request, lanePath string) providers.ProviderConfig {
	cfg := *req.Config
	cfg.BaseURL = museBaseURL(&cfg) + lanePath
	cfg.StaticHeaders = copyHeaders(cfg.StaticHeaders)
	if cfg.StaticHeaders == nil {
		cfg.StaticHeaders = map[string]string{"x-api-version": museAPIVersion}
	}
	return cfg
}

// forwardMuseChat serves the /v1/chat/completions lane.
func forwardMuseChat(w http.ResponseWriter, req *Request) error {
	body := req.Body
	if clientFormatFor(reqCtx(req)) == providers.FormatOpenAIResponses {
		// A /v1/responses client asking for a chat-lane model: the Chat
		// endpoint rejects the Responses "input" key.
		converted, err := zenChatLaneBody(req.Body)
		if err != nil {
			return fmt.Errorf("ForwardMuse chat lane conversion: %w", err)
		}
		body = converted
	}
	cfg := museConfig(req, museChatPath)
	resp, err := proxy.ForwardOpenAI(reqCtx(req), req.Client, &cfg, req.APIKey, body, req.IsStream)
	if err != nil {
		return fmt.Errorf("ForwardMuse: %w", err)
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
		return fmt.Errorf("read muse response: %w", err)
	}
	return jsonResponse(req.Ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
}

// forwardMuseResponses serves the /v1/responses lane every Muse Spark model
// pins. A Responses client is forwarded untouched; a Chat Completions caller is
// converted, because the Responses endpoint has no other entry point.
func forwardMuseResponses(w http.ResponseWriter, req *Request, cleanModel string) error {
	body := req.Body
	if clientFormatFor(reqCtx(req)) != providers.FormatOpenAIResponses {
		transformed, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("ForwardMuse transform body: %w", err)
		}
		body = transformed
	}
	body, err := normalizeZenResponsesBody(body, cleanModel)
	if err != nil {
		return fmt.Errorf("normalize muse responses body: %w", err)
	}

	cfg := museConfig(req, museResponsesPath)
	// The Responses lane is consumed frame by frame, so a non-streaming caller
	// asks for the event stream and gets it re-aggregated on the way out.
	resp, err := proxy.ForwardOpenAI(reqCtx(req), req.Client, &cfg, req.APIKey, body, true)
	if err != nil {
		return fmt.Errorf("ForwardMuse (responses): %w", err)
	}
	defer resp.Body.Close()
	if err := upstreamStatusError(resp); err != nil {
		return err
	}
	if req.IsStream {
		stallReader := proxy.NewStallReaderWithContext(reqCtx(req), resp.Body, 0, "muse-responses")
		defer stallReader.Close()
		return handleCodexStream(w, req, stallReader)
	}
	return handleCodexStream(w, req, resp.Body)
}
