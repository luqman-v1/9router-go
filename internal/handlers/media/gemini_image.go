package media

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// Gemini's native generateContent image lane — port of upstream
// imageProviders/gemini.js (161e3493). The generic /v1/images/generations
// forward is the OpenAI shape, which the Google AI endpoint does not serve:
// image models answer `models/<model>:generateContent` with a contents/parts
// body and return inlineData parts.

// geminiImageInputRef matches a data URI; a long base64-looking string that is
// not a URL is treated as raw image bytes (PNG assumption, as upstream does).
var geminiImageDataURI = regexp.MustCompile(`^data:(image/[^;]+);base64,(.+)$`)

// resolveGeminiImageInput converts one reference image into an inlineData part.
func resolveGeminiImageInput(input string) map[string]any {
	if input == "" {
		return nil
	}
	if m := geminiImageDataURI.FindStringSubmatch(input); m != nil {
		return map[string]any{"inlineData": map[string]string{"mimeType": m[1], "data": m[2]}}
	}
	if !strings.HasPrefix(input, "http") && len(input) > 100 {
		return map[string]any{"inlineData": map[string]string{"mimeType": "image/png", "data": input}}
	}
	return nil
}

func (h *MediaHandler) handleGeminiImage(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	if handlerutil.GetServiceKind(r.Context()) == "" {
		r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "image"))
	}
	var reqBody struct {
		Prompt string   `json:"prompt"`
		Model  string   `json:"model"`
		Image  string   `json:"image"`
		Images []string `json:"images"`
	}
	_ = json.Unmarshal(body, &reqBody)
	prompt := strings.TrimSpace(reqBody.Prompt)
	if prompt == "" {
		return fmt.Errorf("missing required field: prompt")
	}
	model := modelInfo.Model
	if model == "" || model == "gemini" || model == "image" {
		model = "gemini-3.1-flash-image"
	}

	parts := []any{}
	for _, ref := range append(append([]string{}, reqBody.Images...), reqBody.Image) {
		if inline := resolveGeminiImageInput(ref); inline != nil {
			parts = append(parts, inline)
		}
	}
	parts = append(parts, map[string]string{"text": prompt})
	payload, err := json.Marshal(map[string]any{
		"contents":         []any{map[string]any{"parts": parts}},
		"generationConfig": map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}},
	})
	if err != nil {
		return fmt.Errorf("build gemini image request: %w", err)
	}

	pinned := modelInfo.ConnectionID
	if pinned == "" {
		pinned = imagePinnedConnectionID(r)
	}
	usePinned := pinned != ""
	excludeIDs := []string{}
	tried := make(map[string]struct{})
	var lastErr error
	for {
		if r.Context().Err() != nil {
			if lastErr != nil {
				return lastErr
			}
			return r.Context().Err()
		}
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), "gemini", pinned, excludeIDs, model)
		if err != nil || conn == nil || connData == nil {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("no active connection for gemini: %w", err)
		}
		if _, seen := tried[conn.ID]; seen {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("no active connection for gemini: all connections already tried")
		}
		tried[conn.ID] = struct{}{}

		retryable, attemptErr := h.tryGeminiImageConn(w, r, model, prompt, payload, conn, connData)
		if attemptErr == nil {
			return nil
		}
		lastErr = attemptErr
		if usePinned || !retryable || r.Context().Err() != nil {
			return lastErr
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func (h *MediaHandler) tryGeminiImageConn(
	w http.ResponseWriter,
	r *http.Request,
	model string,
	prompt string,
	payload []byte,
	conn *models.ProviderConnection,
	connData *chat.ConnectionData,
) (bool, error) {
	apiKey := chat.ExtractAPIKey(connData)
	providerCfg, err := h.ChatH.GetProviderConfig("gemini", connData)
	if err != nil {
		return false, fmt.Errorf("get gemini config: %w", err)
	}

	baseURL := strings.TrimRight(providerCfg.BaseURL, "/")
	if strings.TrimSpace(connData.BaseURL) != "" {
		baseURL = strings.TrimRight(strings.TrimSpace(connData.BaseURL), "/")
	}
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
	}
	// The chat base is the OpenAI-compat surface; the image lane is the
	// native one two segments up.
	baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	baseURL = strings.TrimSuffix(baseURL, "/openai")
	targetURL := fmt.Sprintf("%s/models/%s:generateContent", baseURL, url.PathEscape(model))

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	handlerutil.SetAuthHeader(httpReq, apiKey, providerCfg.AuthHeader, providerCfg.AuthScheme)

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return true, clientErr
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return r.Context().Err() == nil, fmt.Errorf("gemini image request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return r.Context().Err() == nil, fmt.Errorf("read gemini image response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errText := string(respBody[:min(500, len(respBody))])
		log.Warn("media", "gemini image error", "status", resp.StatusCode, "body", errText)
		backoff := 0
		if h.Repo != nil {
			backoff = h.Repo.GetConnectionBackoffLevel(conn.ID)
		}
		classification := providers.ClassifyError(resp.StatusCode, errText, backoff, respBody)
		resetSec, hasReset := geminiQuotaResetSeconds(respBody)
		shouldRotate := classification.ShouldFallback || hasReset
		if shouldRotate && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			if hasReset {
				cooldownSec = int(resetSec)
			}
			_ = h.Repo.LockConnectionModel(conn.ID, model, cooldownSec, classification.NewBackoffLevel)
		}
		return shouldRotate, fmt.Errorf("gemini image failed with status %d: %s", resp.StatusCode, string(respBody[:min(300, len(respBody))]))
	}

	formatted, err := geminiImageResponseBody(respBody, prompt)
	if err != nil {
		return false, fmt.Errorf("format gemini image response: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(formatted)

	if h.Repo != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(conn.ID, model)
		}
	}
	return false, nil
}

// geminiQuotaResetSeconds extracts a cooldown from Google's
// error.details[].metadata.quotaResetTimeStamp — the reset time the API
// reports for an exhausted image quota, which pins the lock far more
// accurately than the exponential backoff would (upstream 161e3493).
func geminiQuotaResetSeconds(body []byte) (int64, bool) {
	var parsed struct {
		Error struct {
			Details []struct {
				Metadata map[string]string `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, false
	}
	now := time.Now()
	for _, d := range parsed.Error.Details {
		raw := d.Metadata["quotaResetTimeStamp"]
		if raw == "" {
			continue
		}
		reset, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			continue
		}
		if reset.After(now) {
			return int64(reset.Sub(now).Seconds()) + 1, true
		}
	}
	return 0, false
}

// geminiImageResponseBody converts the native response into the OpenAI image
// shape: every inlineData part becomes a b64_json image.
func geminiImageResponseBody(body []byte, prompt string) ([]byte, error) {
	var parsed struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData *struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	images := []map[string]string{}
	for _, c := range parsed.Candidates {
		for _, p := range c.Content.Parts {
			if p.InlineData != nil && p.InlineData.Data != "" {
				images = append(images, map[string]string{"b64_json": p.InlineData.Data})
			}
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("gemini returned no image")
	}
	return json.Marshal(map[string]any{
		"created":       time.Now().Unix(),
		"prompt":        prompt,
		"data":          images,
		"output_format": "png",
	})
}
