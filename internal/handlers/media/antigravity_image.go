package media

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/usagetracker"
)

// handleAntigravityImage handles image generation requests for Antigravity,
// executing via CloudCode's v1internal:generateContent and translating the response to OpenAI image format.
func (h *MediaHandler) handleAntigravityImage(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var reqBody struct {
		Prompt string   `json:"prompt"`
		Size   string   `json:"size"`
		Model  string   `json:"model"`
		Image  string   `json:"image"`
		Images []string `json:"images"`
	}
	_ = json.Unmarshal(body, &reqBody)
	prompt := strings.TrimSpace(reqBody.Prompt)
	if prompt == "" {
		return fmt.Errorf("Missing required field: prompt")
	}

	model := modelInfo.Model
	if model == "" || model == "antigravity" || model == "image" {
		model = "gemini-3.1-flash-image"
	}
	if reqBody.Size != "" && reqBody.Size != "auto" {
		size := strings.Replace(reqBody.Size, ":", "x", -1)
		if !strings.Contains(model, size) {
			model = model + "-" + size
		}
	}
	cleanModel, aspectRatio := translator.ParseImageConfig(model)

	// Upstream parity (imageGeneration.js credential loop): rotate through every
	// active account. The UI-pinned x-connection-id travels via modelInfo only
	// on the combo path; the direct path resolves it from the request header.
	pinned := modelInfo.ConnectionID
	if pinned == "" {
		pinned = imagePinnedConnectionID(r)
	}
	usePinned := pinned != ""
	excludeIDs := []string{}
	var lastErr error
	for {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), "antigravity", pinned, excludeIDs, cleanModel)
		if err != nil || conn == nil {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("no active connection for antigravity: %w", err)
		}
		if attemptErr := h.tryAntigravityImageConn(w, r, body, prompt, cleanModel, aspectRatio, conn, connData); attemptErr == nil {
			return nil
		} else {
			lastErr = attemptErr
		}
		if usePinned {
			return lastErr
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func imagePinnedConnectionID(r *http.Request) string {
	if id := r.Header.Get("x-connection-id"); id != "" {
		return id
	}
	return r.Header.Get("x-provider-connection-id")
}

func (h *MediaHandler) tryAntigravityImageConn(w http.ResponseWriter, r *http.Request, body []byte, prompt, cleanModel, aspectRatio string, conn *models.ProviderConnection, connData *chat.ConnectionData) error {
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return fmt.Errorf("no API key found for antigravity connection %s", conn.ID)
	}

	// Refresh token if expired
	refreshedKey, pid, err := h.ChatH.RefreshOAuthTokenIfExpired(conn.ID, apiKey)
	if err == nil && refreshedKey != "" {
		apiKey = refreshedKey
	}

	projectID := pid
	if projectID == "" && conn != nil && conn.Data != "" {
		var d struct {
			ProjectID            string `json:"projectId"`
			ProviderSpecificData struct {
				ProjectID string `json:"projectId"`
			} `json:"providerSpecificData"`
		}
		if err := json.Unmarshal([]byte(conn.Data), &d); err == nil {
			if d.ProjectID != "" {
				projectID = d.ProjectID
			} else if d.ProviderSpecificData.ProjectID != "" {
				projectID = d.ProviderSpecificData.ProjectID
			}
		}
	}
	if projectID == "" && connData.ProviderSpecificData != nil {
		if pid, ok := connData.ProviderSpecificData["projectId"].(string); ok {
			projectID = pid
		} else if pid, ok := connData.ProviderSpecificData["project_id"].(string); ok {
			projectID = pid
		}
	}

	if projectID == "" {
		if p, _, _ := chat.FetchAntigravityProjectID(r.Context(), h.Client, apiKey); p != "" {
			projectID = p
			h.ChatH.StoreAntigravityProjectID(conn.ID, p)
		}
	}
	if projectID == "" {
		return fmt.Errorf("antigravity: no project ID — onboard the account in Antigravity (antigravity.google) then re-login")
	}

	providerCfg, err := h.ChatH.GetProviderConfig("antigravity", connData)
	if err != nil {
		return fmt.Errorf("get antigravity config: %w", err)
	}

	var imageReq struct {
		Image  string   `json:"image"`
		Images []string `json:"images"`
	}
	_ = json.Unmarshal(body, &imageReq)
	var base64Input string
	if imageReq.Image != "" {
		base64Input = imageReq.Image
	} else if len(imageReq.Images) > 0 {
		base64Input = imageReq.Images[0]
	}

	wrappedReq, err := translator.WrapAntigravityImageRequest(prompt, base64Input, projectID, cleanModel, aspectRatio)
	if err != nil {
		return fmt.Errorf("wrap antigravity image request: %w", err)
	}

	baseURL := strings.TrimRight(providerCfg.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://daily-cloudcode-pa.googleapis.com"
	}
	targetURL := fmt.Sprintf("%s/v1internal:generateContent", baseURL)

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(wrappedReq))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("User-Agent", "antigravity/ide/2.11.0 darwin/arm64")
	httpReq.Header.Set("X-Client-Name", "antigravity")
	httpReq.Header.Set("X-Client-Version", "2.11.0")

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return clientErr
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("antigravity request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read antigravity response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		errText := string(respBody[:min(500, len(respBody))])
		log.Warn("media", "antigravity image error", "status", resp.StatusCode, "body", errText)
		if h.Repo != nil {
			backoff := h.Repo.GetConnectionBackoffLevel(conn.ID)
		if !handlerutil.IsProbeContext(r.Context()) {
			if classification := providers.ClassifyError(resp.StatusCode, errText, backoff); classification.ShouldFallback {
				cooldownSec := max(classification.CooldownMs/1000, 1)
				_ = h.Repo.LockConnectionModel(conn.ID, cleanModel, cooldownSec, classification.NewBackoffLevel)
				var rawModel string
				var rawBody struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(body, &rawBody); err == nil {
					rawModel = rawBody.Model
				}
				if rawModel != "" && rawModel != cleanModel {
					_ = h.Repo.LockConnectionModel(conn.ID, rawModel, cooldownSec, classification.NewBackoffLevel)
				}
			}
		}
		}
		return fmt.Errorf("antigravity image failed with status %d: %s", resp.StatusCode, string(respBody[:min(300, len(respBody))]))
	}

	formatted, err := translator.FormatAntigravityImageResponse(respBody, prompt)
	if err != nil {
		return fmt.Errorf("format image response: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(formatted)

	if h.Repo != nil {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
		// Probes read production state; they never clear it (see
		// tts_forward.go for the same reasoning on the failure path).
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(conn.ID, cleanModel)
		}
	}
	usagetracker.GetTracker().PushRecent(usagetracker.RecentRequest{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Model:     cleanModel,
		Provider:  "antigravity",
		Status:    "ok",
	}, h.Repo)
	return nil
}
