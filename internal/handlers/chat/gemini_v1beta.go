package chat

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/log"
	"9router/proxy/internal/translator"
)

// Gemini API surface: GET /v1beta/models and
// POST /v1beta/models/{model}:generateContent|:streamGenerateContent.
//
// A Gemini SDK (the @google/genai client, the Python SDK, curl) speaks this
// protocol and nothing else. The gateway routed everything else but had no such
// route, so a Gemini client could not use it at all — the gap the audit called
// the most visible of the remaining ones.
//
// The shape of the handler is a translation sandwich: the Gemini body becomes a
// Chat body, the existing Chat pipeline answers it, and the answer becomes a
// GenerateContentResponse. Every part of the gateway a Chat request gets —
// model resolution, combos, key policy, fallback, usage accounting — therefore
// applies unchanged.

// GeminiPathAction extracts the model and the action from a v1beta path suffix.
// Accepted forms:
//
//	models/{model}:generateContent
//	models/{model}:streamGenerateContent
//	models/{provider}/{model}:generateContent
func GeminiPathAction(suffix string) (model string, stream bool, ok bool) {
	suffix = strings.TrimPrefix(suffix, "/")
	suffix = strings.TrimPrefix(suffix, "models/")
	if suffix == "" {
		return "", false, false
	}
	// The action is the last colon segment. A model id never contains one.
	idx := strings.LastIndexByte(suffix, ':')
	if idx <= 0 {
		return "", false, false
	}
	action := suffix[idx+1:]
	name := suffix[:idx]
	switch {
	case strings.HasPrefix(action, "streamGenerateContent"):
		return name, true, true
	case strings.HasPrefix(action, "generateContent"):
		return name, false, true
	default:
		return "", false, false
	}
}

// GeminiListResponse is what GET /v1beta/models answers: the Gemini SDK reads
// `models[].name` and `supportedGenerationMethods`.
type GeminiListResponse struct {
	Models []GeminiListModel `json:"models"`
}

// GeminiListModel is one entry of that list.
type GeminiListModel struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName,omitempty"`
	Description                string   `json:"description,omitempty"`
	Version                    string   `json:"version,omitempty"`
	InputTokenLimit            int      `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit           int      `json:"outputTokenLimit,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}

// HandleGeminiListModels serves GET /v1beta/models.
//
// The SDK calls this before it will send a turn. Without it a Gemini client
// reports "no models available" and never tries one.
func (h *ChatHandler) HandleGeminiListModels(w http.ResponseWriter, r *http.Request) {
	result := h.buildModelsListResult(r.Context(), modelsListModeFromQuery(r))
	visible, err := h.filterModelsByAccess(requestKey(r), result.Models)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := GeminiListResponse{Models: make([]GeminiListModel, 0, len(visible))}
	for _, m := range visible {
		// The SDK addresses a model by the id it was given, so the published
		// name carries the provider prefix the gateway routes on.
		id := m.ID
		if id == "" {
			continue
		}
		out.Models = append(out.Models, GeminiListModel{
			Name:        "models/" + id,
			DisplayName: id,
			Description: "Routed by 9router",
			SupportedGenerationMethods: []string{
				"generateContent",
				"streamGenerateContent",
			},
		})
	}
	handlerutil.WriteJSON(w, http.StatusOK, out)
}

// HandleGeminiGenerateContent serves the two POST actions.
func (h *ChatHandler) HandleGeminiGenerateContent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSONError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	defer func() { _ = r.Body.Close() }()

	suffix := strings.TrimPrefix(r.URL.Path, "/v1beta/models/")
	model, stream, ok := GeminiPathAction(suffix)
	if !ok {
		handlerutil.WriteJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{
				"code":    404,
				"message": "unknown model action: " + suffix,
				"status":  "NOT_FOUND",
			},
		})
		return
	}

	chatBody, err := translator.GeminiClientToChatRequest(body, model, stream)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}

	if stream {
		sw := newGeminiStreamWriter(w, model)
		inner := r.Clone(r.Context())
		inner.Body = io.NopCloser(bytes.NewReader(chatBody))
		inner.ContentLength = int64(len(chatBody))
		inner.URL.Path = "/v1/chat/completions"
		inner.Header.Set("Content-Type", "application/json")

		func() {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("gemini-v1beta", "chat pipeline panicked", "error", rec)
				}
			}()
			h.HandleChatCompletions(sw, inner)
		}()

		if sw.status == 0 {
			writeGeminiError(w, http.StatusBadGateway, "INTERNAL", "upstream produced no response")
			return
		}
		if sw.status != http.StatusOK {
			writeGeminiError(w, sw.status, geminiStatusName(sw.status), strings.TrimSpace(sw.errBuf.String()))
			return
		}
		sw.finish()
		return
	}

	// Non-streaming: capture full response and convert
	capture := &geminiCaptureWriter{header: http.Header{}}
	inner := r.Clone(r.Context())
	inner.Body = io.NopCloser(bytes.NewReader(chatBody))
	inner.ContentLength = int64(len(chatBody))
	inner.URL.Path = "/v1/chat/completions"
	inner.Header.Set("Content-Type", "application/json")

	func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("gemini-v1beta", "chat pipeline panicked", "error", rec)
			}
		}()
		h.HandleChatCompletions(capture, inner)
	}()

	if capture.status == 0 {
		writeGeminiError(w, http.StatusBadGateway, "INTERNAL", "upstream produced no response")
		return
	}
	if capture.status != http.StatusOK {
		writeGeminiError(w, capture.status, geminiStatusName(capture.status), capture.detail())
		return
	}

	converted, err := translator.ChatResponseToGemini(capture.buf.Bytes(), model)
	if err != nil {
		writeGeminiError(w, http.StatusBadGateway, "INTERNAL", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(converted)
}

// geminiStreamWriter streams translated Gemini SSE frames directly to the client
// as chunks arrive from upstream, without buffering the entire body.
type geminiStreamWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	model   string
	header  http.Header

	status      int
	headersSent bool
	lineBuf     bytes.Buffer
	errBuf      bytes.Buffer
}

func newGeminiStreamWriter(w http.ResponseWriter, model string) *geminiStreamWriter {
	flusher, _ := w.(http.Flusher)
	return &geminiStreamWriter{
		w:       w,
		flusher: flusher,
		model:   model,
		header:  make(http.Header),
	}
}

func (s *geminiStreamWriter) Header() http.Header {
	return s.header
}

func (s *geminiStreamWriter) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	if status == http.StatusOK && !s.headersSent {
		s.w.Header().Set("Content-Type", "text/event-stream")
		s.w.Header().Set("Cache-Control", "no-cache")
		s.w.Header().Set("Connection", "keep-alive")
		s.w.WriteHeader(http.StatusOK)
		s.headersSent = true
		if s.flusher != nil {
			s.flusher.Flush()
		}
	}
}

func (s *geminiStreamWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.WriteHeader(http.StatusOK)
	}
	if s.status != http.StatusOK {
		s.errBuf.Write(b)
		return len(b), nil
	}

	if !s.headersSent {
		s.WriteHeader(http.StatusOK)
	}

	s.lineBuf.Write(b)
	for {
		lineBytes, err := s.lineBuf.ReadBytes('\n')
		if err != nil {
			// Incomplete line: restore into lineBuf
			s.lineBuf.Write(lineBytes)
			break
		}
		s.processLine(lineBytes)
	}
	return len(b), nil
}

func (s *geminiStreamWriter) processLine(lineBytes []byte) {
	line := strings.TrimSpace(string(lineBytes))
	if !strings.HasPrefix(line, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	// Drop the OpenAI sentinel: Gemini ends by closing.
	if payload == "" || payload == "[DONE]" {
		return
	}
	frame, ok := translator.ChatChunkToGemini([]byte(payload), s.model)
	if !ok {
		return
	}
	_, _ = s.w.Write([]byte("data: "))
	_, _ = s.w.Write(frame)
	_, _ = s.w.Write([]byte("\n\n"))
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

func (s *geminiStreamWriter) finish() {
	if s.lineBuf.Len() > 0 {
		s.processLine(s.lineBuf.Bytes())
		s.lineBuf.Reset()
	}
}

// geminiCaptureWriter records what the Chat pipeline wrote so it can be
// translated. Headers are captured too: a missing response header is how the
// pipeline signals a failure it never wrote a body for.
type geminiCaptureWriter struct {
	header http.Header
	buf    bytes.Buffer
	status int
}

func (c *geminiCaptureWriter) Header() http.Header { return c.header }

func (c *geminiCaptureWriter) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *geminiCaptureWriter) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return c.buf.Write(p)
}

// Flush satisfies http.Flusher so a streaming pipeline does not type-assert
// itself into a different path.
func (c *geminiCaptureWriter) Flush() {}

// detail returns the captured body as a string, trimmed to what an error
// message can usefully carry.
func (c *geminiCaptureWriter) detail() string {
	s := strings.TrimSpace(c.buf.String())
	if len(s) > 800 {
		s = s[:800]
	}
	return s
}

// writeGeminiError answers in the shape the Gemini SDK parses.
func writeGeminiError(w http.ResponseWriter, status int, code, message string) {
	handlerutil.WriteJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": message,
			"status":  code,
		},
	})
}

// geminiStatusName maps an HTTP status onto Gemini's status vocabulary, which
// the SDK switches on.
func geminiStatusName(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	default:
		return "INTERNAL"
	}
}
