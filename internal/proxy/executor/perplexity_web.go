package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/log"
	"9router/proxy/internal/proxy"
)

// Perplexity Web (perplexity.ai via a signed-in browser session).
//
// The registry entry carried a BaseURL, a cookie-shaped credential and a model
// list, but no executor — so a request fell through to ForwardOpenAI and POSTed
// an OpenAI body at the SSE endpoint, which answers neither.
//
// The lane is not OpenAI-compatible in either direction: the request is a
// query_str envelope, and the answer arrives as Perplexity's own JSON
// event stream whose content lives in markdown blocks that must be accumulated
// and de-duplicated. Upstream does the same work in
// open-sse/executors/perplexity-web.js.

const (
	perplexityWebAPIVersion = "2.18"

	perplexityWebUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

// perplexityModelMap maps a 9router model id to (mode, model_preference).
var perplexityModelMap = map[string][2]string{
	"pplx-auto":     {"concise", "pplx_pro"},
	"pplx-sonar":    {"copilot", "experimental"},
	"pplx-gpt":      {"copilot", "gpt54"},
	"pplx-gemini":   {"copilot", "gemini31pro_high"},
	"pplx-sonnet":   {"copilot", "claude46sonnet"},
	"pplx-opus":     {"copilot", "claude46opus"},
	"pplx-nemotron": {"copilot", "nv_nemotron_3_super"},
}

// perplexityThinkingMap is the thinking variant, used when the request asks for
// reasoning.
var perplexityThinkingMap = map[string]string{
	"pplx-gpt":    "gpt54_thinking",
	"pplx-sonnet": "claude46sonnetthinking",
	"pplx-opus":   "claude46opusthinking",
}

var (
	rePplxCitations = regexp.MustCompile(`\[\d+\]`)
	rePplxGrokTag   = regexp.MustCompile(`(?s)<grok:[^>]*>.*?</grok:[^>]*>`)
	rePplxGrokSelf  = regexp.MustCompile(`<grok:[^>]*/>`)
	rePplxXMLDecl   = regexp.MustCompile(`<\?xml[^?]*\?>`)
	rePplxRespTag   = regexp.MustCompile(`(?i)</?response\b[^>]*>`)
	rePplxMultiSp   = regexp.MustCompile(` {2,}`)
	rePplxMultiNL   = regexp.MustCompile(`\n{3,}`)
)

// perplexitySession remembers the backend conversation id for a history so a
// follow-up continues the same thread. Upstream keeps the same cache keyed by
// an FNV-1a hash of the history.
const (
	pplxSessionMaxAge     = time.Hour
	pplxSessionMaxEntries = 200
)

type pplxSessionEntry struct {
	backendUUID string
	at          time.Time
}

var (
	pplxSessionMu sync.Mutex
	pplxSessions  = map[string]pplxSessionEntry{}
)

// pplxSessionKey is FNV-1a over the joined history, hex-encoded.
func pplxSessionKey(history []pplxMessage) string {
	var b strings.Builder
	for _, h := range history {
		b.WriteString(h.Role)
		b.WriteByte(':')
		b.WriteString(h.Content)
		b.WriteByte('\n')
	}
	h := uint32(0x811c9dc5)
	for i := 0; i < b.Len(); i++ {
		h ^= uint32(b.String()[i])
		h *= 0x01000193
	}
	return fmt.Sprintf("%08x", h)
}

func pplxSessionLookup(history []pplxMessage) string {
	if len(history) == 0 {
		return ""
	}
	key := pplxSessionKey(history)
	pplxSessionMu.Lock()
	defer pplxSessionMu.Unlock()
	entry, ok := pplxSessions[key]
	if !ok {
		return ""
	}
	if time.Since(entry.at) > pplxSessionMaxAge {
		delete(pplxSessions, key)
		return ""
	}
	return entry.backendUUID
}

func pplxSessionStore(history []pplxMessage, current, answer, backendUUID string) {
	if backendUUID == "" {
		return
	}
	full := append(append([]pplxMessage{}, history...), pplxMessage{Role: "user", Content: current}, pplxMessage{Role: "assistant", Content: answer})
	key := pplxSessionKey(full)
	pplxSessionMu.Lock()
	defer pplxSessionMu.Unlock()
	pplxSessions[key] = pplxSessionEntry{backendUUID: backendUUID, at: time.Now()}
	if len(pplxSessions) > pplxSessionMaxEntries {
		var oldestKey string
		var oldest time.Time
		for k, v := range pplxSessions {
			if oldest.IsZero() || v.at.Before(oldest) {
				oldest, oldestKey = v.at, k
			}
		}
		if oldestKey != "" {
			delete(pplxSessions, oldestKey)
		}
	}
}

type pplxMessage struct {
	Role    string
	Content string
}

// pplxParsed is the conversation split the request envelope needs: the system
// prompt, the turns before this one, and the question being asked now.
type pplxParsed struct {
	system  string
	history []pplxMessage
	current string
}

func parsePplxMessages(raw any) pplxParsed {
	var out pplxParsed
	msgs, ok := raw.([]any)
	if !ok {
		return out
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "developer" {
			role = "system"
		}
		content := pplxContentText(msg["content"])
		if strings.TrimSpace(content) == "" {
			continue
		}
		switch role {
		case "system":
			out.system += content + "\n"
		case "user", "assistant":
			out.history = append(out.history, pplxMessage{Role: role, Content: content})
		}
	}
	if n := len(out.history); n > 0 && out.history[n-1].Role == "user" {
		out.current = out.history[n-1].Content
		out.history = out.history[:n-1]
	}
	return out
}

// pplxContentText flattens a Chat content value to text.
func pplxContentText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := b["text"].(string); ok {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// cleanPplxResponse strips the artefacts Perplexity embeds in its markdown:
// citation markers, the <grok:…> tool tags, an XML declaration and response
// wrappers.
func cleanPplxResponse(text string, strip bool) string {
	t := text
	t = rePplxXMLDecl.ReplaceAllString(t, "")
	t = rePplxCitations.ReplaceAllString(t, "")
	t = rePplxGrokTag.ReplaceAllString(t, "")
	t = rePplxGrokSelf.ReplaceAllString(t, "")
	t = rePplxRespTag.ReplaceAllString(t, "")
	if strip {
		t = rePplxMultiSp.ReplaceAllString(t, " ")
		t = rePplxMultiNL.ReplaceAllString(t, "\n\n")
		t = strings.TrimSpace(t)
	}
	return t
}

// ForwardPerplexityWeb sends a Chat request through the web session and writes
// an OpenAI-shaped answer back.
func ForwardPerplexityWeb(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return fmt.Errorf("ForwardPerplexityWeb: parse body: %w", err)
	}
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "Missing or empty messages array")
		return nil
	}

	model := req.ModelName
	if _, after, cut := strings.Cut(model, "/"); cut {
		model = after
	}

	thinking := false
	if t, ok := body["thinking"].(bool); ok && t {
		thinking = true
	}
	if effort, ok := body["reasoning_effort"].(string); ok && effort != "" && effort != "none" {
		thinking = true
	}

	mode, pref := "copilot", model
	if thinking {
		if mapped, ok := perplexityThinkingMap[model]; ok {
			pref = mapped
		} else if m, ok := perplexityModelMap[model]; ok {
			mode, pref = m[0], m[1]
		}
	} else if m, ok := perplexityModelMap[model]; ok {
		mode, pref = m[0], m[1]
	}

	parsed := parsePplxMessages(body["messages"])
	followUp := pplxSessionLookup(parsed.history)
	query := buildPplxQuery(parsed, followUp, body["tools"])
	if strings.TrimSpace(query) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "Empty query after processing")
		return nil
	}

	envelope := buildPplxEnvelope(query, mode, pref, followUp)
	sendBody, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("ForwardPerplexityWeb: marshal envelope: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, req.Config.BaseURL, bytes.NewReader(sendBody))
	if err != nil {
		return fmt.Errorf("ForwardPerplexityWeb: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Origin", "https://www.perplexity.ai")
	httpReq.Header.Set("Referer", "https://www.perplexity.ai/")
	httpReq.Header.Set("User-Agent", perplexityWebUA)
	httpReq.Header.Set("X-App-ApiClient", "default")
	httpReq.Header.Set("X-App-ApiVersion", perplexityWebAPIVersion)
	// The connection stores either a session cookie (the usual case) or an
	// access token; upstream accepts both and picks whichever is present.
	if token, ok := req.ConnData["accessToken"].(string); ok && token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	} else {
		httpReq.Header.Set("Cookie", "__Secure-next-auth.session-token="+req.APIKey)
	}

	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ForwardPerplexityWeb: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("Perplexity returned HTTP %d", resp.StatusCode)
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			msg = "Perplexity auth failed — the session cookie may be expired. Re-paste your __Secure-next-auth.session-token."
		case http.StatusTooManyRequests:
			msg = "Perplexity rate limited. Wait a moment and retry."
		}
		log.Warn("perplexity-web", "upstream refused", "status", resp.StatusCode, "model", model)
		writeOpenAIError(w, resp.StatusCode, msg)
		return nil
	}

	if req.IsStream {
		return streamPplxAsOpenAI(ctx, w, resp.Body, model, parsed)
	}
	return replayPplxAsOpenAI(ctx, w, resp.Body, model, parsed)
}

// buildPplxQuery assembles the single string Perplexity takes as its question:
// the system prompt, a tool hint, and — only on the first turn — the history.
func buildPplxQuery(p pplxParsed, followUp string, tools any) string {
	if followUp != "" {
		return p.current
	}
	var b strings.Builder
	if s := strings.TrimSpace(p.system); s != "" {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	if hint := pplxToolsHint(tools); hint != "" {
		b.WriteString(hint)
		b.WriteByte('\n')
	}
	b.WriteString("You have built-in web search. Answer questions directly using search results.")
	obj := map[string]any{"instructions": b.String()}
	if len(p.history) > 0 {
		obj["history"] = p.history
	}
	if p.current != "" {
		obj["query"] = p.current
	} else if len(p.history) == 0 {
		obj["query"] = ""
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return p.current
	}
	s := string(out)
	// Perplexity caps the accepted query length; upstream keeps the tail
	// because the question is at the end.
	if len(s) > 96000 {
		s = s[len(s)-96000:]
	}
	return s
}

func pplxToolsHint(tools any) string {
	arr, ok := tools.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available tools (reference only, cannot invoke):\n")
	for _, t := range arr {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn := m
		if inner, ok := m["function"].(map[string]any); ok {
			fn = inner
		}
		name, _ := fn["name"].(string)
		if name == "" {
			name = "unnamed"
		}
		desc, _ := fn["description"].(string)
		if idx := strings.IndexByte(desc, '\n'); idx >= 0 {
			desc = desc[:idx]
		}
		if len(desc) > 200 {
			desc = desc[:200]
		}
		b.WriteString("- " + name + ": " + desc + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildPplxEnvelope is the request shape the web endpoint accepts.
func buildPplxEnvelope(query, mode, pref, followUp string) map[string]any {
	return map[string]any{
		"query_str": query,
		"params": map[string]any{
			"query_str":             query,
			"search_focus":          "internet",
			"mode":                  mode,
			"model_preference":      pref,
			"sources":               []string{"web"},
			"attachments":           []any{},
			"frontend_uuid":         randomUUID(),
			"frontend_context_uuid": randomUUID(),
			"version":               perplexityWebAPIVersion,
			"language":              "en-US",
			"timezone":              "UTC",
			"search_recency_filter": nil,
			"is_incognito":          true,
			"use_schematized_api":   true,
			"last_backend_uuid":     nullableString(followUp),
		},
	}
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// randomUUID builds a v4 UUID. crypto/rand is the only source; a request id
// that collides across users is the one thing this identifier exists to avoid.
func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}

// pplxEvent is one decoded event of Perplexity's stream.
type pplxEvent struct {
	backendUUID string
	delta       string
	thinking    string
	done        bool
	err         string
	answer      string
}

// readPplxEvents walks the JSON event stream and yields content as it arrives.
// The stream is `data: {json}` frames separated by blank lines, terminated by
// `event: end_of_stream` or a `[DONE]` payload.
func readPplxEvents(r io.Reader, emit func(pplxEvent) bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var dataLines []string
	fullAnswer := ""
	seenLen := 0
	backendUUID := ""
	seenThinking := map[string]bool{}

	flush := func() bool {
		if len(dataLines) == 0 {
			return true
		}
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		if payload == "" || payload == "[DONE]" {
			return false
		}
		var ev map[string]any
		if json.Unmarshal([]byte(payload), &ev) != nil {
			return true
		}
		if code, ok := ev["error_code"]; ok && code != nil {
			msg, _ := ev["error_message"].(string)
			if msg == "" {
				msg = fmt.Sprintf("Perplexity error: %v", code)
			}
			emit(pplxEvent{err: msg, done: true})
			return false
		}
		if v, ok := ev["backend_uuid"].(string); ok && v != "" {
			backendUUID = v
		}
		blocks, _ := ev["blocks"].([]any)
		for _, blk := range blocks {
			block, ok := blk.(map[string]any)
			if !ok {
				continue
			}
			usage, _ := block["intended_usage"].(string)

			if usage == "pro_search_steps" {
				if plan, ok := block["plan_block"].(map[string]any); ok {
					if steps, ok := plan["steps"].([]any); ok {
						for _, st := range steps {
							step, ok := st.(map[string]any)
							if !ok {
								continue
							}
							switch step["step_type"] {
							case "SEARCH_WEB":
								if web, ok := step["search_web_content"].(map[string]any); ok {
									if queries, ok := web["queries"].([]any); ok {
										for _, q := range queries {
											qm, ok := q.(map[string]any)
											if !ok {
												continue
											}
											if s, _ := qm["query"].(string); s != "" && !seenThinking[s] {
												seenThinking[s] = true
												if !emit(pplxEvent{thinking: "Searching: " + s, backendUUID: backendUUID}) {
													return false
												}
											}
										}
									}
								}
							case "READ_RESULTS":
								if rr, ok := step["read_results_content"].(map[string]any); ok {
									if urls, ok := rr["urls"].([]any); ok {
										for i, u := range urls {
											if i >= 3 {
												break
											}
											if s, _ := u.(string); s != "" && !seenThinking[s] {
												seenThinking[s] = true
												if !emit(pplxEvent{thinking: "Reading: " + s, backendUUID: backendUUID}) {
													return false
												}
											}
										}
									}
								}
							}
						}
					}
				}
			}

			if usage == "plan" {
				if plan, ok := block["plan_block"].(map[string]any); ok {
					if goals, ok := plan["goals"].([]any); ok {
						for _, g := range goals {
							gm, ok := g.(map[string]any)
							if !ok {
								continue
							}
							if s, _ := gm["description"].(string); s != "" && !seenThinking[s] {
								seenThinking[s] = true
								if !emit(pplxEvent{thinking: s, backendUUID: backendUUID}) {
									return false
								}
							}
						}
					}
				}
			}

			if !strings.Contains(usage, "markdown") {
				continue
			}
			mb, ok := block["markdown_block"].(map[string]any)
			if !ok {
				continue
			}
			chunks, _ := mb["chunks"].([]any)
			if len(chunks) == 0 {
				continue
			}
			var joined strings.Builder
			for _, c := range chunks {
				if s, ok := c.(string); ok {
					joined.WriteString(s)
				}
			}
			chunkText := joined.String()
			if progress, _ := mb["progress"].(string); progress == "DONE" {
				fullAnswer = chunkText
				continue
			}
			cumulative := fullAnswer + chunkText
			if len(cumulative) > seenLen {
				delta := cumulative[seenLen:]
				fullAnswer = cumulative
				seenLen = len(cumulative)
				if !emit(pplxEvent{delta: delta, answer: fullAnswer, backendUUID: backendUUID}) {
					return false
				}
			}
		}

		if len(blocks) == 0 {
			if t, ok := ev["text"].(string); ok {
				t = strings.TrimSpace(t)
				if len(t) > seenLen {
					delta := t[seenLen:]
					fullAnswer = t
					seenLen = len(t)
					if !emit(pplxEvent{delta: delta, answer: fullAnswer, backendUUID: backendUUID}) {
						return false
					}
				}
			}
		}

		if final, _ := ev["final"].(bool); final {
			return false
		}
		if status, _ := ev["status"].(string); status == "COMPLETED" {
			return false
		}
		return true
	}

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if !flush() {
				emit(pplxEvent{delta: "", answer: fullAnswer, backendUUID: backendUUID, done: true})
				return
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimLeft(strings.TrimPrefix(line, "data:"), " "))
			continue
		}
		if line == "event: end_of_stream" {
			emit(pplxEvent{delta: "", answer: fullAnswer, backendUUID: backendUUID, done: true})
			return
		}
	}
	if len(dataLines) > 0 {
		flush()
	}
	emit(pplxEvent{delta: "", answer: fullAnswer, backendUUID: backendUUID, done: true})
}

// streamPplxAsOpenAI re-emits Perplexity's events as OpenAI chat chunks.
func streamPplxAsOpenAI(ctx context.Context, w http.ResponseWriter, body io.Reader, model string, parsed pplxParsed) error {
	flusher := proxy.WriteSSEHeaders(w)
	id := "chatcmpl-pplx-" + randomUUID()[:12]
	created := time.Now().Unix()

	writeChunk := func(delta map[string]any, finish any) {
		chunk := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created,
			"model": model, "system_fingerprint": nil,
			"choices": []any{map[string]any{
				"index": 0, "delta": delta, "finish_reason": finish, "logprobs": nil,
			}},
		}
		raw, err := json.Marshal(chunk)
		if err != nil {
			return
		}
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(raw)
		_, _ = w.Write([]byte("\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}

	writeChunk(map[string]any{"role": "assistant"}, nil)

	var answer, backendUUID string
	readPplxEvents(body, func(ev pplxEvent) bool {
		if ctx.Err() != nil {
			return false
		}
		if ev.backendUUID != "" {
			backendUUID = ev.backendUUID
		}
		if ev.err != "" {
			writeChunk(map[string]any{"content": "[Error: " + ev.err + "]"}, nil)
			return false
		}
		if ev.thinking != "" {
			writeChunk(map[string]any{"reasoning_content": ev.thinking + "\n"}, nil)
			return true
		}
		if ev.done {
			answer = ev.answer
			return false
		}
		if ev.answer != "" {
			answer = ev.answer
		}
		if ev.delta != "" {
			if cleaned := cleanPplxResponse(ev.delta, false); cleaned != "" {
				writeChunk(map[string]any{"content": cleaned}, nil)
			}
		}
		return true
	})

	writeChunk(map[string]any{}, "stop")
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	pplxSessionStore(parsed.history, parsed.current, cleanPplxResponse(answer, true), backendUUID)
	return nil
}

// replayPplxAsOpenAI aggregates the stream into one chat.completion.
func replayPplxAsOpenAI(ctx context.Context, w http.ResponseWriter, body io.Reader, model string, parsed pplxParsed) error {
	var answer, backendUUID string
	var thinking []string
	readPplxEvents(body, func(ev pplxEvent) bool {
		if ctx.Err() != nil {
			return false
		}
		if ev.backendUUID != "" {
			backendUUID = ev.backendUUID
		}
		if ev.err != "" {
			writeOpenAIError(w, http.StatusBadGateway, ev.err)
			return false
		}
		if ev.thinking != "" {
			thinking = append(thinking, ev.thinking)
			return true
		}
		if ev.done {
			answer = ev.answer
			return false
		}
		if ev.answer != "" {
			answer = ev.answer
		}
		return true
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}

	answer = cleanPplxResponse(answer, true)
	pplxSessionStore(parsed.history, parsed.current, answer, backendUUID)

	msg := map[string]any{"role": "assistant", "content": answer}
	if len(thinking) > 0 {
		msg["reasoning_content"] = strings.Join(thinking, "\n")
	}
	promptTokens := (len(parsed.current) + 3) / 4
	completionTokens := (len(answer) + 3) / 4
	payload := map[string]any{
		"id": "chatcmpl-pplx-" + randomUUID()[:12], "object": "chat.completion",
		"created": time.Now().Unix(), "model": model, "system_fingerprint": nil,
		"choices": []any{map[string]any{
			"index": 0, "message": msg, "finish_reason": "stop", "logprobs": nil,
		}},
		"usage": map[string]any{
			"prompt_tokens": promptTokens, "completion_tokens": completionTokens,
			"total_tokens": promptTokens + completionTokens,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(raw)
	return err
}

// writeOpenAIError answers in the shape every OpenAI client understands.
func writeOpenAIError(w http.ResponseWriter, status int, message string) {
	raw, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "upstream_error"},
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
