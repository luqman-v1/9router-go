package executor

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
	"9router/proxy/internal/translator"
)

func ForwardOpencode(w http.ResponseWriter, req *Request) error {
	apiKey := req.APIKey
	if apiKey == "" {
		apiKey = "public"
	}

	var reqObj struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(req.Body, &reqObj)
	cleanModel := strings.TrimPrefix(reqObj.Model, "oc/")
	cleanModel = strings.TrimPrefix(cleanModel, "opencode/")
	if parenIdx := strings.IndexByte(cleanModel, '('); parenIdx != -1 {
		cleanModel = cleanModel[:parenIdx]
	}

	if isOpencodeResponsesModel(cleanModel) {
		// Route through Responses API format: https://opencode.ai/zen/v1/responses
		transformedBody, _, err := buildResponsesBody(req.Body)
		if err != nil {
			return fmt.Errorf("transform body for muse-spark: %w", err)
		}

		// reqObj.Model, not cleanModel: the "(level)" override and the
		// reasoning effort it encodes live in the untouched id (see
		// normalizeMuseSparkResponsesBody).
		transformedBody, err = normalizeMuseSparkResponsesBody(transformedBody, reqObj.Model)
		if err != nil {
			return fmt.Errorf("normalize muse-spark body: %w", err)
		}

		// The free-tier gate fingerprints the lowercase tool quartet; capitalised
		// variants from Claude Code CLI are renamed here and restored on the
		// response (translator.ConcealFingerprintTools).
		transformedBody, toolNameMap := shapeOpenCodeFreeBody(transformedBody, true, cleanModel)
		transformedBody = translator.ApplyFingerprintSystemPrompt(transformedBody)
		req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
		w = NewToolNameRestoringWriter(w, toolNameMap)

		// The lane URL is what the relay forwards to, so it is built the same way
		// for a relayed connection as for a direct one.
		cfg := *req.Config
		baseURL := strings.TrimRight(cfg.BaseURL, "/")
		baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
		cfg.BaseURL = opencodeInferenceBase(baseURL) + "/responses"
		baseHeaders := cfg.StaticHeaders
		hint := opencodeSessionHint(req, true)
		cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(cfg.StaticHeaders, hint, req.IsStream)
		ctx := req.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		resp, err := forwardOpenCodeRotating(ctx, func() (*http.Response, error) {
			return forwardOpencodeRequest(ctx, req.Client, &cfg, apiKey, transformedBody, req.IsStream)
		}, func() (*http.Response, error) {
			return forwardOpencodeRequestDirect(ctx, &cfg, apiKey, transformedBody, req.IsStream)
		}, opencodeSessionRotation(req, true, hint, func(session string) {
			cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(baseHeaders, session, req.IsStream)
		}), forgetOpencodeStableSession(req, true, hint))
		if err != nil {
			return fmt.Errorf("ForwardOpencode (muse-spark responses): %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
			return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody, Header: resp.Header}
		}

		// First SSE frame before the 200 head, same as the chat lane: a
		// refusal or a stall must still be able to fail over.
		responsesBody, ferr := awaitOpenCodeFirstEvent(ctx, resp, 0)
		if ferr != nil {
			return ferr
		}
		if req.IsStream {
			stallReader := proxy.NewStallReaderWithContext(ctx, responsesBody, cfg.ResolveStallTimeout(proxy.ResolveDefaultStallTimeout()), "opencode-responses")
			defer stallReader.Close()
			return handleCodexStream(w, req, stallReader)
		}
		return handleCodexStream(w, req, responsesBody)
	}

	body := InjectReasoningContent(req.Body, "opencode")
	// The free lane's MiMo route streams thinking as reasoning +
	// reasoning_details[] instead of reasoning_content; the passthrough lifts
	// the alias so OpenAI-shaped clients see the thinking. Every other model
	// on the lane already emits reasoning_content, so the lift is a no-op
	// there (upstream quirks.reasoningAliasFields).
	req.LiftReasoningAliases = true
	// opencode Chat Completions path: same conceal + restore of tool names.
	body, toolNameMap := shapeOpenCodeFreeBody(body, false, cleanModel)
	body = translator.ApplyFingerprintSystemPrompt(body)
	req.Ctx = translator.WithToolNameMap(req.Ctx, toolNameMap)
	w = NewToolNameRestoringWriter(w, toolNameMap)
	// Strict models wrap caller tool calls as execute/CALL_TOOL; hand the
	// client back its own spelling. The bridge buffers whole SSE lines, so it
	// may only wrap a streaming response — the non-streaming lane rewrites
	// the re-aggregated JSON instead (bridgeOpenCodeExecuteToolCallsJSON).
	if req.IsStream {
		w = NewOpenCodeExecuteBridgeWriter(w)
	}

	// Upstream OpenCode free tier strictly requires stream=true.
	// Non-streaming calls are blocked with 403 FreeTierError.
	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err == nil {
		reqMap["stream"] = true
		if cleanModel != "" {
			reqMap["model"] = cleanModel
		}
		// A reasoning model spends the same output ceiling on its thinking as on
		// the answer, so a small client budget returns empty content with
		// finish_reason "length" — see output_budget.go for the measurements.
		clampOpenCodeOutputBudget(reqMap, "opencode", cleanModel)
		includeStreamUsage(reqMap)
		// The free backend validates reasoning_effort against the plain
		// OpenAI enum and answers 400 "Invalid option" for "auto", which
		// adaptive thinking (Claude Code's thinking:{type:"adaptive"})
		// arrives as. Omitting it lets the upstream pick its own default,
		// which is what adaptive means.
		if v, ok := reqMap["reasoning_effort"].(string); ok && v == "auto" {
			delete(reqMap, "reasoning_effort")
		}
		// Deterministic marshaling keeps the serialized request prefix
		// stable across turns, which DeepSeek's prefix prompt cache
		// requires; json/v2 randomizes map member order by default.
		if b, err := marshalOpenCodeFreeChatBody(reqMap); err == nil {
			body = b
		}
	}

	// One lane URL for both egress paths: a relay forwards to whatever this names.
	cfg := *req.Config
	cfg.BaseURL = opencodeChatURL(cfg.BaseURL)
	baseHeaders := cfg.StaticHeaders
	hint := opencodeSessionHint(req, true)
	cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(cfg.StaticHeaders, hint, true)

	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := forwardOpenCodeRotating(ctx, func() (*http.Response, error) {
		return forwardOpencodeRequest(ctx, req.Client, &cfg, apiKey, body, true)
	}, func() (*http.Response, error) {
		return forwardOpencodeRequestDirect(ctx, &cfg, apiKey, body, true)
	}, opencodeSessionRotation(req, true, hint, func(session string) {
		cfg.StaticHeaders = proxy.BuildOpenCodeHeaders(baseHeaders, session, true)
	}), forgetOpencodeStableSession(req, true, hint))
	if err != nil {
		return fmt.Errorf("ForwardOpencode: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
		return &proxy.UpstreamError{StatusCode: resp.StatusCode, Body: errBody, Header: resp.Header}
	}

	// Read the first SSE frame before committing 200 OK. The free-tier gate can
	// answer a refusal inside a 200 frame, or stall before its first one, and
	// once the response head is on the wire no failover is possible — so this
	// has to happen here rather than inside the stream writer.
	streamBody, ferr := awaitOpenCodeFirstEvent(ctx, resp, 0)
	if ferr != nil {
		return ferr
	}

	if req.IsStream {
		return execSSEStream(w, streamBody, req)
	}
	data, err := io.ReadAll(io.LimitReader(streamBody, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read opencode response: %w", err)
	}
	if converted, ok := sseToOpenAIJSON(data); ok {
		data = converted
	}
	data = bridgeOpenCodeExecuteToolCallsJSON(data)
	return jsonResponse(req.Ctx, w, bytes.NewReader(data), req.TranslateResp, req.ResponseBuf)
}

// opencodeInferenceBase rewrites a zen-layout base URL to the v2 inference
// gate's /inference/openai/v1 prefix. Any host mirroring the zen layout follows the
// same migration, so the rewrite keys off the prefix rather than the host. A
// base with no zen prefix — a custom endpoint, or the registry default that
// already points at /inference/openai/v1 — is returned untouched.
func opencodeInferenceBase(base string) string {
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	base = strings.TrimSuffix(base, "/messages")
	base = strings.TrimSuffix(base, "/responses")
	switch {
	case strings.HasSuffix(base, "/zen/v1"):
		return strings.TrimSuffix(base, "/zen/v1") + "/inference/openai/v1"
	case strings.HasSuffix(base, "/inference/openai/v1"):
		return base
	case strings.HasSuffix(base, "/inference/v1"):
		return strings.TrimSuffix(base, "/inference/v1") + "/inference/openai/v1"
	default:
		return base
	}
}

// opencodeMessagesInferenceBase keeps a zen-layout base URL on the legacy
// /inference/v1 prefix: the v2 gate's /inference/openai/v1 surface has no
// /messages route (404), while the legacy prefix still serves Claude
// Messages. Mirrors opencodeInferenceBase, keyed off the prefix rather than
// the host.
func opencodeMessagesInferenceBase(base string) string {
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/chat/completions")
	base = strings.TrimSuffix(base, "/messages")
	base = strings.TrimSuffix(base, "/responses")
	switch {
	case strings.HasSuffix(base, "/zen/v1"):
		return strings.TrimSuffix(base, "/zen/v1") + "/inference/v1"
	case strings.HasSuffix(base, "/inference/openai/v1"):
		return strings.TrimSuffix(base, "/inference/openai/v1") + "/inference/v1"
	case strings.HasSuffix(base, "/inference/v1"):
		return base
	default:
		return base
	}
}

func opencodeChatURL(base string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	trimmed := strings.TrimSuffix(base, "/chat/completions")
	if strings.HasSuffix(trimmed, "/zen/v1") {
		return strings.TrimSuffix(trimmed, "/zen/v1") + "/inference/openai/v1/chat/completions"
	}
	if strings.HasSuffix(trimmed, "/inference/v1") {
		return strings.TrimSuffix(trimmed, "/inference/v1") + "/inference/openai/v1/chat/completions"
	}
	return base
}

// isOpenCodeAutoToolChoiceModel ports the upstream forceAutoToolChoiceModels
// quirk (open-sse/executors/opencode.js): only models confirmed auto-only get
// their explicit tool_choice rewritten, so the allowlist stays closed until a
// model is probed.
func isOpenCodeAutoToolChoiceModel(cleanModel string) bool {
	return cleanModel == "muse-spark-1.3-contributor-free"
}

// isOpencodeFreeModel reports whether a bare model id belongs to the OpenCode
// free tier. Keyed zen requests for these ids answer 403 ("free tier can only
// be used from within OpenCode"), so the zen lane forwards them to the
// keyless /inference/v1 gate with public credentials. The source of truth is
// the curated registry and -free suffix matching (providers.IsOpencodeFreeTierModel).
func isOpencodeFreeModel(cleanModel string) bool {
	base := cleanModel
	if parenIdx := strings.IndexByte(base, '('); parenIdx != -1 {
		base = base[:parenIdx]
	}
	// IsOpencodeFreeTierModel handles catalog membership and -free suffix rules,
	// keeping resolution consistent across all three lanes (free, zen, go).
	for _, provider := range []string{"opencode", "opencode-zen", "opencode-go"} {
		if providers.IsOpencodeFreeTierModel(provider, base) {
			return true
		}
	}
	return false
}

var opencodeGoMessagesModels = map[string]bool{
	"claude-haiku-5-5": true,
	"minimax-m3":       true,
	"minimax-m2.7":     true,
	"minimax-m2.5":     true,
	"qwen3.8-max":      true,
	"qwen3.8-flash":    true,
	"qwen3.7-max":      true,
	"qwen3.7-plus":     true,
	"qwen3.6-plus":     true,
}

// EnsureClaudeMessages exposes the OpenAI→Claude Messages request conversion
// (see ensureMessagesMaxTokens) for the fallback path, which forwards raw
// OpenAI-format bodies to Anthropic-native upstreams.
