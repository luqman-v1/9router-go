package media

import (
	"bytes"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/usagetracker"
)

func detectAudioMime(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".webm":
		return "audio/webm"
	default:
		return "audio/mpeg"
	}
}

// handleAntigravitySTT transcribes audio via Gemini/Antigravity multimodal input.
func (h *MediaHandler) handleAntigravitySTT(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	var fileBytes []byte
	var filename string
	var model string
	var language string
	var prompt string
	var responseFormat string

	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		_, params, err := mime.ParseMediaType(contentType)
		if err != nil || params["boundary"] == "" {
			return fmt.Errorf("invalid multipart form data")
		}
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			switch p.FormName() {
			case "file":
				filename = p.FileName()
				fileBytes, _ = io.ReadAll(p)
			case "model":
				b, _ := io.ReadAll(p)
				model = strings.TrimSpace(string(b))
			case "language":
				b, _ := io.ReadAll(p)
				language = strings.TrimSpace(string(b))
			case "prompt":
				b, _ := io.ReadAll(p)
				prompt = strings.TrimSpace(string(b))
			case "response_format":
				b, _ := io.ReadAll(p)
				responseFormat = strings.TrimSpace(string(b))
			}
		}
	} else {
		return fmt.Errorf("expected multipart/form-data for audio transcription")
	}

	if len(fileBytes) == 0 {
		return fmt.Errorf("missing required field: file")
	}

	if model == "" {
		model = modelInfo.Model
	}
	if model == "" || model == "antigravity" || model == "stt" {
		model = "gemini-2.5-flash"
	}
	model = translator.NormalizeAntigravityModel(model)

	pinned := modelInfo.ConnectionID
	if pinned == "" {
		pinned = imagePinnedConnectionID(r)
	}
	usePinned := pinned != ""
	excludeIDs := []string{}
	var lastErr error
	for {
		conn, connData, err := h.ChatH.GetBestConnection("antigravity", pinned, excludeIDs, model)
		if err != nil || conn == nil {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("no active connection for antigravity: %w", err)
		}
		if attemptErr := h.tryAntigravitySTTConn(w, r, body, fileBytes, filename, model, language, prompt, responseFormat, conn, connData); attemptErr == nil {
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

func (h *MediaHandler) tryAntigravitySTTConn(w http.ResponseWriter, r *http.Request, body, fileBytes []byte, filename, model, language, prompt, responseFormat string, conn *models.ProviderConnection, connData *chat.ConnectionData) error {
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return fmt.Errorf("no API key found for antigravity connection %s", conn.ID)
	}

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

	mimeType := detectAudioMime(filename)
	b64Audio := base64.StdEncoding.EncodeToString(fileBytes)

	instruction := "Generate an accurate transcript of the speech in this audio. Return only the transcribed text, with no preamble, markdown code blocks, or commentary."
	if prompt != "" {
		instruction = prompt
	}
	if language != "" {
		instruction += " Language: " + language + "."
	}

	parts := []map[string]any{
		{"text": instruction},
		{"inlineData": map[string]string{
			"mimeType": mimeType,
			"data":     b64Audio,
		}},
	}

	reqPayload := map[string]any{
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": parts,
			},
		},
		"generationConfig": map[string]any{
			"temperature": 0.0,
		},
	}
	wrapper := map[string]any{
		"project":   projectID,
		"model":     model,
		"userAgent": "antigravity",
		"requestId": fmt.Sprintf("stt-%d", time.Now().UnixNano()),
		"request":   reqPayload,
	}
	wrappedReq, err := json.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("wrap request: %w", err)
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
		return fmt.Errorf("antigravity stt request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		errText := string(respBody[:min(500, len(respBody))])
		log.Warn("media", "antigravity stt upstream error", "status", resp.StatusCode, "body", errText)
		if h.Repo != nil {
			backoff := h.Repo.GetConnectionBackoffLevel(conn.ID)
			if classification := providers.ClassifyError(resp.StatusCode, errText, backoff); classification.ShouldFallback {
				cooldownSec := max(classification.CooldownMs/1000, 1)
				_ = h.Repo.LockConnectionModel(conn.ID, model, cooldownSec, classification.NewBackoffLevel)
			}
		}
		return fmt.Errorf("antigravity stt failed with status %d: %s", resp.StatusCode, string(respBody[:min(300, len(respBody))]))
	}

	unwrapped := translator.UnwrapAntigravityResponse(respBody)
	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(unwrapped, &geminiResp); err != nil {
		return fmt.Errorf("unmarshal gemini stt response: %w", err)
	}

	var transcript string
	if len(geminiResp.Candidates) > 0 && len(geminiResp.Candidates[0].Content.Parts) > 0 {
		transcript = strings.TrimSpace(geminiResp.Candidates[0].Content.Parts[0].Text)
	}

	if responseFormat == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(transcript))
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		respJSON, _ := json.Marshal(map[string]any{
			"text": transcript,
		})
		_, _ = w.Write(respJSON)
	}

	if h.Repo != nil {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
		_ = h.Repo.UnlockConnectionModel(conn.ID, model)
	}
	usagetracker.GetTracker().PushRecent(usagetracker.RecentRequest{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Model:     model,
		Provider:  "antigravity",
		Status:    "ok",
	}, h.Repo)
	return nil
}
