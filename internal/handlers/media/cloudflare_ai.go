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
	"net/http"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/providers"
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

const (
	cloudflareMaxRetries    = 6
	cloudflareRetryCooldown = 5 * time.Minute
)

type cloudflareFailure struct {
	retryable bool
	quota     bool
	message   string
}

type cloudflareUpstreamResp struct {
	status      int
	contentType string
	body        []byte
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

// classifyCloudflareFailure inspects an upstream Cloudflare error response in a
// single JSON pass, tagging daily-neuron quota exhaustion (code 4006) alongside
// rate-limit, auth, and routing failures (429/401/403, or 400 with code 7003).
func classifyCloudflareFailure(status int, raw []byte) cloudflareFailure {
	var env struct {
		Errors []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	var hasQuota, hasRoute bool
	var msg string
	if json.Unmarshal(raw, &env) == nil {
		for _, e := range env.Errors {
			switch e.Code {
			case 4006:
				hasQuota = true
			case 7003:
				hasRoute = true
			}
			if msg == "" {
				msg = strings.TrimSpace(e.Message)
			}
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
		if len(msg) > 300 {
			msg = msg[:300]
		}
	}
	return cloudflareFailure{
		retryable: hasQuota ||
			status == http.StatusTooManyRequests ||
			status == http.StatusUnauthorized ||
			status == http.StatusForbidden ||
			(status == http.StatusBadRequest && hasRoute),
		quota:   hasQuota,
		message: msg,
	}
}

func cloudflareCooldownUntil(now time.Time, quota bool) time.Time {
	u := now.UTC()
	if quota {
		return time.Date(u.Year(), u.Month(), u.Day()+1, 0, 0, 0, 0, time.UTC)
	}
	return u.Add(cloudflareRetryCooldown)
}

func buildCloudflareTargetURL(providerBaseURL, connBaseURL, model string) string {
	// The run endpoint lives at <account root>/ai/run/<model>. The registry
	// BaseURL (accountId already resolved by getProviderConfig) is the chat
	// completions URL, so strip back to the account root; a custom connection
	// baseUrl (proxy prefix / test mock) is used verbatim.
	base := strings.TrimRight(providerBaseURL, "/")
	if connBaseURL != "" {
		base = strings.TrimRight(connBaseURL, "/")
	}
	base = strings.TrimSuffix(base, "/chat/completions")
	base = strings.TrimSuffix(base, "/v1")
	base = strings.TrimSuffix(base, "/ai")
	return base + "/ai/run/" + model
}

func writeCloudflareRawResponse(w http.ResponseWriter, resp *cloudflareUpstreamResp) {
	if resp.contentType != "" {
		w.Header().Set(constants.HeaderContentType, resp.contentType)
	}
	w.WriteHeader(resp.status)
	_, _ = w.Write(resp.body)
}

// handleCloudflareImage serves /v1/images/generations (and /images/edits) for
// cloudflare-ai/cf, posting to /ai/run/<model> with the model-appropriate body
// shape, rotating across active connections on quota/retryable refusals, and
// returning a normalized OpenAI images response.
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

	// Resolve reference and mask images once before rotating across accounts.
	reference := pickImage(req.Image, req.Images)
	if reference != "" && (strings.HasPrefix(reference, "http://") || strings.HasPrefix(reference, "https://")) {
		resolved, err := h.resolveImageInput(r.Context(), reference)
		if err != nil {
			return err
		}
		reference = resolved
	}

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

	pinned := modelInfo.ConnectionID
	if pinned == "" {
		pinned = imagePinnedConnectionID(r)
	}
	usePinned := pinned != ""

	excludeIDs := make([]string, 0, cloudflareMaxRetries)
	tried := make(map[string]bool, cloudflareMaxRetries)
	var lastResp *cloudflareUpstreamResp
	var lastErr error

	for attempt := 0; attempt < cloudflareMaxRetries; attempt++ {
		if ctxErr := r.Context().Err(); ctxErr != nil {
			return ctxErr
		}

		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, pinned, excludeIDs, modelInfo.Model)
		if err != nil || connData == nil {
			if lastResp != nil {
				writeCloudflareRawResponse(w, lastResp)
				return nil
			}
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("%s", mediaConnectionError(err, modelInfo.Provider))
		}
		if conn != nil {
			if tried[conn.ID] {
				break
			}
			tried[conn.ID] = true
			excludeIDs = append(excludeIDs, conn.ID)
		}

		upstream, err := h.sendCloudflareImageAttempt(r.Context(), modelInfo, connData, payload, contentType)
		if err != nil {
			lastErr = err
			if usePinned || conn == nil || r.Context().Err() != nil {
				return lastErr
			}
			continue
		}

		if upstream.status != http.StatusOK {
			failure := classifyCloudflareFailure(upstream.status, upstream.body)
			if !failure.retryable {
				writeCloudflareRawResponse(w, upstream)
				return nil
			}
			if h.Repo != nil && conn != nil && !handlerutil.IsProbeContext(r.Context()) {
				_ = h.Repo.LockConnectionRateLimit(r.Context(),
					conn.ID,
					cloudflareCooldownUntil(time.Now().UTC(), failure.quota),
					1,
					upstream.status,
					failure.message,
				)
			}
			if usePinned || conn == nil {
				writeCloudflareRawResponse(w, upstream)
				return nil
			}
			lastResp = upstream
			lastErr = fmt.Errorf("cloudflare-ai upstream returned %d: %s", upstream.status, failure.message)
			continue
		}

		normalized, err := normalizeCloudflareImageResponse(upstream.body)
		if err != nil {
			return err
		}
		w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(normalized)
		if h.Repo != nil && conn != nil {
			_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
		}
		return nil
	}

	if lastResp != nil {
		writeCloudflareRawResponse(w, lastResp)
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("no active connections for provider: %s", modelInfo.Provider)
}

func (h *MediaHandler) sendCloudflareImageAttempt(ctx context.Context, modelInfo *chat.ModelInfo, connData *chat.ConnectionData, payload []byte, contentType string) (*cloudflareUpstreamResp, error) {
	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		return nil, err
	}
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" && providerCfg != nil && providerCfg.DefaultAPIKey != "" {
		apiKey = providerCfg.DefaultAPIKey
	}
	if apiKey == "" {
		return nil, errors.New("no API key found")
	}

	connBaseURL := ""
	if connData != nil {
		connBaseURL = connData.BaseURL
	}
	targetURL := buildCloudflareTargetURL(providerCfg.BaseURL, connBaseURL, modelInfo.Model)

	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create cloudflare image request: %w", err)
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
		return nil, clientErr
	}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		return nil, fmt.Errorf("upstream cloudflare image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read cloudflare image response: %w", err)
	}

	return &cloudflareUpstreamResp{
		status:      resp.StatusCode,
		contentType: resp.Header.Get(constants.HeaderContentType),
		body:        raw,
	}, nil
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

// resolveImageInput downloads a reference image URL and returns it as a
// base64-encoded string. It uses r.Context(), requires a 200 response, and
// fetches through the vision bridge's connect-time guarded client, which
// refuses private/loopback/metadata addresses regardless of
// ALLOW_PRIVATE_NETWORKING and re-checks every redirect hop (DNS rebinding /
// redirect SSRF, OmniRoute #20475).
func (h *MediaHandler) resolveImageInput(ctx context.Context, imageURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", fmt.Errorf("create image request: %w", err)
	}
	resp, err := visionImageDownloadClient().Do(req)
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
