package chat

import (
	"context"
	json "9router/proxy/internal/fastjson"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	internalproxy "9router/proxy/internal/proxy"
	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/translator"
)

// responsesEndpoint labels the client-facing endpoint in usage logs.
const responsesEndpoint = "/v1/responses"

// forwardEndpoint labels the client-facing endpoint in usage logs and decides
// whether an Anthropic upstream is fed a native Messages body. A Responses
// client is not a Claude one, so it must never be labelled /v1/messages.
func forwardEndpoint(ctx context.Context, claudeClient string) string {
	if translator.IsResponsesClient(ctx) {
		return responsesEndpoint
	}
	return claudeClient
}

// newResponsesBridge builds a Chat→Responses bridge that writes through an
// already-committed SSE writer. Producers that translate upstream into OpenAI
// chunks on the way out — the Claude and Gemini paths — hand their finished
// frames to it instead of writing them raw, so a /v1/responses client is served
// correctly no matter which translation produced the Chat shape.
func newResponsesBridge(ctx context.Context, hw *internalproxy.HeartbeatWriter, flusher http.Flusher, metrics *streamMetrics, start time.Time) *executor.ResponsesBridge {
	return executor.NewResponsesBridge(
		translator.RequestedModelFromContext(ctx),
		translator.CustomToolNamesFrom(ctx),
		func(frame []byte) error {
			if metrics != nil {
				if metrics.TTFT == 0 {
					metrics.TTFT = time.Since(start).Milliseconds()
				}
				metrics.ResponseBuf.Write(frame)
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

// HandleResponses handles POST /v1/responses (OpenAI Responses API clients).
//
// It is the mirror image of HandleMessages: the client speaks Responses while
// the upstream may only speak Chat Completions, so the request is converted on
// the way in and the response is converted back on the way out. The wire format
// the client uses travels on the context, because the executors are the layer
// that has to know it and threading a new bool through every request struct
// would touch five call sites for a single flag.
func (h *ChatHandler) HandleResponses(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("chat", "read body failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	var reqBody struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		log.Error("chat", "parse JSON failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if reqBody.Model == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "missing model")
		return
	}


	// Per-key model policy: 403 before the responses bridge or any connection
	// selection.
	if !h.enforceModelAccess(w, r, reqBody.Model) {
		return
	}
	modelInfo, err := h.resolveModel(reqBody.Model)
	if err != nil {
		log.Error("chat", "resolve model failed", "error", err, "model", reqBody.Model)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := h.newResponsesContext(r, reqBody.Model, modelInfo, &body)

	var workingBody map[string]any
	if err := json.Unmarshal(body, &workingBody); err != nil {
		log.Error("chat", "parse responses body failed", "error", err)
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	workingBody["stream"] = reqBody.Stream

	requiredCaps := DetectRequiredCapabilities(body)

	if len(modelInfo.ComboModels) > 0 {
		augmented, comboStrategy, injected := h.applyCapacityAdapter(modelInfo.ComboModels, requiredCaps, modelInfo.Strategy, reqBody.Model)
		if modelInfo.Strategy == "fusion" {
			bodyJSON, err := json.Marshal(workingBody)
			if err != nil {
				handlerutil.WriteJSONError(w, http.StatusInternalServerError, "failed to marshal request body")
				return
			}
			h.handleFusion(ctx, w, bodyJSON, modelInfo.ComboModels, modelInfo.Strategy, reqBody.Stream, false, reqBody.Model, modelInfo.StickyLimit, modelInfo.JudgeModel)
			return
		}
		h.handleMessagesComboFallback(ctx, w, workingBody, augmented, comboStrategy, reqBody.Stream, reqBody.Model, modelInfo.StickyLimit, injected...)
		return
	}

	targetEntry := reqBody.Model
	if !strings.Contains(targetEntry, "/") && modelInfo.Provider != "" {
		targetEntry = modelInfo.Provider + "/" + modelInfo.Model
	}
	augmented, strat := h.AugmentModelsWithCapacityAdapter([]string{targetEntry}, requiredCaps)
	if len(augmented) > 1 {
		injected := augmented[:len(augmented)-1]
		log.Info("chat", "capacity adapter auto-switch responses", "target", reqBody.Model, "switched_to", augmented[0], "caps", keysString(requiredCaps))
		h.handleMessagesComboFallback(ctx, w, workingBody, augmented, strat, reqBody.Stream, reqBody.Model, 0, injected...)
		return
	}

	h.handleResponsesSingleModel(ctx, w, workingBody, modelInfo, reqBody.Stream)
}

// newResponsesContext prepares the request context and rewrites body into the
// format the resolved upstream actually speaks.
//
// The native check runs here rather than inside the executor because a combo
// or a capacity-adapter switch can still land on a different provider than the
// one resolved above, and a request already converted to Chat Completions
// cannot travel back to Responses. When the upstream speaks Responses natively
// nothing is touched at all: upstream makes the same call by skipping
// translation when the source format already equals the target format.
func (h *ChatHandler) newResponsesContext(r *http.Request, requestedModel string, modelInfo *ModelInfo, body *[]byte) context.Context {
	ctx := handlerutil.WithSessionID(r.Context(), handlerutil.ExtractSessionID(r))
	ctx = translator.WithClientFormat(ctx, translator.ClientFormatResponses)
	ctx = translator.WithRequestedModel(ctx, stripModelContextMarker(requestedModel))
	// The dispatching key decides which guardrail policy governs the response.
	ctx = withGuardrailKey(ctx, requestKeyID(r))

	providerCfg, err := h.getProviderConfig(modelInfo.Provider, nil)
	if err == nil && executor.UpstreamSpeaksResponses(modelInfo.Provider, modelInfo.Model, providerCfg) {
		log.Debug("chat", "responses passthrough", "provider", modelInfo.Provider, "model", modelInfo.Model)
		return ctx
	}

	converted, err := translator.ResponsesToChatRequest(*body)
	if err != nil {
		log.Error("chat", "responses request conversion failed", "error", err)
		return ctx
	}
	if !converted.Converted {
		return ctx
	}
	*body = converted.Body
	ctx = translator.WithCustomToolNames(ctx, converted.CustomToolNames)
	return translator.WithResponsesBridge(ctx)
}

// handleResponsesSingleModel forwards a prepared Responses request for a single
// model. translateResponse stays false: that flag marks a Claude Messages
// client, and a Responses client is never one.
func (h *ChatHandler) handleResponsesSingleModel(ctx context.Context, w http.ResponseWriter, preparedReq map[string]any, modelInfo *ModelInfo, isStream bool) {
	cw := newCommittedResponseWriter(w)
	preparedReq["model"] = modelInfo.Model
	finalBody, err := json.Marshal(preparedReq)
	if err != nil {
		handlerutil.WriteJSONError(cw, http.StatusInternalServerError, "failed to marshal responses request")
		return
	}

	result := h.handleAccountFallback(ctx, cw, modelInfo.Provider, modelInfo.Model, modelInfo.ConnectionID, finalBody, isStream, false, responsesEndpoint)
	if result == nil {
		return
	}
	if cw.IsCommitted() {
		log.Error("chat", "upstream error after headers committed", "error", result)
		return
	}
	var ue *upstreamError
	if errors.As(result, &ue) {
		cw.Header().Set("Content-Type", "application/json")
		cw.WriteHeader(ue.StatusCode)
		cw.Write(ue.Body)
		return
	}
	handlerutil.WriteJSONError(cw, http.StatusBadGateway, fmt.Sprintf("upstream error: %v", result))
}
