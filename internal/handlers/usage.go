package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/pricing"
	"9router/proxy/internal/translator"
)

// logUsage persists a usage record and updates connection metadata.
func (h *ChatHandler) logUsage(info *UsageLogInfo, usage *translator.OpenAIUsage, latencyMs int64, requestBody []byte, metrics *streamMetrics) {
	totalTokens := usage.PromptTokens + usage.CompletionTokens
	cost := pricing.EstimateCost(info.Model, usage.PromptTokens, usage.CompletionTokens)
	metaJSON := fmt.Sprintf(`{"provider":"%s","model":"%s","connectionId":"%s"}`, info.Provider, info.Model, info.ConnectionID)

	log.Printf("[usage] provider=%s model=%s prompt=%d completion=%d cached=%d cost=%.4f", info.Provider, info.Model, usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens, cost)

	tokensJSON := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"cached_tokens":%d,"cache_creation_input_tokens":0}`, usage.PromptTokens, usage.CompletionTokens, totalTokens, usage.CachedTokens)
	if err := h.Repo.InsertUsageHistory(info.Provider, info.Model, info.ConnectionID, maskAPIKey(info.APIKey), info.Endpoint, usage.PromptTokens, usage.CompletionTokens, cost, "success", totalTokens, metaJSON, tokensJSON); err != nil {
		log.Printf("[error] component=usage err=\"insert: %v\"", err)
	}

	now := time.Now().UTC()
	reqID := fmt.Sprintf("%d-%s", now.UnixMilli(), info.Model)
	reqMsgs := extractRequestMessages(requestBody)
	var ttftMs int64
	var respContent string
	if metrics != nil {
		ttftMs = metrics.ttft
		respContent = metrics.responseBuf.String()
		if len(respContent) > constants.MaxResponseContentLen {
			respContent = respContent[:constants.MaxResponseContentLen] + "...[truncated]"
		}
	}
	reqData, _ := json.Marshal(map[string]any{
		"id": reqID, "provider": info.Provider, "model": info.Model,
		"connectionId": info.ConnectionID, "status": "success",
		"timestamp": now.Format("2006-01-02T15:04:05.000Z"),
		"latency": map[string]int64{"ttft": ttftMs, "total": latencyMs},
		"tokens": map[string]int{
			"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens,
			"cached_tokens": usage.CachedTokens, "reasoning_tokens": 0,
		},
		"request":  map[string]any{"messages": reqMsgs},
		"response": map[string]any{"content": respContent},
	})
	if err := h.Repo.InsertRequestDetail(reqID, info.Provider, info.Model, info.ConnectionID, "success", string(reqData)); err != nil {
		log.Printf("[error] component=request-detail err=\"insert: %v\"", err)
	}

	h.upsertDailyUsage(info.Provider, info.Model, info.Endpoint, info.ConnectionID, usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens, cost)
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
func (h *ChatHandler) upsertDailyUsage(provider, model, endpoint, connectionID string, promptTokens, completionTokens, cachedTokens int, cost float64) {
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
		log.Printf("[usage] failed to marshal daily usage: %v", err)
		return
	}
	if err := h.Repo.UpsertUsageDaily(dateKey, string(dataJSON)); err != nil {
		log.Printf("[usage] failed to upsert daily usage: %v", err)
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

// maskAPIKey returns a masked version of an API key for storage.
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
}
