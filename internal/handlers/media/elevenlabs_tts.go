package media

import (
	"io"
	"net/http"
	"net/url"
	"strings"

	json "encoding/json/v2"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// forwardElevenLabsTTS synthesizes speech through the ElevenLabs text-to-speech
// endpoint, mirroring open-sse/handlers/ttsProviders/elevenlabs.js.
func (h *MediaHandler) forwardElevenLabsTTS(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo, input, voice, respFmt string) {
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)

	preferredConnID := r.Header.Get("x-connection-id")
	if preferredConnID == "" {
		preferredConnID = r.Header.Get("x-provider-connection-id")
	}
	if preferredConnID == "" {
		preferredConnID = modelInfo.ConnectionID
	}
	usePinned := preferredConnID != ""
	excludeIDs := []string{}
	var lastStatus int
	var lastMsg string
	for {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), "elevenlabs", preferredConnID, excludeIDs, modelInfo.Model)
		if err != nil || connData == nil {
			if lastMsg != "" {
				handlerutil.WriteJSONError(w, lastStatus, lastMsg)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusNotFound, "no active connections for provider: elevenlabs")
			return
		}
		status, msg, done := h.tryElevenLabsTTSConn(w, r, modelInfo, input, voice, respFmt, raw, conn, connData)
		if done {
			return
		}
		lastStatus, lastMsg = status, msg
		if usePinned {
			handlerutil.WriteJSONError(w, status, msg)
			return
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func (h *MediaHandler) tryElevenLabsTTSConn(w http.ResponseWriter, r *http.Request, modelInfo *chat.ModelInfo, input, voice, respFmt string, raw map[string]any, conn *models.ProviderConnection, connData *chat.ConnectionData) (int, string, bool) {
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return http.StatusUnauthorized, "no API key found for elevenlabs", false
	}

	model := modelInfo.Model
	modelId := "eleven_flash_v2_5"
	voiceId := model
	if strings.Contains(model, "/") {
		parts := strings.SplitN(model, "/", 2)
		modelId = parts[0]
		voiceId = parts[1]
	} else if strings.HasPrefix(model, "eleven_") && strings.TrimSpace(voice) != "" {
		modelId = model
		voiceId = voice
	}

	rf := strings.ToLower(strings.TrimSpace(respFmt))
	if rf == "" {
		rf = "mp3"
	}
	type outFmt struct {
		param  string
		format string
		mime   string
	}
	var out outFmt
	switch rf {
	case "opus":
		out = outFmt{"opus_48000_128", "opus", "audio/opus"}
	case "wav":
		out = outFmt{"wav_24000", "wav", "audio/wav"}
	case "pcm":
		out = outFmt{"pcm_24000", "pcm", "audio/pcm"}
	case "json":
		out = outFmt{"mp3_44100_128", "mp3", "audio/mpeg"}
	default:
		out = outFmt{"mp3_44100_128", "mp3", "audio/mpeg"}
	}

	payload := map[string]any{
		"text":     input,
		"model_id": modelId,
	}
	if raw != nil {
		if lang, ok := raw["language"].(string); ok && strings.TrimSpace(lang) != "" {
			payload["language_code"] = strings.TrimSpace(lang)
		}
		for _, key := range []string{"voice_settings", "seed", "apply_text_normalization", "previous_text", "next_text", "pronunciation_dictionary_locators"} {
			if v, ok := raw[key]; ok && v != nil {
				payload[key] = v
			}
		}
	}
	payloadBytes, _ := json.Marshal(payload)

	base := elevenLabsTTSURL(h.ChatH, connData)
	if !strings.Contains(base, "text-to-speech") {
		base = strings.TrimRight(base, "/") + "/v1/text-to-speech"
	}
	targetURL := strings.TrimRight(base, "/") + "/" + url.PathEscape(voiceId) + "?output_format=" + url.QueryEscape(out.param)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return http.StatusInternalServerError, "failed to create elevenlabs tts request", false
	}
	req.Header.Set("Content-Type", "application/json")
	providerCfg, _ := h.ChatH.GetProviderConfig("elevenlabs", connData)
	authHeader := "xi-api-key"
	authScheme := "raw"
	if providerCfg != nil {
		if providerCfg.AuthHeader != "" {
			authHeader = providerCfg.AuthHeader
		}
		authScheme = providerCfg.AuthScheme
	}
	handlerutil.SetAuthHeader(req, apiKey, authHeader, authScheme)
	if providerCfg != nil {
		for k, v := range providerCfg.StaticHeaders {
			req.Header.Set(k, v)
		}
	}

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return http.StatusBadGateway, clientErr.Error(), false
	}
	client = guardMediaRedirects(client)
	resp, err := client.Do(req)
	if err != nil {
		return http.StatusBadGateway, "elevenlabs tts request failed: " + err.Error(), true
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode != http.StatusOK {
		snippet := string(respBody[:min(300, len(respBody))])
		msg := snippet
		var parsed map[string]any
		if err := json.Unmarshal(respBody, &parsed); err == nil {
			if d, ok := parsed["detail"].(map[string]any); ok {
				if m, ok := d["message"].(string); ok && m != "" {
					msg = m
				}
			} else if m, ok := parsed["message"].(string); ok && m != "" {
				msg = m
			}
		}
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity {
			return http.StatusBadRequest, msg, false
		}
		log.Warn("media", "elevenlabs tts upstream error", "status", resp.StatusCode, "body", snippet)
		if h.Repo != nil {
			backoff := h.Repo.GetConnectionBackoffLevel(r.Context(), conn.ID)
			if classification := providers.ClassifyError(resp.StatusCode, snippet, backoff, respBody); classification.ShouldFallback {
				cooldownSec := max(classification.CooldownMs/1000, 1)
				if !handlerutil.IsProbeContext(r.Context()) {
					_ = h.Repo.LockConnectionModel(r.Context(), conn.ID, modelInfo.Model, cooldownSec, classification.NewBackoffLevel)
				}
				return resp.StatusCode, msg, true
			}
		}
		return resp.StatusCode, msg, false
	}

	if len(respBody) < 1024 {
		return http.StatusBadGateway, "ElevenLabs TTS returned empty audio", false
	}

	if h.Repo != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(r.Context(), conn.ID, modelInfo.Model)
		}
	}
	h.trackTTSUsage("elevenlabs", modelInfo.Model)
	// writeTTSResponse needs the original response format string for json envelope handling.
	wtFmt := respFmt
	if wtFmt == "" {
		wtFmt = "mp3"
	}
	h.writeTTSResponse(w, r, wtFmt, respBody, out.mime, out.format)
	return 0, "", true
}

func elevenLabsTTSURL(chatH *chat.ChatHandler, connData *chat.ConnectionData) string {
	if connData != nil && strings.TrimSpace(connData.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(connData.BaseURL), "/")
	}
	if cfg, err := chatH.GetProviderConfig("elevenlabs", connData); err == nil && cfg != nil && cfg.TTSURL != "" {
		return strings.TrimRight(cfg.TTSURL, "/")
	}
	return "https://api.elevenlabs.io/v1/text-to-speech"
}
