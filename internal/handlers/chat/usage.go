package chat

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/pricing"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/usagetracker"
)

var dailyUsageMu sync.Mutex

const maxPersistedErrorLen = 512

// LogFailure persists a bounded, credential-free diagnostic for a failed or
// aborted request. It intentionally does not touch usageHistory/daily totals.
func (h *ChatHandler) LogFailure(
	info *UsageLogInfo,
	usage *translator.OpenAIUsage,
	err error,
	latencyMs int64,
	requestBody []byte,
	metrics *streamMetrics,
) {
	if info == nil || h.Repo == nil || err == nil {
		return
	}
	if usage == nil {
		usage = &translator.OpenAIUsage{}
	}
	now := time.Now().UTC()
	reqID := fmt.Sprintf("%d-%s", now.UnixMilli(), info.Model)
	tokens := map[string]int{
		"prompt_tokens":               usage.PromptTokens,
		"completion_tokens":           usage.CompletionTokens,
		"cached_tokens":               usage.GetCachedTokens(),
		"cache_creation_input_tokens": usage.CacheCreationInputTokens,
	}
	statusCode := http.StatusBadGateway
	var upstreamErr *upstreamError
	if errors.As(err, &upstreamErr) && upstreamErr.StatusCode > 0 {
		statusCode = upstreamErr.StatusCode
	}
	if isClientCanceled(nil, err) {
		statusCode = StatusClientClosedRequest
	}
	message := extractErrorText([]byte(err.Error()))
	if message == "" {
		var upstreamErr *upstreamError
		if errors.As(err, &upstreamErr) {
			message = extractErrorText(upstreamErr.Body)
		}
	}
	if message == "" {
		message = "request failed"
	}
	message = sanitizeDetailError(message)
	var responseContent string
	if metrics != nil {
		responseContent = metrics.ResponseBuf.String()
		if len(responseContent) > constants.MaxResponseContentLen {
			responseContent = responseContent[:constants.MaxResponseContentLen] + "...[truncated]"
		}
	}
	reqData, marshalErr := json.Marshal(map[string]any{
		"id": reqID, "provider": info.Provider, "model": info.Model,
		"connectionId": info.ConnectionID, "status": "error",
		"timestamp": now.Format("2006-01-02T15:04:05.000Z"),
		"latency": map[string]int64{
			"ttft":  metricsTTFT(metrics),
			"total": latencyMs,
		},
		"tokens":   tokens,
		"request":  map[string]any{"messages": extractRequestMessages(requestBody)},
		"response": map[string]any{"error": message, "status": statusCode, "content": responseContent},
	})
	if marshalErr != nil {
		return
	}
	if insertErr := h.Repo.InsertRequestDetail(
		reqID,
		info.Provider,
		info.Model,
		info.ConnectionID,
		"error",
		string(reqData),
	); insertErr != nil {
		log.Error("usage", "insert failed request detail failed", "error", insertErr)
	}
}

func metricsTTFT(metrics *streamMetrics) int64 {
	if metrics == nil {
		return 0
	}
	return metrics.TTFT
}

func sanitizeDetailError(message string) string {
	if len(message) > maxPersistedErrorLen {
		return message[:maxPersistedErrorLen] + "...[truncated]"
	}
	return message
}

// LogUsage is the exported method to persist a usage record and update connection metadata.
func (h *ChatHandler) LogUsage(info *UsageLogInfo, usage *translator.OpenAIUsage, latencyMs int64, requestBody []byte, metrics *streamMetrics) {
	h.logUsage(info, usage, latencyMs, requestBody, metrics)
}

// logUsage persists a usage record and updates connection metadata.
func (h *ChatHandler) logUsage(info *UsageLogInfo, usage *translator.OpenAIUsage, latencyMs int64, requestBody []byte, metrics *streamMetrics) {
	if usage == nil {
		usage = &translator.OpenAIUsage{}
	}

	var ttftMs int64
	var respContent string
	if metrics != nil {
		ttftMs = metrics.TTFT
		respContent = metrics.ResponseBuf.String()
		if len(respContent) > constants.MaxResponseContentLen {
			respContent = respContent[:constants.MaxResponseContentLen] + "...[truncated]"
		}
	}

	// Fallback token estimation if upstream emitted zero tokens
	if usage.PromptTokens == 0 && len(requestBody) > 0 {
		usage.PromptTokens = CountValueChars(requestBody) / 4
		if usage.PromptTokens == 0 {
			usage.PromptTokens = 1
		}
	}
	if usage.CompletionTokens == 0 && len(respContent) > 0 {
		usage.CompletionTokens = CountValueChars(respContent) / 4
		if usage.CompletionTokens == 0 {
			usage.CompletionTokens = 1
		}
	}

	totalTokens := usage.PromptTokens + usage.CompletionTokens
	cachedTokens := usage.GetCachedTokens()
	cacheCreationTokens := usage.CacheCreationInputTokens

	// prompt_tokens is cache-inclusive, so the cached and cache-creation counts
	// are handed over too: the formula takes them out of the input rate and
	// charges them at their own, cheaper rate. The provider matters because
	// gateways can price the same model differently.
	cost := pricing.EstimateCost(info.Provider, info.Model, pricing.TokenCounts{
		PromptTokens:        usage.PromptTokens,
		CompletionTokens:    usage.CompletionTokens,
		CachedTokens:        cachedTokens,
		CacheCreationTokens: cacheCreationTokens,
		ReasoningTokens:     usage.ReasoningTokens(),
	})
	metaJSON := fmt.Sprintf(`{"provider":"%s","model":"%s","connectionId":"%s"}`, info.Provider, info.Model, info.ConnectionID)

	log.Info("usage", "logged", append([]any{
		"provider", info.Provider, "model", info.Model,
		"prompt", usage.PromptTokens, "completion", usage.CompletionTokens, "cached", cachedTokens,
		"cache_creation", cacheCreationTokens, "ttft_ms", ttftMs, "latency_ms", latencyMs, "cost", cost,
	}, info.ConnIdentityKV()...)...)

	tokensJSON := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"cached_tokens":%d,"cache_creation_input_tokens":%d}`, usage.PromptTokens, usage.CompletionTokens, totalTokens, cachedTokens, cacheCreationTokens)
	if err := h.Repo.InsertUsageHistory(info.Provider, info.Model, info.ConnectionID, maskAPIKey(info.APIKey), info.Endpoint, usage.PromptTokens, usage.CompletionTokens, cost, "success", totalTokens, metaJSON, tokensJSON); err != nil {
		log.Error("usage", "insert failed", "error", err)
	}

	now := time.Now().UTC()
	reqID := fmt.Sprintf("%d-%s", now.UnixMilli(), info.Model)
	reqMsgs := extractRequestMessages(requestBody)

	reqData, err := json.Marshal(map[string]any{
		"id": reqID, "provider": info.Provider, "model": info.Model,
		"connectionId": info.ConnectionID, "status": "success",
		"timestamp": now.Format("2006-01-02T15:04:05.000Z"),
		"latency":   map[string]int64{"ttft": ttftMs, "total": latencyMs},
		"tokens": map[string]int{
			"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens,
			"cached_tokens": cachedTokens, "cache_creation_input_tokens": cacheCreationTokens,
			"reasoning_tokens": usage.ReasoningTokens(),
		},
		"request":  map[string]any{"messages": reqMsgs},
		"response": map[string]any{"content": respContent},
	})
	if err != nil {
		log.Error("usage", "marshal request detail failed", "error", err)
		return
	}
	if err := h.Repo.InsertRequestDetail(reqID, info.Provider, info.Model, info.ConnectionID, "success", string(reqData)); err != nil {
		log.Error("usage", "insert request detail failed", "error", err)
	}

	h.upsertDailyUsage(info.Provider, info.Model, info.Endpoint, info.ConnectionID, info.APIKey, usage.PromptTokens, usage.CompletionTokens, cachedTokens, cost)

	usagetracker.GetTracker().PushRecent(usagetracker.RecentRequest{
		Timestamp:        now.Format(time.RFC3339),
		Model:            info.Model,
		Provider:         info.Provider,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CachedTokens:     cachedTokens,
		Status:           "ok",
	}, h.Repo)
}

// extractRequestMessages extracts truncated messages from the request body for logging.
func extractRequestMessages(body []byte) []map[string]string {
	var req translator.OpenAIRequest
	if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
		return nil
	}
	msgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		content := extractContent(m.Content)
		if len(content) > constants.MaxMessageContentLen {
			content = content[:constants.MaxMessageContentLen] + "..."
		}
		msgs = append(msgs, map[string]string{"role": m.Role, "content": content})
	}
	if len(msgs) > constants.MaxLoggedMessages {
		msgs = msgs[len(msgs)-constants.MaxLoggedMessages:]
	}
	return msgs
}

// extractContent handles content that is either a string or array of content blocks.
func extractContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, block := range v {
			if m, ok := block.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return fmt.Sprintf("%v", content)
	}
}

// upsertDailyUsage reads the existing daily aggregate, merges new tokens, and upserts.
func (h *ChatHandler) upsertDailyUsage(provider, model, endpoint, connectionID, apiKey string, promptTokens, completionTokens, cachedTokens int, cost float64) {
	dailyUsageMu.Lock()
	defer dailyUsageMu.Unlock()

	dateKey := time.Now().UTC().Format("2006-01-02")
	existing, _ := h.Repo.GetUsageDaily(dateKey)

	data := parseDailyData(existing)

	data["requests"] = getJSONInt(data, "requests") + 1
	data["promptTokens"] = getJSONInt(data, "promptTokens") + promptTokens
	data["completionTokens"] = getJSONInt(data, "completionTokens") + completionTokens
	data["cachedTokens"] = getJSONInt(data, "cachedTokens") + cachedTokens
	data["cost"] = getJSONFloat(data, "cost") + cost

	data["totalPromptTokens"] = getJSONInt(data, "totalPromptTokens") + promptTokens
	data["totalCompletionTokens"] = getJSONInt(data, "totalCompletionTokens") + completionTokens
	data["totalRequests"] = getJSONInt(data, "totalRequests") + 1

	// byProvider
	byProvider := getJSONMap(data, "byProvider")
	pp := getJSONMap(byProvider, provider)
	pp["requests"] = getJSONInt(pp, "requests") + 1
	pp["promptTokens"] = getJSONInt(pp, "promptTokens") + promptTokens
	pp["completionTokens"] = getJSONInt(pp, "completionTokens") + completionTokens
	pp["cachedTokens"] = getJSONInt(pp, "cachedTokens") + cachedTokens
	pp["cost"] = getJSONFloat(pp, "cost") + cost
	byProvider[provider] = pp
	data["byProvider"] = byProvider

	// byModel
	modelKey := model + "|" + provider
	byModel := getJSONMap(data, "byModel")
	mm := getJSONMap(byModel, modelKey)
	mm["requests"] = getJSONInt(mm, "requests") + 1
	mm["promptTokens"] = getJSONInt(mm, "promptTokens") + promptTokens
	mm["completionTokens"] = getJSONInt(mm, "completionTokens") + completionTokens
	mm["cachedTokens"] = getJSONInt(mm, "cachedTokens") + cachedTokens
	mm["cost"] = getJSONFloat(mm, "cost") + cost
	if mm["rawModel"] == nil {
		mm["rawModel"] = model
	}
	if mm["provider"] == nil {
		mm["provider"] = provider
	}
	byModel[modelKey] = mm
	data["byModel"] = byModel

	// byApiKey
	apiKeyVal := maskAPIKey(apiKey)
	if apiKeyVal == "" {
		apiKeyVal = "local-no-key"
	}
	akKey := apiKeyVal + "|" + model + "|" + provider
	byApiKey := getJSONMap(data, "byApiKey")
	ak := getJSONMap(byApiKey, akKey)
	ak["requests"] = getJSONInt(ak, "requests") + 1
	ak["promptTokens"] = getJSONInt(ak, "promptTokens") + promptTokens
	ak["completionTokens"] = getJSONInt(ak, "completionTokens") + completionTokens
	ak["cachedTokens"] = getJSONInt(ak, "cachedTokens") + cachedTokens
	ak["cost"] = getJSONFloat(ak, "cost") + cost
	ak["rawModel"] = model
	ak["provider"] = provider
	ak["apiKey"] = apiKeyVal
	byApiKey[akKey] = ak
	data["byApiKey"] = byApiKey

	// byEndpoint
	epKey := endpoint + "|" + model + "|" + provider
	byEndpoint := getJSONMap(data, "byEndpoint")
	ep := getJSONMap(byEndpoint, epKey)
	ep["requests"] = getJSONInt(ep, "requests") + 1
	ep["promptTokens"] = getJSONInt(ep, "promptTokens") + promptTokens
	ep["completionTokens"] = getJSONInt(ep, "completionTokens") + completionTokens
	ep["cachedTokens"] = getJSONInt(ep, "cachedTokens") + cachedTokens
	ep["cost"] = getJSONFloat(ep, "cost") + cost
	if ep["endpoint"] == nil {
		ep["endpoint"] = endpoint
	}
	if ep["rawModel"] == nil {
		ep["rawModel"] = model
	}
	if ep["provider"] == nil {
		ep["provider"] = provider
	}
	byEndpoint[epKey] = ep
	data["byEndpoint"] = byEndpoint

	// byAccount
	if connectionID != "" {
		byAccount := getJSONMap(data, "byAccount")
		acc := getJSONMap(byAccount, connectionID)
		acc["requests"] = getJSONInt(acc, "requests") + 1
		acc["promptTokens"] = getJSONInt(acc, "promptTokens") + promptTokens
		acc["completionTokens"] = getJSONInt(acc, "completionTokens") + completionTokens
		acc["cachedTokens"] = getJSONInt(acc, "cachedTokens") + cachedTokens
		acc["cost"] = getJSONFloat(acc, "cost") + cost
		if acc["rawModel"] == nil {
			acc["rawModel"] = model
		}
		if acc["provider"] == nil {
			acc["provider"] = provider
		}
		byAccount[connectionID] = acc
		data["byAccount"] = byAccount
	}

	// providers (legacy nested format)
	providers := getJSONMap(data, "providers")
	pd := getJSONMap(providers, provider)
	pd["promptTokens"] = getJSONInt(pd, "promptTokens") + promptTokens
	pd["completionTokens"] = getJSONInt(pd, "completionTokens") + completionTokens
	pd["requests"] = getJSONInt(pd, "requests") + 1

	models := getJSONMap(pd, "models")
	md := getJSONMap(models, model)
	md["promptTokens"] = getJSONInt(md, "promptTokens") + promptTokens
	md["completionTokens"] = getJSONInt(md, "completionTokens") + completionTokens
	md["requests"] = getJSONInt(md, "requests") + 1
	models[model] = md
	pd["models"] = models
	providers[provider] = pd
	data["providers"] = providers

	dataJSON, err := json.Marshal(data)
	if err != nil {
		log.Error("usage", "marshal daily usage failed", "error", err)
		return
	}
	if err := h.Repo.UpsertUsageDaily(dateKey, string(dataJSON)); err != nil {
		log.Error("usage", "upsert daily usage failed", "error", err)
	}
}

// parseDailyData parses an existing daily JSON string into a mutable map.
func parseDailyData(raw string) map[string]any {
	if raw == "" {
		return make(map[string]any)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m == nil {
		return make(map[string]any)
	}
	return m
}

// getJSONInt extracts an int from a JSON map (stored as float64 after unmarshal).
func getJSONInt(m map[string]any, key string) int {
	if v, ok := m[key]; ok {
		if n, ok := v.(float64); ok {
			return int(n)
		}
	}
	return 0
}

func getJSONFloat(m map[string]any, key string) float64 {
	if v, ok := m[key]; ok {
		if n, ok := v.(float64); ok {
			return n
		}
	}
	return 0
}

// getJSONMap extracts or creates a nested map from a JSON map.
func getJSONMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if sub, ok := v.(map[string]any); ok {
			return sub
		}
	}
	return make(map[string]any)
}

// maskAPIKey returns a masked version of an API key for storage. The canonical
// implementation lives in handlerutil so the usage-stats reader can derive the
// same mask from a stored row to recover the key's name.
func maskAPIKey(key string) string {
	return handlerutil.MaskAPIKey(key)
}
