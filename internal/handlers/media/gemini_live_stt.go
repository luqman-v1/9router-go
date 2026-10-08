package media

import (
	"bytes"
	"context"
	"encoding/base64"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/handlers/chat"
	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
)

// Gemini Live API realtime STT transport. Port of
// open-sse/handlers/geminiLiveStt.js (upstream v0.5.91).
//
// The REST generateContent path only transcribes whole files inline. The Live
// API's `:bidiGenerateContent` WebSocket is the streaming counterpart: audio
// goes up as realtimeInput mediaChunks and the server pushes incremental
// `serverContent.inputTranscription` events back. This file owns the socket
// lifecycle only — the response envelope stays the engine's single STT shape.

const (
	geminiLiveSetupTimeout = 10 * time.Second  // open → setupComplete
	geminiLiveTurnTimeout  = 60 * time.Second  // audio streamed → turnComplete
	geminiLiveMaxTimeout   = 300 * time.Second // clamp ceiling for caller-supplied knobs
	geminiLiveChunkBytes   = 16 * 1024         // ~0.5s of 16-bit 16kHz mono PCM
	// geminiLiveGoAwayReconnects is how many goAway advisories a single call may
	// rotate through: the socket is re-dialled, setup replayed, and audio
	// resumed from the offset already sent.
	geminiLiveGoAwayReconnects = 1
)

// liveSTTError carries the HTTP status so the caller shapes the same error
// envelope the other STT transports produce.
type liveSTTError struct {
	message string
	status  int
}

func (e *liveSTTError) Error() string { return e.message }

func liveErrf(status int, format string, args ...any) *liveSTTError {
	return &liveSTTError{message: fmt.Sprintf(format, args...), status: status}
}

// liveSTTResult is the transcript plus the raw per-frame deltas, so verbose_json
// segments can be shaped without fabricating timestamps the frames never carry.
type liveSTTResult struct {
	text   string
	chunks []string
}

// liveServerContent is the part of a server frame this transport reads.
type liveServerContent struct {
	SetupComplete      bool `json:"setupComplete"`
	TurnComplete       bool `json:"turnComplete"`
	InputTranscription *struct {
		Text string `json:"text"`
	} `json:"inputTranscription"`
}

type liveFrame struct {
	Error *struct {
		Message string `json:"message"`
		Status  any    `json:"status"`
	} `json:"error"`
	GoAway *struct {
		Time string `json:"time"`
	} `json:"goAway"`
	ServerContent *liveServerContent `json:"serverContent"`
}

// liveSTTSession owns one transcription call across however many sockets the
// goAway advisories cost.
type liveSTTSession struct {
	wsURL    string
	token    string
	model    string
	audio    []byte
	mimeType string
	system   string
	form     map[string]string

	text       string
	chunks     []string
	sent       int // audio prefix already handed to the current socket
	streamed   bool
	goAwayLeft int

	dialer *websocket.Dialer
}

// transcribeGeminiLive streams one audio file through the Live bidirectional
// socket and returns the accumulated transcript.
func transcribeGeminiLive(ctx context.Context, baseURL, model, token string, audio []byte, mimeType string, form map[string]string) (*liveSTTResult, error) {
	if len(audio) == 0 {
		return nil, liveErrf(http.StatusBadRequest, "Empty audio file")
	}

	wsURL, err := toLiveWSURL(baseURL, model, token)
	if err != nil {
		return nil, err
	}

	session := &liveSTTSession{
		wsURL:      wsURL,
		model:      model,
		audio:      audio,
		mimeType:   mimeType,
		system:     liveSystemInstruction(form),
		form:       form,
		goAwayLeft: geminiLiveGoAwayReconnects,
		dialer:     websocket.DefaultDialer,
	}
	return session.run(ctx)
}

func (s *liveSTTSession) run(ctx context.Context) (*liveSTTResult, error) {
	result := &liveSTTResult{}

	for {
		socket, _, err := s.dialer.DialContext(ctx, s.wsURL, nil)
		if err != nil {
			if strings.TrimSpace(result.text) != "" {
				return result, nil
			}
			return nil, liveErrf(http.StatusBadGateway, "Gemini Live websocket connection failed")
		}

		done, err := s.converse(ctx, socket, result)
		socket.Close()
		if done || err != nil {
			return result, err
		}
		// A goAway rotated the socket: the transcript survives, and audio
		// resumes from the offset already sent.
	}
}

// converse drives one socket. It reports done=true when the turn completed or
// closed with a partial transcript, and (false, nil) when a goAway asked for a
// rotation before the deadline.
func (s *liveSTTSession) converse(ctx context.Context, socket *websocket.Conn, result *liveSTTResult) (bool, error) {
	if err := socket.WriteJSON(map[string]any{"setup": map[string]any{
		"model": "models/" + s.model,
		"generationConfig": map[string]any{
			"responseModalities":      []string{"TEXT"},
			"inputAudioTranscription": map[string]any{},
		},
		"systemInstruction": map[string]any{
			"parts": []any{map[string]any{"text": s.system}},
		},
	}}); err != nil {
		return false, liveErrf(http.StatusBadGateway, "Gemini Live socket closed while sending setup")
	}

	for {
		if err := socket.SetReadDeadline(time.Now().Add(s.nextTimeout())); err != nil {
			return false, liveErrf(http.StatusBadGateway, "Gemini Live socket setup failed")
		}
		_, data, err := socket.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			// A partial transcript beats a hard error on a graceful close;
			// silence is a hard error.
			if strings.TrimSpace(result.text) != "" {
				return true, nil
			}
			return true, liveErrf(http.StatusBadGateway, "Gemini Live socket closed before completion")
		}

		var frame liveFrame
		if err := json.Unmarshal(data, &frame, handlerutil.UpstreamBody); err != nil {
			continue // non-JSON frames carry no Live API semantics
		}

		if frame.Error != nil {
			status := ""
			if frame.Error.Status != nil {
				status = fmt.Sprintf(" (%v)", frame.Error.Status)
			}
			return true, liveErrf(http.StatusBadGateway, "Gemini Live error%s: %s", status, frame.Error.Message)
		}

		if delta := liveTranscriptionDelta(frame); delta != "" {
			result.text += delta
			result.chunks = append(result.chunks, delta)
		}

		if frame.ServerContent == nil {
			if frame.GoAway != nil && s.tryRotate(frame.GoAway.Time) {
				return false, nil
			}
			continue
		}

		if frame.ServerContent.SetupComplete {
			s.streamed = true
			if err := s.streamAudioAndPrompt(socket); err != nil {
				return true, err
			}
			continue
		}
		if frame.ServerContent.TurnComplete {
			return true, nil
		}
	}
}

// nextTimeout is the read deadline for the next frame: the setup budget before
// setupComplete, the turn budget after audio has been streamed.
func (s *liveSTTSession) nextTimeout() time.Duration {
	if s.streamed {
		return liveTimeoutField(s.form, "turn_timeout_ms", geminiLiveTurnTimeout)
	}
	return liveTimeoutField(s.form, "setup_timeout_ms", geminiLiveSetupTimeout)
}

// tryRotate spends one goAway advisory. The server names the instant it will
// force-close; rotating before that is the graceful play. Once the budget is
// spent a later goAway is left to the close path, which settles on the partial
// transcript.
func (s *liveSTTSession) tryRotate(goAwayTime string) bool {
	if s.goAwayLeft <= 0 {
		return false
	}
	s.goAwayLeft--
	return true
}

// streamAudioAndPrompt sends every audio byte not yet sent as realtimeInput
// mediaChunks, then the flushing user turn that yields turnComplete.
func (s *liveSTTSession) streamAudioAndPrompt(socket *websocket.Conn) error {
	for s.sent < len(s.audio) {
		end := min(s.sent+geminiLiveChunkBytes, len(s.audio))
		frame := map[string]any{
			"realtimeInput": map[string]any{
				"mediaChunks": []any{map[string]any{
					"mimeType": s.mimeType,
					"data":     base64.StdEncoding.EncodeToString(s.audio[s.sent:end]),
				}},
			},
		}
		if err := socket.WriteJSON(frame); err != nil {
			return liveErrf(http.StatusBadGateway, "Gemini Live socket closed while streaming audio")
		}
		s.sent = end
	}
	// Final user turn: flushes the recognizer and yields turnComplete.
	err := socket.WriteJSON(map[string]any{
		"clientContent": map[string]any{
			"turns":        []any{map[string]any{"parts": []any{map[string]any{"text": s.system}}}},
			"turnComplete": true,
		},
	})
	if err != nil {
		return liveErrf(http.StatusBadGateway, "Gemini Live socket closed while streaming audio")
	}
	return nil
}

// liveTranscriptionDelta returns the non-blank transcription delta. A
// padding-only frame carries no transcript and must not make an empty run look
// like a partial success on close.
func liveTranscriptionDelta(frame liveFrame) string {
	if frame.ServerContent == nil || frame.ServerContent.InputTranscription == nil {
		return ""
	}
	delta := frame.ServerContent.InputTranscription.Text
	if strings.TrimSpace(delta) == "" {
		return ""
	}
	return delta
}

// liveSystemInstruction picks the text the recogniser is primed with.
// `system_instruction` (a registry param) replaces the built-in directive
// wholesale; `prompt` and `language` only shape the default.
func liveSystemInstruction(form map[string]string) string {
	if override := strings.TrimSpace(form["system_instruction"]); override != "" {
		return override
	}
	instruction := strings.TrimSpace(form["prompt"])
	if instruction == "" {
		instruction = "Transcribe the spoken audio verbatim."
	}
	if language := strings.TrimSpace(form["language"]); language != "" {
		return instruction + " Language: " + language + "."
	}
	return instruction
}

func liveTimeoutField(form map[string]string, key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(form[key])
	if raw == "" {
		return fallback
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return fallback
	}
	d := time.Duration(ms) * time.Millisecond
	if d > geminiLiveMaxTimeout {
		return geminiLiveMaxTimeout
	}
	return d
}

// toLiveWSURL turns a REST base (https://host/v1beta/models) into the Live
// WebSocket endpoint (wss://host/ws/api/v1beta/models/<model>:bidiGenerateContent).
func toLiveWSURL(baseURL, model, token string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", liveErrf(http.StatusBadGateway, "Gemini Live base URL is unparseable: %s", baseURL)
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", liveErrf(http.StatusBadGateway, "Gemini Live needs an http(s) base URL, got %q", baseURL)
	}
	if !strings.HasPrefix(u.Path, "/ws/") {
		u.Path = "/ws/api" + u.Path
	}
	return fmt.Sprintf("%s/%s:bidiGenerateContent?key=%s",
		strings.TrimRight(u.String(), "/"), url.PathEscape(model), url.QueryEscape(token)), nil
}

// parseLiveSTTForm pulls the multipart fields this transport reads out of a
// transcription request, plus the declared audio content type. The form
// pass-through every transport already gets carries the registry params, so no
// engine change is needed to reach this leaf.
func parseLiveSTTForm(body []byte, contentType string) (audio []byte, contentTypeOfAudio, model string, form map[string]string, err error) {
	form = map[string]string{}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return nil, "", "", nil, fmt.Errorf("invalid multipart form data")
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		p, perr := mr.NextPart()
		if perr != nil {
			if perr == io.EOF {
				break
			}
			return nil, "", "", nil, fmt.Errorf("invalid multipart form data")
		}
		value, _ := io.ReadAll(p)
		switch p.FormName() {
		case "file":
			audio = value
			contentTypeOfAudio = p.Header.Get("Content-Type")
		case "model":
			model = strings.TrimSpace(string(value))
		default:
			form[p.FormName()] = strings.TrimSpace(string(value))
		}
	}
	return audio, contentTypeOfAudio, model, form, nil
}

// logLiveSTTTraffic keeps the operator able to tell a transport failure from a
// model failure: both arrive as one error string otherwise.
func logLiveSTTTraffic(provider, model string, err error) {
	log.Warn("media", "gemini live stt failed", "provider", provider, "model", model, "error", err)
}

// geminiLiveRESTBase is the native Gemini REST base the Live WebSocket URL is
// derived from. It is deliberately not the provider's OpenAI-compatible
// BaseURL: toLiveWSURL prefixes the path with /ws/api, so an
// /v1beta/openai/chat/completions base would produce a wrong socket URL.
// (open-sse/providers/registry/gemini.js sttConfig.baseUrl.)
const geminiLiveRESTBase = "https://generativelanguage.googleapis.com/v1beta/models"

// handleGeminiLiveSTT transcribes one audio file over the Gemini Live
// bidirectional socket. Dispatch reaches it on the model's transport marker, not
// on a hardcoded id, so a new realtime provider extends the engine through data.
func (h *MediaHandler) handleGeminiLiveSTT(w http.ResponseWriter, r *http.Request, body []byte, modelInfo *chat.ModelInfo) error {
	audio, audioContentType, model, form, err := parseLiveSTTForm(body, r.Header.Get("Content-Type"))
	if err != nil {
		return err
	}
	if len(audio) == 0 {
		return liveErrf(http.StatusBadRequest, "missing required field: file")
	}
	if model == "" {
		model = modelInfo.Model
	}
	mimeType := audioContentType
	if mimeType == "" {
		mimeType = "audio/wav"
	}

	pinned := modelInfo.ConnectionID
	if pinned == "" {
		pinned = imagePinnedConnectionID(r)
	}
	usePinned := pinned != ""
	excludeIDs := []string{}
	var lastErr error
	for {
		conn, connData, err := h.ChatH.GetBestConnection(modelInfo.Provider, pinned, excludeIDs, model)
		if err != nil || conn == nil {
			if lastErr != nil {
				return lastErr
			}
			return fmt.Errorf("no active connection for %s: %w", modelInfo.Provider, err)
		}
		apiKey := chat.ExtractAPIKey(connData)
		if apiKey == "" {
			return fmt.Errorf("no API key found for %s connection %s", modelInfo.Provider, conn.ID)
		}

		result, transcribeErr := transcribeGeminiLive(r.Context(), geminiLiveRESTBase, model, apiKey, audio, mimeType, form)
		if transcribeErr == nil {
			return writeLiveSTTResult(w, result, form["response_format"])
		}
		lastErr = transcribeErr
		logLiveSTTTraffic(modelInfo.Provider, model, transcribeErr)
		if usePinned {
			return lastErr
		}
		excludeIDs = append(excludeIDs, conn.ID)
	}
}

// writeLiveSTTResult shapes the response with the same envelope the
// OpenAI-compatible transport uses: {text} by default, and for verbose_json a
// segments list built from the Live API's incremental deltas. Those frames carry
// no timestamps, so segments expose {id,text} only — start/end/duration are
// deliberately absent rather than fabricated as zeros.
func writeLiveSTTResult(w http.ResponseWriter, result *liveSTTResult, responseFormat string) error {
	payload := map[string]any{"text": result.text}
	if strings.EqualFold(strings.TrimSpace(responseFormat), "verbose_json") {
		segments := make([]map[string]any, 0, len(result.chunks))
		for id, segText := range result.chunks {
			segments = append(segments, map[string]any{"id": id, "text": segText})
		}
		payload["segments"] = segments
	}
	respJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode live stt response: %w", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respJSON)
	return nil
}

// isTranscriptionEndpoint reports whether an endpoint is one of the two
// spellings the STT handlers are mounted on.
func isTranscriptionEndpoint(endpoint string) bool {
	return endpoint == "/v1/audio/transcriptions" || endpoint == "/audio/transcriptions"
}

// writeLiveSTTError renders a transport failure with the status the Live
// transport chose (502 upstream trouble, 504 a lifecycle timeout, 400 a bad
// request) instead of flattening everything to 502.
func writeLiveSTTError(w http.ResponseWriter, err error) {
	var liveErr *liveSTTError
	if errors.As(err, &liveErr) {
		handlerutil.WriteJSONError(w, liveErr.status, liveErr.message)
		return
	}
	handlerutil.WriteJSONError(w, http.StatusBadGateway, err.Error())
}
