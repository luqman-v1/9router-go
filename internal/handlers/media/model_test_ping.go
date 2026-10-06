package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"9router/proxy/internal/handlerutil"
	"9router/proxy/internal/translator"
)

// The dashboard's per-model Test button pings a model through the same endpoint
// its kind serves, because a kind's contract IS that endpoint: a System One
// model answers structured questions on /v1/systemone and rejects a Chat
// Completions probe with its own 500, while the Run button beside it posts to
// that endpoint and succeeds. Upstream src/app/api/models/test/ping.js
// dispatches on `kind` (commit 20014b31, "probe System One models through
// /v1/systemone"); this file is that dispatch.
//
// Every probe runs back through the handler that owns the kind, so the verdict
// is read from the response a real client of that endpoint would see rather
// than from a private copy of the forwarding logic.

// testModelTimeout bounds one probe. A provider that never answers (no chat
// lane, a STT model handed an upload it cannot read) must not hold the button.
const testModelTimeout = 15 * time.Second

const (
	// probeMaxTokens is generous on purpose: a reasoning model spends a small
	// budget on chain-of-thought and a starved probe reports "no choices" for a
	// model that is working (upstream issue #3010).
	probeMaxTokens   = 1024
	probeTextLimit   = 240
	probeBodyReadCap = 8 << 20
)

type testModelRequest struct {
	Model string `json:"model"`
	Kind  string `json:"kind"`
}

// HandleTestModel handles POST /api/models/test — the dashboard's per-model
// Test button. Body: {"model":"<provider>/<model>","kind":"<service kind>"},
// where an absent kind means a plain LLM chat probe.
func (h *MediaHandler) HandleTestModel(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		handlerutil.WriteJSON(w, http.StatusBadRequest, testModelResult{Error: "failed to read body"})
		return
	}
	defer r.Body.Close()

	var req testModelRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		handlerutil.WriteJSON(w, http.StatusBadRequest, testModelResult{Error: "Model required"})
		return
	}
	if !isProbeKind(req.Kind) {
		handlerutil.WriteJSON(w, http.StatusBadRequest, testModelResult{Error: "unsupported model kind: " + req.Kind})
		return
	}

	handlerutil.WriteJSON(w, http.StatusOK, h.probeModel(r, req.Model, req.Kind))
}

// probeModel runs one probe and turns whatever the kind's handler answered into
// a verdict. A 200 is not a pass: a passthrough endpoint copies the upstream
// status through, so a provider that answers 200 with an empty or error-shaped
// body has to be read out of the payload, not the status line.
func (h *MediaHandler) probeModel(r *http.Request, model, kind string) testModelResult {
	start := time.Now()
	payload := probeFor(kind, model)
	ctx, cancel := context.WithTimeout(r.Context(), testModelTimeout)
	defer cancel()

	probe, err := http.NewRequestWithContext(ctx, http.MethodPost, probePath(kind), payload.body)
	if err != nil {
		return testModelResult{Error: err.Error()}
	}
	if payload.contentType != "" {
		probe.Header.Set("Content-Type", payload.contentType)
	}

	rec := httptest.NewRecorder()
	h.dispatchProbe(rec, probe, kind)

	result := readProbeResult(rec, kind)
	result.LatencyMs = time.Since(start).Milliseconds()
	return result
}

// dispatchProbe hands the probe to the handler that owns the kind.
func (h *MediaHandler) dispatchProbe(rec *httptest.ResponseRecorder, probe *http.Request, kind string) {
	switch kind {
	case "embedding":
		h.HandleEmbeddings(rec, probe)
	case "image":
		h.HandleImages(rec, probe)
	case "tts":
		h.HandleAudioSpeech(rec, probe)
	case "stt":
		h.HandleAudioTranscriptions(rec, probe)
	case "video":
		h.HandleVideoGenerations(rec, probe)
	case "systemone":
		h.HandleSystemone(rec, probe)
	default:
		h.ChatH.HandleChatCompletions(rec, probe)
	}
}

// isProbeKind reports whether the dashboard asked for something this handler
// knows how to probe. "" is the LLM default and is always probeable.
func isProbeKind(kind string) bool {
	switch kind {
	case "", "embedding", "image", "tts", "stt", "video", "systemone":
		return true
	}
	return false
}

// probePath names the endpoint a probe is dispatched through, so a failure
// reads as "the wrong lane" instead of an anonymous probe.
func probePath(kind string) string {
	switch kind {
	case "embedding":
		return "/embeddings"
	case "image":
		return "/images/generations"
	case "tts":
		return "/audio/speech"
	case "stt":
		return "/audio/transcriptions"
	case "video":
		return "/videos/generations"
	case "systemone":
		return "/systemone"
	}
	return "/chat/completions"
}

// probePayload is a probe body plus the content type it must be sent with.
// Multipart probes carry their own type, which is why it is not a constant.
type probePayload struct {
	body        io.Reader
	contentType string
}

// probeFor builds the request the named kind would receive from a real client.
// The payloads ask something the provider can actually answer: a probe that
// asks for nothing answers a working credential with a false failure.
func probeFor(kind, model string) probePayload {
	switch kind {
	case "embedding":
		return jsonProbe(map[string]any{"model": model, "input": "test"})
	case "image", "video":
		return jsonProbe(map[string]any{"model": model, "prompt": "test"})
	case "tts":
		return jsonProbe(map[string]any{
			"model": model,
			"input": "Hello, this is a probe.",
			"voice": "alloy",
		})
	case "stt":
		buf := &bytes.Buffer{}
		mw := multipart.NewWriter(buf)
		_ = mw.WriteField("model", model)
		if file, err := mw.CreateFormFile("file", "probe.wav"); err == nil {
			_, _ = file.Write(silentWAV())
		}
		_ = mw.Close()
		return probePayload{body: buf, contentType: mw.FormDataContentType()}
	case "systemone":
		return jsonProbe(map[string]any{
			"model": model,
			"state": "Customer: I was charged twice for my order this morning.",
			"questions": map[string]any{
				"probe": map[string]any{
					"type":         "noul",
					"instructions": "Is the customer reporting a billing problem?",
				},
			},
		})
	}
	return jsonProbe(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": probeMaxTokens,
		"stream":     false,
	})
}

func jsonProbe(payload map[string]any) probePayload {
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte("{}")
	}
	return probePayload{body: bytes.NewReader(encoded), contentType: "application/json"}
}

// silentWAV builds a 250 ms silent 16 kHz mono WAV. A provider rejects an
// empty or wrong-format upload, and that rejection would report the credential
// as broken when the model itself is fine.
func silentWAV() []byte {
	const (
		sampleRate    = 16000
		channels      = 1
		bitsPerSample = 16
		durationMs    = 250
	)
	sampleCount := max(1, sampleRate*durationMs/1000)
	dataSize := sampleCount * channels * bitsPerSample / 8

	buf := make([]byte, 44+dataSize)
	copy(buf[0:], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+dataSize))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:], 16) // PCM header size
	binary.LittleEndian.PutUint16(buf[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(buf[22:], channels)
	binary.LittleEndian.PutUint32(buf[24:], sampleRate)
	binary.LittleEndian.PutUint32(buf[28:], sampleRate*channels*bitsPerSample/8)
	binary.LittleEndian.PutUint16(buf[32:], channels*bitsPerSample/8)
	binary.LittleEndian.PutUint16(buf[34:], bitsPerSample)
	copy(buf[36:], "data")
	binary.LittleEndian.PutUint32(buf[40:], uint32(dataSize))
	return buf
}

// testModelResult is the dashboard verdict. It mirrors upstream's
// {ok, latencyMs, error, status, note} so the SPA can render the server's own
// measurement instead of its browser clock.
type testModelResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	Status    int    `json:"status,omitempty"`
	Note      string `json:"note,omitempty"`
	Error     string `json:"error,omitempty"`
}

// readProbeResult decides pass or fail from the probe response.
func readProbeResult(rec *httptest.ResponseRecorder, kind string) testModelResult {
	status := rec.Code
	body, _ := io.ReadAll(io.LimitReader(rec.Body, probeBodyReadCap))
	parsed := parseProbeBody(body)

	if status != http.StatusOK {
		return testModelResult{
			Status: status,
			Error:  "HTTP " + strconv.Itoa(status) + probeDetail(probeErrorText(body, parsed)),
		}
	}
	return verdictForKind(kind, parsed, status)
}

// verdictForKind answers "did this endpoint return the thing it is for".
func verdictForKind(kind string, parsed probeBody, status int) testModelResult {
	switch kind {
	case "embedding":
		if !parsed.hasEmbedding() {
			return testModelResult{Status: status, Error: "Provider returned no embedding data"}
		}
	case "image":
		if !parsed.hasImages() {
			return testModelResult{Status: status, Error: "Provider returned no image data for this model"}
		}
	case "video":
		// A video job endpoint answers with the job it created, not with media:
		// an id, a status, or a remote url. Only a body carrying none of those
		// is a probe that found nothing.
		if !parsed.hasVideoJob() {
			return testModelResult{Status: status, Error: "Provider returned no video job for this model"}
		}
	case "tts":
		if !parsed.hasAudio() {
			return testModelResult{Status: status, Error: "Provider returned no audio for this model"}
		}
	case "stt":
		if !parsed.hasText() {
			return testModelResult{Status: status, Error: "Provider returned no transcription text for this model"}
		}
	case "systemone":
		if !parsed.hasAnswers() {
			return testModelResult{Status: status, Error: "Provider returned no answers for this model"}
		}
	default:
		return chatVerdict(parsed, status)
	}
	return testModelResult{OK: true, Status: status}
}

// chatVerdict reads the Chat Completions shape. The rejections matter in order:
// an error envelope under HTTP 200 (a gateway in front of the provider
// answering for it), a provider-reported status that is not success, then no
// completion at all. A length-limited reasoning reply is a pass: that model
// spent its budget on chain-of-thought and said so, which is a working
// connection (issue #3010).
func chatVerdict(parsed probeBody, status int) testModelResult {
	if msg, ok := parsed.errorMessage(); ok {
		return testModelResult{Status: status, Error: truncateDetail(msg, probeTextLimit)}
	}
	if ps, ok := parsed.providerStatusError(); ok {
		return testModelResult{
			Status: status,
			Error:  "Provider status " + ps.status + ": " + truncateDetail(ps.text, probeTextLimit),
		}
	}
	choice, ok := parsed.firstChoice()
	if !ok {
		return testModelResult{Status: status, Error: "Provider returned no completion choices for this model"}
	}
	if choice.finishReason == "length" && choice.reasoningOnly() {
		return testModelResult{OK: true, Status: status, Note: "reasoning-only response (length-limited)"}
	}
	return testModelResult{OK: true, Status: status}
}

// probeBody is a probe response decoded per kind on demand, so one probe never
// pays for a field only another kind reads.
type probeBody struct {
	raw    []byte
	object map[string]jsontext.Value
}

func parseProbeBody(raw []byte) probeBody {
	// Cline and Clinepass wrap a non-streaming reply in {"success":true,"data":…}.
	// The wrapper carries no choices, so without unwrapping a working provider
	// reads as an empty completion.
	raw = translator.UnwrapClineEnvelope(raw)
	body := probeBody{raw: raw}
	if json.Unmarshal(raw, &body.object) != nil {
		body.object = nil
	}
	return body
}

func (b probeBody) hasEmbedding() bool {
	items, ok := b.array("data")
	if !ok {
		return false
	}
	var vectors []struct {
		Embedding []float64 `json:"embedding"`
	}
	if json.Unmarshal(items, &vectors) != nil {
		return false
	}
	return len(vectors) > 0 && len(vectors[0].Embedding) > 0
}

func (b probeBody) hasImages() bool {
	items, ok := b.array("data")
	if !ok {
		return false
	}
	var entries []struct {
		URL     string         `json:"url"`
		B64JSON jsontext.Value `json:"b64_json"`
	}
	if json.Unmarshal(items, &entries) != nil || len(entries) == 0 {
		return false
	}
	first := entries[0]
	return first.URL != "" || len(first.B64JSON) > 0
}

// hasVideoJob reports a created video job: an id, a queued status, or a URL to
// the finished recording. A job endpoint returns the job, never a data array.
func (b probeBody) hasVideoJob() bool {
	if b.hasImages() {
		return true
	}
	return b.fieldString("id") != "" ||
		b.fieldString("video_url") != "" ||
		b.fieldString("url") != "" ||
		b.fieldString("status") != ""
}

// hasAudio accepts both speech shapes: the binary recording the handler streams
// straight through, and the {"audio":"base64"} envelope a provider returns
// under response_format=json.
func (b probeBody) hasAudio() bool {
	if b.object == nil {
		// An audio/wav body is the answer: the handler streams the recording
		// itself, so there is no JSON envelope to read a field out of.
		return len(bytes.TrimSpace(b.raw)) > 0
	}
	return b.fieldString("audio") != ""
}

func (b probeBody) hasText() bool {
	return strings.TrimSpace(b.fieldString("text")) != ""
}

func (b probeBody) hasAnswers() bool {
	raw, ok := b.field("answers")
	if !ok || raw[0] != '{' {
		return false
	}
	var answers map[string]jsontext.Value
	if json.Unmarshal(raw, &answers) != nil {
		return false
	}
	return len(answers) > 0
}

// errorMessage reports an `error` field carried under HTTP 200. The field is
// either a string or an object carrying a message.
func (b probeBody) errorMessage() (string, bool) {
	raw, ok := b.field("error")
	if !ok || len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	if msg := b.fieldString("error"); msg != "" {
		return msg, true
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		return obj.Message, true
	}
	return string(raw), true
}

type providerStatus struct {
	status string
	text   string
}

// providerStatusError reports a provider that answered 200 carrying a status of
// its own that is not success — a gateway in front of it reporting the upstream
// failure without changing the status line.
func (b probeBody) providerStatusError() (providerStatus, bool) {
	status := b.fieldString("status")
	switch status {
	case "", "200", "0":
		return providerStatus{}, false
	}
	for _, key := range []string{"msg", "message"} {
		if msg := b.fieldString(key); msg != "" {
			return providerStatus{status: status, text: msg}, true
		}
	}
	return providerStatus{}, false
}

// field returns a top-level field's raw JSON, so a reader can tell an absent
// field from a null one without decoding the whole envelope.
func (b probeBody) field(key string) (jsontext.Value, bool) {
	if b.object == nil {
		return nil, false
	}
	raw, ok := b.object[key]
	return raw, ok
}

// fieldString reads a top-level string field, returning "" for a field that is
// missing or holds another type.
func (b probeBody) fieldString(key string) string {
	raw, ok := b.field(key)
	if !ok {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

type probeChoice struct {
	content      string
	reasoning    string
	toolCalls    []jsontext.Value
	finishReason string
}

func (b probeBody) firstChoice() (probeChoice, bool) {
	choices, ok := b.array("choices")
	if !ok {
		return probeChoice{}, false
	}
	var items []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content          any              `json:"content"`
			Reasoning        jsontext.Value   `json:"reasoning"`
			ReasoningContent jsontext.Value   `json:"reasoning_content"`
			Thinking         jsontext.Value   `json:"thinking"`
			ThinkingContent  jsontext.Value   `json:"thinking_content"`
			ToolCalls        []jsontext.Value `json:"tool_calls"`
		} `json:"message"`
	}
	if json.Unmarshal(choices, &items) != nil || len(items) == 0 {
		return probeChoice{}, false
	}

	first := items[0]
	choice := probeChoice{
		finishReason: first.FinishReason,
		content:      probeText(first.Message.Content),
		toolCalls:    first.Message.ToolCalls,
	}
	for _, raw := range []jsontext.Value{
		first.Message.Reasoning,
		first.Message.ReasoningContent,
		first.Message.Thinking,
		first.Message.ThinkingContent,
	} {
		if text := jsonTextString(raw); text != "" {
			choice.reasoning = text
			break
		}
	}
	return choice, true
}

// jsonTextString unquotes a raw JSON value that holds a string, returning ""
// for null, an object or an array — none of which is a reasoning trace.
func jsonTextString(raw jsontext.Value) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// reasoningOnly reports a message that spent its whole budget thinking and said
// nothing — a truncated answer on a working connection, not an empty one.
func (c probeChoice) reasoningOnly() bool {
	return c.reasoning != "" && strings.TrimSpace(c.content) == "" && len(c.toolCalls) == 0
}

// probeText flattens content, which is a string on OpenAI and an array of typed
// blocks on a Claude-shaped reply the gateway forwards unchanged.
func probeText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var text strings.Builder
		for _, block := range v {
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if str, ok := m["text"].(string); ok {
				text.WriteString(str)
			}
		}
		return text.String()
	}
	return ""
}

// array reads a top-level array field and returns its raw JSON.
func (b probeBody) array(key string) (jsontext.Value, bool) {
	raw, ok := b.field(key)
	if !ok || len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	return raw, true
}

// probeErrorText picks the most specific message out of an error payload: the
// gateway's {"error":{"message":…}} envelope first, then the flatter shapes a
// provider or a proxied error page produces, and the raw body last.
func probeErrorText(raw []byte, parsed probeBody) string {
	if msg, ok := parsed.errorMessage(); ok {
		return msg
	}
	for _, key := range []string{"msg", "message", "detail"} {
		if msg := parsed.fieldString(key); msg != "" {
			return msg
		}
	}
	return strings.TrimSpace(string(raw))
}

// probeDetail renders the ": <detail>" tail of an HTTP failure, truncated so a
// proxied HTML error page cannot fill a dashboard row.
func probeDetail(detail string) string {
	detail = truncateDetail(detail, probeTextLimit)
	if detail == "" {
		return ""
	}
	return ": " + detail
}

func truncateDetail(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
