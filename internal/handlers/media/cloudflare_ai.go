package media

// Cloudflare Workers AI image lane (upstream parity: decolua/9router
// open-sse/handlers/imageProviders/cloudflareAi.js). Every cloudflare-ai/cf
// image model answers POST /accounts/<accountId>/ai/run/<model>, never the
// OpenAI-shaped /ai/v1/images/generations the generic forwarder dials — that
// was the "No route for that URI" 7000/7003 regression on flux-2-klein-9b
// (issue #864). FLUX.2 models require multipart/form-data even for text-only
// prompts; older text-to-image models take a JSON body. Response is normalized
// to the OpenAI images shape the client already expects.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// cloudflareMultipartModels are the FLUX.2 ids that reject JSON and require a
// multipart/form-data body (even when no reference image is supplied).
var cloudflareMultipartModels = map[string]bool{
	"@cf/black-forest-labs/flux-2-dev":      true,
	"@cf/black-forest-labs/flux-2-klein-4b": true,
	"@cf/black-forest-labs/flux-2-klein-9b": true,
}

// cloudflareOptionalFields ride through to the run endpoint verbatim when
// present and non-empty, mirroring upstream's OPTIONAL_FIELDS list.
var cloudflareOptionalFields = []string{
	"negative_prompt",
	"guidance",
	"seed",
	"num_steps",
	"steps",
	"strength",
}

// isCloudflareImageModel reports whether info resolves to the cloudflare-ai
// provider on an image endpoint.
func isCloudflareImageModel(info *chat.ModelInfo) bool {
	if info == nil {
		return false
	}
	return providers.ResolveAlias(info.Provider) == "cloudflare-ai" ||
		providers.ResolveAlias(info.Provider) == "cf"
}

// handleCloudflareImage serves /v1/images/generations (and /images/edits) for
// cloudflare-ai/cf, posting to /ai/run/<model> with the model-appropriate body
// shape and returning a normalized OpenAI images response.
func (h *MediaHandler) handleCloudflareImage(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var req struct {
		Prompt    string   `json:"prompt"`
		Size      string   `json:"size"`
		Width     any      `json:"width"`
		Height    any      `json:"height"`
		Image     string   `json:"image"`
		Images    []string `json:"images"`
		MaskImage string   `json:"mask_image"`
		Mask      string   `json:"mask"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return fmt.Errorf("parse cloudflare image body: %w", err)
	}

	// Resolve reference image (URL → base64).
	reference := pickImage(req.Image, req.Images)
	if reference != "" && (strings.HasPrefix(reference, "http://") || strings.HasPrefix(reference, "https://")) {
		resolved, err := h.resolveImageInput(r.Context(), reference)
		if err != nil {
			return err
		}
		reference = resolved
	}

	// Resolve mask image.
	maskStr := pickImage(req.MaskImage, nil)
	if req.Mask != "" {
		maskStr = req.Mask
	}
	if maskStr != "" && (strings.HasPrefix(maskStr, "http://") || strings.HasPrefix(maskStr, "https://")) {
		resolved, err := h.resolveImageInput(r.Context(), maskStr)
		if err != nil {
			return err
		}
		maskStr = resolved
	}

	// Parse width/height from "size" (WxH) or explicit fields.
	width, height := cloudflareDimensions(req.Size, req.Width, req.Height)
	// Cloudflare's FLUX.1 Schnell schema is closed: width/height/size reject
	// the request (upstream #5373). FLUX.2 and Stable Diffusion accept them.
	if strings.HasSuffix(modelInfo.Model, "flux-1-schnell") {
		width, height = 0, 0
	}

	var payload []byte
	var err error
	contentType := constants.ContentTypeJSON
	if cloudflareMultipartModels[modelInfo.Model] {
		payload, contentType, err = buildCloudflareMultipart(req.Prompt, width, height, reference, maskStr, body)
		if err != nil {
			return err
		}
	} else {
		payload, err = buildCloudflareJSON(req.Prompt, width, height, reference, maskStr, body)
		if err != nil {
			return err
		}
	}

	// Pinned connections do not rotate: the caller (usually a combo member)
	// owns the fallback walk, and forcing a different account under a pin would
	// violate the pin. Mirrors handleAccountFallback's pinned branch.
	pinned := modelInfo.ConnectionID != ""

	var excludeIDs []string
	tried := map[string]bool{}
	var lastErr error

	for attempt := 0; attempt < cloudflareMaxRetries; attempt++ {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, modelInfo.ConnectionID, excludeIDs, modelInfo.Model)
		if err != nil || connData == nil {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("%s", mediaConnectionError(err, modelInfo.Provider))
		}
		if tried[conn.ID] {
			break
		}
		tried[conn.ID] = true

		providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
		if err != nil {
			if pinned {
				return err
			}
			continue
		}
		apiKey := chat.ExtractAPIKey(connData)
		if apiKey == "" && providerCfg != nil && providerCfg.DefaultAPIKey != "" {
			apiKey = providerCfg.DefaultAPIKey
		}
		if apiKey == "" {
			if pinned {
				return errors.New("no API key found")
			}
			continue
		}

		// The run endpoint lives at <account root>/ai/run/<model>, so it must
		// rebuild per connection: each account carries its own accountId. The
		// registry BaseURL (accountId already resolved by getProviderConfig) is
		// the chat completions URL, so strip back to the account root; a custom
		// connection baseUrl (proxy prefix / test mock) is used verbatim.
		base := strings.TrimRight(providerCfg.BaseURL, "/")
		if connData.BaseURL != "" {
			base = strings.TrimRight(connData.BaseURL, "/")
		}
		base = strings.TrimSuffix(base, "/chat/completions")
		base = strings.TrimSuffix(base, "/v1")
		base = strings.TrimSuffix(base, "/ai")
		targetURL := base + "/ai/run/" + modelInfo.Model

		upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("create cloudflare image request: %w", err)
		}
		upstreamReq.Header.Set(constants.HeaderContentType, contentType)
		handlerutil.SetAuthHeader(upstreamReq, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)
		for k, v := range providerCfg.StaticHeaders {
			if upstreamReq.Header.Get(k) == "" {
				upstreamReq.Header.Set(k, v)
			}
		}

		client, clientErr := h.ChatH.GetClientForConnection(connData)
		if clientErr != nil {
			if pinned {
				return clientErr
			}
			continue
		}
		resp, err := client.Do(upstreamReq)
		if err != nil {
			if pinned {
				return fmt.Errorf("upstream cloudflare image: %w", err)
			}
			lastErr = fmt.Errorf("upstream cloudflare image: %w", err)
			excludeIDs = append(excludeIDs, conn.ID)
			continue
		}

		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK && rerr == nil {
			normalized, nerr := normalizeCloudflareImageResponse(raw)
			if nerr != nil {
				return nerr
			}
			w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(normalized)
			if conn != nil && h.Repo != nil {
				_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
			}
			return nil
		}

		if pinned {
			w.Header().Set(constants.HeaderContentType, resp.Header.Get(constants.HeaderContentType))
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(raw)
			return nil
		}

		// Account-scoped refusal: park the account so this request (and future
		// ones) skip it, then walk to the next connection.
		if isCloudflareRetryableStatus(resp.StatusCode, raw) {
			lastErr = fmt.Errorf("upstream cloudflare image returned %d: %s", resp.StatusCode, truncateBodyMessage(raw))
			excludeIDs = append(excludeIDs, conn.ID)
			h.parkCloudflareAccount(conn.ID, resp.StatusCode, raw)
			continue
		}

		// Non-retryable upstream failure: surface it to the client untouched.
		w.Header().Set(constants.HeaderContentType, resp.Header.Get(constants.HeaderContentType))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(raw)
		return nil
	}

	if lastErr != nil {
		return lastErr
	}
	return errors.New("no available connections for provider: " + modelInfo.Provider)
}

// pickImage returns the first non-empty image string from the options.
func pickImage(image string, images []string) string {
	if strings.TrimSpace(image) != "" {
		return strings.TrimSpace(image)
	}
	for _, im := range images {
		if strings.TrimSpace(im) != "" {
			return strings.TrimSpace(im)
		}
	}
	return ""
}

// cloudflareDimensions extracts width/height: first from "size" ("WxH"), then
// from explicit width/height fields, mirroring upstream's getDimensions.
func cloudflareDimensions(size string, w, h any) (int, int) {
	if size != "" {
		if parts := strings.Split(size, "x"); len(parts) == 2 {
			if wi, err := strconv.Atoi(parts[0]); err == nil {
				if hi, err := strconv.Atoi(parts[1]); err == nil {
					return wi, hi
				}
			}
		}
	}
	if wi, ok := toInt(w); ok {
		if hi, ok := toInt(h); ok {
			return wi, hi
		}
	}
	return 0, 0
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

// buildCloudflareJSON builds a JSON body for the older (non-FLUX.2) models,
// matching upstream's buildJsonBody: {prompt, width?, height?, image_b64?,
// mask_b64?, optional fields...}.
func buildCloudflareJSON(prompt string, width, height int, reference, mask string, originalBody []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(originalBody, &raw); err != nil {
		return nil, err
	}

	req := map[string]any{"prompt": prompt}
	if width > 0 {
		req["width"] = width
	}
	if height > 0 {
		req["height"] = height
	}
	if reference != "" {
		req["image_b64"] = reference
	}
	if mask != "" {
		req["mask_b64"] = mask
	}
	for _, key := range cloudflareOptionalFields {
		if v, ok := raw[key]; ok && v != nil && v != "" {
			req[key] = v
		}
	}
	return json.Marshal(req)
}

// buildCloudflareMultipart builds a multipart/form-data body for FLUX.2 models,
// matching upstream's buildMultipartBody: prompt + dimensions + optional fields
// as named parts; image and mask as base64 parts.
func buildCloudflareMultipart(prompt string, width, height int, reference, mask string, originalBody []byte) ([]byte, string, error) {
	var raw map[string]any
	if err := json.Unmarshal(originalBody, &raw); err != nil {
		return nil, "", err
	}

	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)

	_ = mw.WriteField("prompt", prompt)
	if width > 0 {
		_ = mw.WriteField("width", strconv.Itoa(width))
	}
	if height > 0 {
		_ = mw.WriteField("height", strconv.Itoa(height))
	}
	for _, key := range cloudflareOptionalFields {
		if v, ok := raw[key]; ok && v != nil && v != "" {
			_ = mw.WriteField(key, fmt.Sprintf("%v", v))
		}
	}
	if reference != "" {
		_ = mw.WriteField("image_b64", reference)
	}
	if mask != "" {
		_ = mw.WriteField("mask_b64", mask)
	}
	_ = mw.Close()
	return buf.Bytes(), mw.FormDataContentType(), nil
}

// normalizeCloudflareImageResponse converts Cloudflare's /ai/run/<model> response
// to the OpenAI images shape {created, data:[{b64_json}|{url}]}.
//
// Cloudflare can return:
//   - raw binary image (Content-Type: image/png) from some models
//   - JSON {result:{image:"<b64>"}} (the standard run endpoint shape)
//   - JSON {result:{data:[{b64_json}]}}  (nested data array)
//   - JSON {result:"<url>"}              (already a URL)
func normalizeCloudflareImageResponse(raw []byte) ([]byte, error) {
	// Already in the OpenAI images shape {created, data:[...]}? Pass through.
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err == nil {
		if _, ok := top["created"]; ok {
			if _, ok := top["data"].([]any); ok {
				return raw, nil
			}
		}
	}

	var wrapper struct {
		Result any `json:"result"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		// Raw binary: treat raw bytes as a PNG image.
		return openAIImageFromBinary(raw)
	}

	result := wrapper.Result
	created := int(time.Now().Unix())

	// result may itself be wrapped in another "result" (queued responses).
	for {
		if m, ok := result.(map[string]any); ok {
			if inner, hasResult := m["result"].(map[string]any); hasResult {
				result = inner
				continue
			}
			if arr, ok := m["result"].([]any); ok && len(arr) > 0 {
				result = arr
				continue
			}
		}
		break
	}

	switch r := result.(type) {
	case map[string]any:
		// Standard run shape: {image:"<b64>"} or {data:[{b64_json}]}.
		if b64, ok := r["image"].(string); ok && b64 != "" {
			return json.Marshal(map[string]any{
				"created": created,
				"data":    []map[string]string{{"b64_json": b64}},
			})
		}
		if data, ok := r["data"].([]any); ok {
			items := make([]map[string]string, 0, len(data))
			for _, item := range data {
				if m, ok := item.(map[string]any); ok {
					entry := map[string]string{}
					if b64, ok := m["b64_json"].(string); ok {
						entry["b64_json"] = b64
					}
					if url, ok := m["url"].(string); ok {
						entry["url"] = url
					}
					if len(entry) > 0 {
						items = append(items, entry)
					}
				}
			}
			return json.Marshal(map[string]any{"created": created, "data": items})
		}

	case string:
		// result is already a URL.
		return json.Marshal(map[string]any{
			"created": created,
			"data":    []map[string]string{{"url": r}},
		})
	}

	// Fallback: treat the whole thing as a potential base64 image.
	return openAIImageFromBinary(raw)
}

// openAIImageFromBinary treats raw bytes as a PNG image and returns a
// normalized OpenAI images response wrapping it as b64_json.
func openAIImageFromBinary(data []byte) ([]byte, error) {
	b64 := base64.StdEncoding.EncodeToString(data)
	return json.Marshal(map[string]any{
		"created": int(time.Now().Unix()),
		"data":    []map[string]string{{"b64_json": b64}},
	})
}

// cloudflareMaxRetries bounds the rotation walk so an operator with many
// cloudflare connections cannot turn one image request into a long tail of
// upstream calls; the walk terminates early once every account is excluded.
const cloudflareMaxRetries = 6

// cloudflareQuotaCooldown parks a drained daily-free-allocation account until
// the next UTC midnight, which is when Cloudflare resets the 10k-neuron budget.
func cloudflareQuotaCooldown(now time.Time) time.Time {
	next := now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	return next
}

// isCloudflareRetryableStatus reports whether a Cloudflare run response is an
// account-scoped refusal worth walking to the next connection: quota exhausted
// (code 4006), rate limiting (429), or an auth/forbidden refusal (401/403).
func isCloudflareRetryableStatus(status int, raw []byte) bool {
	if status == http.StatusTooManyRequests || status == http.StatusUnauthorized || status == http.StatusForbidden {
		return true
	}
	if status != http.StatusBadRequest {
		return false
	}
	var env struct {
		Errors []struct {
			Code int `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	for _, e := range env.Errors {
		if e.Code == 4006 || e.Code == 7003 {
			return true
		}
	}
	return false
}

// parkCloudflareAccount records an account-scoped refusal on the connection so
// the selector skips it until the quota resets (or a short window for non-quota
// refusals). Mirrors chat's divaConnectRateLimiter / LockConnectionRateLimit.
func (h *MediaHandler) parkCloudflareAccount(connID string, status int, raw []byte) {
	if h.Repo == nil {
		return
	}
	now := time.Now().UTC()
	until := now.Add(5 * time.Minute)
	if status == http.StatusBadRequest && containsCloudflareQuotaCode(raw) {
		until = cloudflareQuotaCooldown(now)
	}
	_ = h.Repo.LockConnectionRateLimit(connID, until, 1, status, truncateBodyMessage(raw))
}

// containsCloudflareQuotaCode reports whether the body carries Cloudflare's
// daily-allocation-exhausted code (4006).
func containsCloudflareQuotaCode(raw []byte) bool {
	var env struct {
		Errors []struct {
			Code int `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return false
	}
	for _, e := range env.Errors {
		if e.Code == 4006 {
			return true
		}
	}
	return false
}

// truncateBodyMessage returns the first Cloudflare error message from the body,
// or a compact status summary, for the connection error log.
func truncateBodyMessage(raw []byte) string {
	if len(raw) == 0 {
		return "empty upstream response"
	}
	var env struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &env); err == nil {
		if len(env.Errors) > 0 && env.Errors[0].Message != "" {
			msg := env.Errors[0].Message
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return msg
		}
	}
	if len(raw) > 200 {
		return string(raw[:200])
	}
	return string(raw)
}

// resolveImageInput downloads a reference image URL and returns it as a
// base64-encoded string. It uses r.Context(), requires a 200 response, and
// refuses private/resolved IPs via proxy.IsPrivateIP (SSRF protection).
func (h *MediaHandler) resolveImageInput(ctx context.Context, imageURL string) (string, error) {
	u, err := url.Parse(imageURL)
	if err != nil {
		return "", fmt.Errorf("parse image url: %w", err)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if ip == nil {
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return "", fmt.Errorf("resolve image host %q: %w", host, err)
		}
		ip = ips[0]
	}
	if !proxy.IsPrivateNetworkingAllowed() && proxy.IsPrivateIP(ip) {
		return "", fmt.Errorf("image host %q resolves to private IP %s", host, ip)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", fmt.Errorf("create image request: %w", err)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("image fetch returned status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}
