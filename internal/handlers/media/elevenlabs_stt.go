package media

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/usagetracker"
)

// ElevenLabs Scribe STT. The generic OpenAI-compatible multipart path is
// rejected by ElevenLabs: the vendor takes its own field names (model_id,
// language_code), authenticates with `xi-api-key`, and returns subtitle/caption
// renders as `additional_formats[].content` rather than a JSON envelope.
//
// Wire observables (upstream open-sse/handlers/sttCore.js transcribeElevenLabs):
//   - model_id carries the model, `file` the audio part;
//   - a blank `language` is omitted so the vendor auto-detects;
//   - timestamps_granularity / tag_audio_events ride the body only when valid;
//   - diarize and num_speakers are mutually exclusive — diarize wins;
//   - response_format maps to an `additional_formats` render that is served
//     verbatim, never synthesized from the JSON body.


// elevenLabsResponseFormats maps the OpenAI response_format onto the Scribe
// `additional_formats[].format` names. Mirrors upstream sttConfig.responseFormats.
type elevenLabsResponseFormats struct {
	Segments  string
	Subtitles string
	Captions  string
}

var elevenLabsSTTFormats = elevenLabsResponseFormats{
	Segments:  "seg_json",
	Subtitles: "srt",
	Captions:  "vtt",
}

// elevenLabsSTTForm is the normalized caller request: the audio part plus the
// scalar form fields the client sent.
type elevenLabsSTTForm struct {
	FileName string
	FileData []byte
	Model    string
	Fields   map[string]string
}

func (f *elevenLabsSTTForm) get(key string) string {
	return strings.TrimSpace(f.Fields[key])
}

// parseElevenLabsSTTRequest reads the caller's multipart body. Non-multipart
// callers are rejected outright — there is no JSON form of this endpoint.
func parseElevenLabsSTTRequest(body []byte, contentType string, modelInfo *chat.ModelInfo) (*elevenLabsSTTForm, error) {
	if !strings.Contains(contentType, "multipart/form-data") {
		return nil, fmt.Errorf("expected multipart/form-data for audio transcription")
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, fmt.Errorf("invalid multipart form data")
	}

	form := &elevenLabsSTTForm{Fields: map[string]string{}}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		name := p.FormName()
		if name == "file" {
			form.FileName = p.FileName()
			form.FileData, _ = io.ReadAll(p)
			continue
		}
		if name == "" {
			continue
		}
		b, _ := io.ReadAll(p)
		form.Fields[name] = strings.TrimSpace(string(b))
	}

	if len(form.FileData) == 0 {
		return nil, fmt.Errorf("missing required field: file")
	}
	// model_id is the upstream model id, not the caller's provider-qualified
	// spelling — the vendor knows nothing about 9router's "elevenlabs/…" form.
	form.Model = modelInfo.Model
	if form.Model == "" {
		form.Model = form.get("model")
	}
	return form, nil
}

// elevenLabsAdditionalFormat resolves an OpenAI response_format into the
// Scribe render worth requesting. raw marks the subtitle/caption formats, which
// are served as a text body rather than a JSON envelope.
func elevenLabsAdditionalFormat(responseFormat string, fmts elevenLabsResponseFormats) (format string, raw bool) {
	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "srt":
		return fmts.Subtitles, true
	case "vtt":
		return fmts.Captions, true
	case "verbose_json":
		return fmts.Segments, false
	}
	return "", false
}

// elevenLabsSTTAdditionalFormats is the wire value for the `additional_formats`
// form field, or "" when the requested format needs no extra render.
func elevenLabsSTTAdditionalFormats(responseFormat string) string {
	format, _ := elevenLabsAdditionalFormat(responseFormat, elevenLabsSTTFormats)
	if format == "" {
		return ""
	}
	b, err := json.Marshal([]map[string]string{{"format": format}})
	if err != nil {
		return ""
	}
	return string(b)
}

// buildElevenLabsSTTBody renders the upstream multipart body. Fields the caller
// left blank or sent out of range are omitted entirely rather than forwarded,
// so the vendor's own defaults (and its auto language detection) still apply.
func buildElevenLabsSTTBody(form *elevenLabsSTTForm) (*bytes.Buffer, string, error) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)

	fileName := form.FileName
	if fileName == "" {
		fileName = "audio.wav"
	}
	part, err := w.CreateFormFile("file", fileName)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(form.FileData); err != nil {
		return nil, "", err
	}

	fields := [][2]string{{"model_id", form.Model}}
	if lang := form.get("language"); lang != "" {
		fields = append(fields, [2]string{"language_code", lang})
	}
	switch granularity := form.get("timestamps_granularity"); granularity {
	case "word", "character", "none":
		fields = append(fields, [2]string{"timestamps_granularity", granularity})
	}
	if form.get("tag_audio_events") == "true" {
		fields = append(fields, [2]string{"tag_audio_events", "true"})
	}
	// diarize and num_speakers are mutually exclusive upstream; diarize wins
	// when both are present so the request stays valid instead of erroring.
	if form.get("diarize") == "true" {
		fields = append(fields, [2]string{"diarize", "true"})
	} else if speakers := elevenLabsNumSpeakers(form.get("num_speakers")); speakers != "" {
		fields = append(fields, [2]string{"num_speakers", speakers})
	}
	if extra := elevenLabsSTTAdditionalFormats(form.get("response_format")); extra != "" {
		fields = append(fields, [2]string{"additional_formats", extra})
	}

	for _, kv := range fields {
		if err := w.WriteField(kv[0], kv[1]); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf, w.FormDataContentType(), nil
}

// elevenLabsNumSpeakers normalizes the speaker count to Scribe's 1-32 window,
// returning "" for anything outside it (or non-numeric).
func elevenLabsNumSpeakers(raw string) string {
	if raw == "" {
		return ""
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) {
		return ""
	}
	if n < 1 || n > 32 {
		return ""
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// elevenLabsSTTURL honors a per-connection base URL override (upstream reads
// cfg.baseUrl, which a connection may point at a regional host) before the
// provider registry's STT endpoint.
func elevenLabsSTTURL(chatH *chat.ChatHandler, connData *chat.ConnectionData) string {
	if connData != nil && strings.TrimSpace(connData.BaseURL) != "" {
		return strings.TrimRight(strings.TrimSpace(connData.BaseURL), "/")
	}
	cfg, err := chatH.GetProviderConfig("elevenlabs", connData)
	if err != nil || cfg == nil || cfg.STTURL == "" {
		return ""
	}
	return strings.TrimRight(cfg.STTURL, "/")
}

// handleElevenLabsSTT transcribes audio via the ElevenLabs Scribe API.
func (h *MediaHandler) handleElevenLabsSTT(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	form, err := parseElevenLabsSTTRequest(body, r.Header.Get("Content-Type"), modelInfo)
	if err != nil {
		return err
	}
	if form.Model == "" {
		return fmt.Errorf("missing model for elevenlabs transcription")
	}

	provider := modelInfo.Provider
	if provider == "" {
		provider = "elevenlabs"
	}

	// Upstream parity (sttCore credential loop): rotate through the active
	// accounts. A pinned connection is honored once and its error surfaces.
	pinned := modelInfo.ConnectionID
	if pinned == "" {
		pinned = imagePinnedConnectionID(r)
	}
	usePinned := pinned != ""
	excludeIDs := []string{}
	var lastErr error
	for {
		conn, connData, err := h.ChatH.GetBestConnectionWithContext(r.Context(), provider, pinned, excludeIDs, form.Model)
		if err != nil || conn == nil {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("no active connection for %s: %w", provider, err)
		}
		if attemptErr := h.tryElevenLabsSTTConn(w, r, form, conn, connData); attemptErr == nil {
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

func (h *MediaHandler) tryElevenLabsSTTConn(w http.ResponseWriter, r *http.Request, form *elevenLabsSTTForm, conn *models.ProviderConnection, connData *chat.ConnectionData) error {
	apiKey := chat.ExtractAPIKey(connData)
	if apiKey == "" {
		return fmt.Errorf("no API key found for %s connection %s", conn.Provider, conn.ID)
	}

	providerCfg, err := h.ChatH.GetProviderConfig("elevenlabs", connData)
	if err != nil {
		return fmt.Errorf("get elevenlabs config: %w", err)
	}

	reqBody, contentType, err := buildElevenLabsSTTBody(form)
	if err != nil {
		return fmt.Errorf("build elevenlabs stt request: %w", err)
	}

	sttURL := elevenLabsSTTURL(h.ChatH, connData)
	if sttURL == "" {
		return fmt.Errorf("no ElevenLabs speech-to-text endpoint configured")
	}

	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, sttURL, bytes.NewReader(reqBody.Bytes()))
	if err != nil {
		return fmt.Errorf("create elevenlabs stt request: %w", err)
	}
	httpReq.Header.Set("Content-Type", contentType)
	authHeader := providerCfg.AuthHeader
	if authHeader == "" {
		authHeader = "xi-api-key"
	}
	handlerutil.SetAuthHeader(httpReq, apiKey, authHeader, providerCfg.AuthScheme)
	for k, v := range providerCfg.StaticHeaders {
		httpReq.Header.Set(k, v)
	}

	client, clientErr := h.ChatH.GetClientForConnection(connData)
	if clientErr != nil {
		return clientErr
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("elevenlabs stt request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := string(respBody[:min(300, len(respBody))])
		log.Warn("media", "elevenlabs stt upstream error", "status", resp.StatusCode, "body", snippet)
		if h.Repo != nil {
			backoff := h.Repo.GetConnectionBackoffLevel(conn.ID)
			if !handlerutil.IsProbeContext(r.Context()) {
				if classification := providers.ClassifyError(resp.StatusCode, snippet, backoff); classification.ShouldFallback {
					cooldownSec := max(classification.CooldownMs/1000, 1)
					_ = h.Repo.LockConnectionModel(conn.ID, form.Model, cooldownSec, classification.NewBackoffLevel)
				}
			}
		}
		return fmt.Errorf("elevenlabs stt failed with status %d: %s", resp.StatusCode, snippet)
	}

	writeElevenLabsSTTResponse(w, form.get("response_format"), respBody)

	if h.Repo != nil {
		h.Repo.UpdateConnectionLastUsed(conn.ID)
		// Probes read production state; they never clear it.
		if !handlerutil.IsProbeContext(r.Context()) {
			_ = h.Repo.UnlockConnectionModel(conn.ID, form.Model)
		}
	}
	usagetracker.GetTracker().PushRecent(usagetracker.RecentRequest{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Model:     form.Model,
		Provider:  "elevenlabs",
		Status:    "ok",
	}, h.Repo)
	return nil
}

// writeElevenLabsSTTResponse serves the requested render. Subtitle/caption
// formats are the vendor's own strings and go out verbatim; verbose_json gets
// real segments only when the seg_json render was actually returned, so
// transports diffed by clients never show invented timings.
func writeElevenLabsSTTResponse(w http.ResponseWriter, responseFormat string, respBody []byte) {
	var upstream map[string]any
	if err := json.Unmarshal(respBody, &upstream); err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "invalid elevenlabs stt response")
		return
	}
	text, _ := upstream["text"].(string)

	format, raw := elevenLabsAdditionalFormat(responseFormat, elevenLabsSTTFormats)
	rendered, hasRendered := extractElevenLabsAdditionalFormat(upstream, format)

	if raw {
		// The subtitle/caption renders arrive as plain strings; anything else
		// falls back to the transcript rather than being guessed at.
		body := text
		if renderedText, isString := rendered.(string); isString {
			body = renderedText
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
		return
	}

	switch strings.ToLower(strings.TrimSpace(responseFormat)) {
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(text))
		return
	case "verbose_json":
		envelope := map[string]any{
			"text":     text,
			"language": upstream["language_code"],
		}
		if p, ok := upstream["language_probability"]; ok {
			envelope["language_probability"] = p
		}
		if words, ok := upstream["words"].([]any); ok {
			envelope["words"] = words
		}
		if n, ok := upstream["num_speakers"]; ok {
			envelope["num_speakers"] = n
		}
		if segments, ok := rendered.([]any); ok && hasRendered {
			envelope["segments"] = segments
		}
		handlerutil.WriteJSON(w, http.StatusOK, envelope)
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{"text": text})
}

// extractElevenLabsAdditionalFormat returns `additional_formats[].content` for
// format. The vendor returns srt/vtt as a plain string and seg_json as a JSON
// *string*; an absent or unparseable render yields ok=false so callers fall back
// instead of guessing.
func extractElevenLabsAdditionalFormat(upstream map[string]any, format string) (value any, ok bool) {
	if format == "" {
		return nil, false
	}
	entries, isSlice := upstream["additional_formats"].([]any)
	if !isSlice {
		return nil, false
	}
	for _, entry := range entries {
		entryMap, isMap := entry.(map[string]any)
		if !isMap || handlerutil.GetString(entryMap, "format") != format {
			continue
		}
		content, isString := entryMap["content"].(string)
		if !isString {
			return nil, false
		}
		if format != elevenLabsSTTFormats.Segments {
			return content, true
		}
		var segments any
		if err := json.Unmarshal([]byte(content), &segments); err != nil {
			return nil, false
		}
		return segments, true
	}
	return nil, false
}
