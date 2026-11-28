package media

import (
	"bytes"
	"encoding/base64"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/middleware"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
)

// HandleAudioSpeech handles /v1/audio/speech.
func (h *MediaHandler) HandleAudioSpeech(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "tts"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()
	// Xiaomi MiMo TTS uses a chat-completions contract, not the OpenAI
	// /audio/speech shape (port of open-sse/handlers/ttsProviders/xiaomi-mimo.js).
	// It leaves forwardTTSRequest behind, so it needs the guard here.
	if h.isMiMoSpeech(body) {
		if _, refused := guardMediaInput(w, r.Header.Get("Content-Type"), body); refused {
			return
		}
		h.forwardMiMoSpeech(w, r, body)
		return
	}
	h.forwardTTSRequest(w, r, body)
}

// isMiMoSpeech reports whether body.model resolves to the xiaomi-mimo provider.
func (h *MediaHandler) isMiMoSpeech(body []byte) bool {
	var reqBody struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &reqBody); err != nil || reqBody.Model == "" {
		return false
	}
	info, err := h.ChatH.ResolveModel(reqBody.Model)
	return err == nil && info != nil && info.Provider == "xiaomi-mimo"
}

// mimoTTSPrefix/voice defaults (open-sse/handlers/ttsProviders/xiaomi-mimo.js).
const (
	mimoDefaultModel = "mimo-v2.5-tts"
	mimoDefaultVoice = "mimo_default"
)

// parseMiMoModelVoice splits the model part as "modelId/voiceId" against the
// single known mimo model (port of _base.js parseModelVoice for this adapter).
func parseMiMoModelVoice(model string) (modelID, voiceID string) {
	switch {
	case model == "":
		return mimoDefaultModel, mimoDefaultVoice
	case model == mimoDefaultModel:
		return mimoDefaultModel, mimoDefaultVoice
	case strings.HasPrefix(model, mimoDefaultModel+"/"):
		return mimoDefaultModel, strings.TrimPrefix(model, mimoDefaultModel+"/")
	}
	if before, after, ok := strings.CutLast(model, "/"); ok && before != "" {
		return before, after
	}
	return mimoDefaultModel, mimoDefaultVoice
}

// forwardMiMoSpeech synthesizes speech via Xiaomi MiMo's OpenAI-compatible
// chat-completions contract: target text in role:assistant, style/language
// instructions in role:user, voice via the top-level audio.voice field, audio
// returned base64 in choices[0].message.audio.data.
func (h *MediaHandler) forwardMiMoSpeech(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Model       string `json:"model"`
		Input       string `json:"input"`
		Style       string `json:"style"`
		Language    string `json:"language"`
		ResponseFmt string `json:"response_format"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "Missing required field: input")
		return
	}

	modelInfo, err := h.ChatH.ResolveModel(req.Model)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	modelID, voiceID := parseMiMoModelVoice(modelInfo.Model)

	// Language and style are soft instructions; MiMo auto-detects the spoken
	// language of the text, the hint only nudges it (independent of the voice).
	var instructions []string
	if req.Language != "" {
		instructions = append(instructions, "Speak in "+req.Language+".")
	}
	if req.Style != "" {
		instructions = append(instructions, req.Style)
	}
	messages := []map[string]string{{"role": "assistant", "content": req.Input}}
	if len(instructions) > 0 {
		messages = append([]map[string]string{{"role": "user", "content": strings.Join(instructions, " ")}}, messages...)
	}

	payload, err := json.Marshal(map[string]any{
		"model":    modelID,
		"stream":   false,
		"messages": messages,
		"audio":    map[string]string{"format": "wav", "voice": voiceID},
	})
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	preferredConnID := preferredMediaConnID(r, modelInfo)
	usePinned := preferredConnID != ""
	excludeIDs := []string{}
	tried := make(map[string]struct{})
	var lastErr *mediaForwardAttemptErr

	for {
		if r.Context().Err() != nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusBadGateway, r.Context().Err().Error())
			return
		}
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), modelInfo.Provider, preferredConnID, excludeIDs, modelInfo.Model)
		if err != nil || connData == nil {
			if lastErr != nil {
				lastErr.write(w)
				return
			}
			handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(err, modelInfo.Provider))
			return
		}
		if conn != nil {
			if _, seen := tried[conn.ID]; seen {
				if lastErr != nil {
					lastErr.write(w)
					return
				}
				handlerutil.WriteJSONError(w, http.StatusNotFound, mediaConnectionError(nil, modelInfo.Provider))
				return
			}
			tried[conn.ID] = struct{}{}
		}

		attemptErr := h.tryMiMoSpeechConn(w, r, payload, req.ResponseFmt, modelInfo, conn, connData)
		if attemptErr == nil {
			return
		}
		lastErr = attemptErr
		if conn == nil || usePinned || !attemptErr.retryable || r.Context().Err() != nil {
			lastErr.write(w)
			return
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

func (h *MediaHandler) tryMiMoSpeechConn(
	w http.ResponseWriter,
	r *http.Request,
	payload []byte,
	responseFmt string,
	modelInfo *chat.ModelInfo,
	conn *models.ProviderConnection,
	connData *chat.ConnectionData,
) *mediaForwardAttemptErr {
	providerCfg, err := h.ChatH.GetProviderConfig(modelInfo.Provider, connData)
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusInternalServerError, jsonMsg: err.Error(), retryable: false}
	}
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return &mediaForwardAttemptErr{status: http.StatusUnauthorized, jsonMsg: "no API key found", retryable: conn != nil}
	}

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: clientErr.Error(), retryable: true}
	}
	if client == nil {
		client = h.Client
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, providerCfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusInternalServerError, jsonMsg: fmt.Sprintf("create request: %v", err), retryable: false}
	}
	upstreamReq.Header.Set(constants.HeaderContentType, constants.ContentTypeJSON)
	upstreamReq.Header.Set(constants.HeaderAuthorization, "Bearer "+apiKey)

	resp, err := client.Do(upstreamReq)
	if err != nil {
		return &mediaForwardAttemptErr{
			status:    http.StatusBadGateway,
			jsonMsg:   fmt.Sprintf("upstream error: %v", err),
			retryable: r.Context().Err() == nil,
		}
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: err.Error(), retryable: r.Context().Err() == nil}
	}

	var data struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Message struct {
				Audio struct {
					Data   string `json:"data"`
					Format string `json:"format"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	// Upstream body may be non-JSON; tolerate parse failure (JS wraps in try/catch).
	_ = json.Unmarshal(respBody, &data)

	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("MiMo TTS error (%d)", resp.StatusCode)
		switch {
		case data.Error != nil && data.Error.Message != "":
			msg = data.Error.Message
		case len(bytes.TrimSpace(respBody)) > 0:
			msg = string(bytes.TrimSpace(respBody))
		}
		backoff := 0
		if conn != nil && h.Repo != nil {
			backoff = h.Repo.GetConnectionBackoffLevel(r.Context(), conn.ID)
		}
		snippet := string(respBody[:min(500, len(respBody))])
		classification := providers.ClassifyErrorForProvider(resp.StatusCode, snippet, backoff, modelInfo.Provider, respBody)
		if classification.ShouldFallback && conn != nil && h.Repo != nil && !handlerutil.IsProbeContext(r.Context()) {
			cooldownSec := max(classification.CooldownMs/1000, 1)
			_ = h.Repo.LockConnectionModel(r.Context(), conn.ID, modelInfo.Model, cooldownSec, classification.NewBackoffLevel)
		}
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: msg, retryable: classification.ShouldFallback}
	}

	audio, format := "", "wav"
	if len(data.Choices) > 0 {
		audio = data.Choices[0].Message.Audio.Data
		if data.Choices[0].Message.Audio.Format != "" {
			format = data.Choices[0].Message.Audio.Format
		}
	}
	if audio == "" {
		msg := "MiMo TTS returned no audio"
		if data.Error != nil && data.Error.Message != "" {
			msg = data.Error.Message
		}
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: msg, retryable: false}
	}

	// response_format=json returns {audio, format}; default returns raw audio bytes.
	if responseFmt == "json" {
		if conn != nil && h.Repo != nil {
			_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
			if !handlerutil.IsProbeContext(r.Context()) {
				_ = h.Repo.UnlockConnectionModel(r.Context(), conn.ID, modelInfo.Model)
			}
		}
		handlerutil.WriteJSON(w, http.StatusOK, map[string]string{"audio": audio, "format": format})
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(audio)
	if err != nil {
		return &mediaForwardAttemptErr{status: http.StatusBadGateway, jsonMsg: "MiMo TTS returned invalid audio", retryable: false}
	}
	if conn != nil && h.Repo != nil {
		_ = h.Repo.UpdateConnectionLastUsed(conn.ID)
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(r.Context(), conn.ID, modelInfo.Model)
		}
	}
	w.Header().Set("Content-Type", "audio/"+format)
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	return nil
}

// HandleAudioTranscriptions handles /v1/audio/transcriptions.
func (h *MediaHandler) HandleAudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "stt"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		middleware.WriteBodyReadError(w, r, err)
		return
	}
	defer func() { _ = r.Body.Close() }()
	h.forwardMediaRequest(w, r, body, "whisper-1", "/v1/audio/transcriptions")
}

// HandleAudioTranslations handles /v1/audio/translations.
func (h *MediaHandler) HandleAudioTranslations(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(handlerutil.WithServiceKind(r.Context(), "stt"))
	body, err := io.ReadAll(r.Body)
	if err != nil {
		middleware.WriteBodyReadError(w, r, err)
		return
	}
	defer func() { _ = r.Body.Close() }()
	h.forwardMediaRequest(w, r, body, "whisper-1", "/v1/audio/translations")
}
