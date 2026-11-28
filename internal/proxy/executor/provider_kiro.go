package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

func ForwardKiro(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	body, err := kiroUpstreamBody(req)
	if err != nil {
		return err
	}

	mode := kiroCreateResponseMode()
	hasConversationState := kiroBodyHasConversationState(body)

	// Direct mode: if configured to always use CreateResponse and the body has
	// conversationState, try it first. Any HTTP answer (ok or not) is used;
	// only a network failure/no response falls through to legacy.
	if mode == "always" && hasConversationState {
		crResp, crErr := tryKiroCreateResponse(ctx, req, body)
		if crResp != nil {
			defer func() { _ = crResp.Body.Close() }()
			return writeKiroCreateResponse(w, req, crResp)
		}
		_ = crErr // network failure — continue to legacy
	}

	resp, err := proxy.ForwardKiro(ctx, req.Client, req.Config, req.APIKey, body, req.IsStream, req.ConnData)
	if err != nil {
		// auto: a 429 carrying USER_REQUEST_RATE_EXCEEDED means this account is
		// enrolled in the create_response_api experiment. Fall back in-request
		// to /v1/responses; never touch generic 429 cooldown/failover.
		if mode == "auto" && hasConversationState && kiroRateExceededError(err) {
			crResp, crErr := tryKiroCreateResponse(ctx, req, body)
			if crResp != nil {
				defer func() { _ = crResp.Body.Close() }()
				return writeKiroCreateResponse(w, req, crResp)
			}
			_ = crErr
		}
		return fmt.Errorf("ForwardKiro: %w", err)
	}
	defer resp.Body.Close()
	if req.IsStream {
		return handleKiroStream(w, req, resp.Body)
	}
	return handleKiroNonStream(w, req, resp.Body)
}

// kiroBodyHasConversationState reports whether the translated upstream body is
// a Kiro conversationState envelope (the only shape CreateResponse accepts).
func kiroBodyHasConversationState(body []byte) bool {
	var probe map[string]any
	if json.Unmarshal(body, &probe) != nil {
		return false
	}
	_, ok := probe["conversationState"]
	return ok
}

// kiroRateExceededError matches the experiment's 429 marker without relying on
// error text: UpstreamError carries the raw body.
func kiroRateExceededError(err error) bool {
	var upstream *proxy.UpstreamError
	if !errors.As(err, &upstream) {
		return false
	}
	return upstream.StatusCode == http.StatusTooManyRequests &&
		bytes.Contains(upstream.Body, []byte(kiroCreateResponseMarker))
}

// tryKiroCreateResponse builds and sends the /v1/responses request. A non-nil
// response means an HTTP answer arrived; nil means a network failure.
func tryKiroCreateResponse(ctx context.Context, req *Request, kiroBody []byte) (*http.Response, error) {
	crBody, err := buildKiroCreateResponseBody(kiroBody, req.ModelName, req.ConnData)
	if err != nil {
		return nil, err
	}
	region := ""
	if req.ConnData != nil {
		region, _ = req.ConnData["region"].(string)
	}
	endpoint := kiroCreateResponseURL
	if endpoint == "" {
		endpoint = kiroCreateResponseEndpoint(region)
	}
	headers := buildKiroCreateResponseHeaders(req.APIKey, req.ConnData, crBody)
	return executeKiroCreateResponse(ctx, req.Client, endpoint, headers, crBody)
}

// writeKiroCreateResponse turns the CreateResponse HTTP answer into the
// client's requested format. The CreateResponse output is SSE — never feed it
// to the binary EventStream parser used by the legacy Kiro path.
func writeKiroCreateResponse(w http.ResponseWriter, req *Request, resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: body, Header: resp.Header}
	}
	model := req.ModelName
	if model == "" {
		model = "kiro"
	}
	stream := kiroCreateResponseStream(req.Ctx, resp.Body, model)
	defer func() { _ = stream.Close() }()
	if req.IsStream {
		return execSSEStream(w, stream, req)
	}
	return jsonResponse(req.Ctx, w, stream, req.TranslateResp, req.ResponseBuf)
}

// kiroUpstreamBody translates an OpenAI chat body into the Kiro envelope,
// passing through bodies that already use conversationState.
func kiroUpstreamBody(req *Request) ([]byte, error) {
	var probe map[string]any
	if err := json.Unmarshal(req.Body, &probe); err != nil {
		return nil, fmt.Errorf("ForwardKiro: parse request body: %w", err)
	}
	if _, ok := probe["conversationState"]; ok {
		// Already a Kiro envelope (MITM passthrough) — only clean it.
		return req.Body, nil
	}

	model := req.ModelName
	if model == "" {
		var oreq struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(req.Body, &oreq); err == nil {
			model = oreq.Model
		}
	}
	// Drop the provider prefix the client sent (kr/claude-sonnet-4.5).
	if _, after, ok := strings.Cut(model, "/"); ok {
		model = after
	}
	// -thinking / -agentic are 9router fictions, stripped before upstream.
	model = strings.TrimSuffix(model, "-thinking")
	model = strings.TrimSuffix(model, "-agentic")
	if idx := strings.IndexByte(model, '('); idx > 0 {
		model = model[:idx]
	}
	// Anthropic spellings (claude-sonnet-4-5-20250929) 400 on Kiro's data
	// plane; fold them onto the dotted upstream alias.
	model = normalizeKiroUpstreamModelId(model)

	profileArn, authMethod := "", ""
	if req.ConnData != nil {
		if s, ok := req.ConnData["profileArn"].(string); ok {
			profileArn = strings.TrimSpace(s)
		}
		authMethod, _ = req.ConnData["authMethod"].(string)
	}

	out, err := translator.OpenAIToKiro(req.Body, translator.KiroTranslateOptions{
		Model:      model,
		ProfileArn: providers.KiroEffectiveProfileARN(authMethod, profileArn),
	})
	if err != nil {
		log.Error("executor", "kiro translation failed", "model", model, "error", err)
		return nil, &proxy.UpstreamError{
			StatusCode: http.StatusBadRequest,
			Body:       jsonErrorBody(fmt.Sprintf("kiro request could not be built: %v", err), "invalid_request_error", http.StatusBadRequest),
		}
	}
	return out, nil
}

// ForwardAzure forwards to Azure OpenAI with dynamic URL from env vars.
