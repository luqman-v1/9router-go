package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/log"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"

	"github.com/google/uuid"
)

// Perch (perchai.app) ships no public API: every plan reaches hosted models
// only through Perch's own Web / Desktop / CLI surfaces. The model-call proxy
// on app.perchai.app is the CLI's transport, and this executor speaks it:
// one signed turn ticket per model call, a Perch envelope, and Perch's own
// event stream back. Protocol reference: the perchai-cli bundle, cross-checked
// against ErfanBagheri404/PerchAI2API, zoeee1125/perchai2api and
// YoungJurry/pi-perchai (docs/protocol.md there).

const (
	perchTurnTicketPath = "/api/perch-terminal/turn-ticket"
	perchModelCallPath  = "/api/perch-terminal/model-call"
	perchCLIUserAgent   = "perchai-cli/2.4.125"
	perchTicketMargin   = 30 * time.Second
	// perchTurnCallBudget bounds model calls on one standard-profile turn
	// ticket (the CLI's own 40-call budget); a new user message opens a new
	// turn, so refusing past it is correct, not a missed refresh.
	perchTurnCallBudget = 40
)

// perchRoostChoices maps a 9router model id to the Roost tier the proxy takes
// for it. A tier routes through the account's Roost lane; anything else is a
// pinned manual model and travels as manualModelOptionId.
var perchRoostChoices = map[string]string{
	"perch-auto": "standard",
	"perch-pro":  "pro",
}

// perchTurnTicket is one minted ticket: the signed value, the run it binds,
// and when the signature stops being accepted.
type perchTurnTicket struct {
	ticket    string
	ticketID  string
	runID     string
	expiresAt time.Time
}

// perchTurnSlot serializes ticket use per (account, turn scope). The queue
// shares one tail promise so a burst of concurrent calls mints one ticket;
// each caller keeps its own context, so aborting one request cannot cancel
// another (pi-perchai's createTickets, same shape).
type perchTurnSlot struct {
	tail    chan struct{}
	mu      sync.Mutex
	current *perchTurnTicket
	calls   int
}

var (
	perchTurnMu    sync.Mutex
	perchTurnSlots = map[string]*perchTurnSlot{}
)

// perchTurnSlotFor returns the queue slot for one (token fingerprint, scope).
func perchTurnSlotFor(token, scope string) *perchTurnSlot {
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:]) + ":" + scope
	perchTurnMu.Lock()
	defer perchTurnMu.Unlock()
	if slot, ok := perchTurnSlots[key]; ok {
		return slot
	}
	// Bounded: a listener that never stops minting scopes would grow this
	// forever. 16 is far above one process's live turns.
	if len(perchTurnSlots) >= 16 {
		for k := range perchTurnSlots {
			delete(perchTurnSlots, k)
			break
		}
	}
	slot := &perchTurnSlot{tail: make(chan struct{})}
	slot.tail = make(chan struct{})
	close(slot.tail)
	perchTurnSlots[key] = slot
	return slot
}

// perchAcquireTurn reserves one call against the slot's ticket, minting or
// renewing it when it is inside the renewal margin. A failed call still
// counts against the budget: Perch meters the turn, not the success.
func perchAcquireTurn(ctx context.Context, client *http.Client, origin, token, scope string, baseURL string) (*perchTurnTicket, error) {
	slot := perchTurnSlotFor(token, scope)

	// The head of the queue runs immediately; everyone else waits for the
	// previous call to leave. The wait honors this caller's context only.
	slot.mu.Lock()
	turn := make(chan struct{})
	prev := slot.tail
	slot.tail = turn
	slot.mu.Unlock()

	select {
	case <-prev:
	case <-ctx.Done():
		close(turn)
		return nil, ctx.Err()
	}

	release := func() {
		close(turn)
	}
	defer release()

	slot.mu.Lock()
	t := slot.current
	calls := slot.calls
	slot.mu.Unlock()

	if t != nil && time.Until(t.expiresAt) <= perchTicketMargin {
		t = nil
	}
	if t == nil {
		var err error
		t, err = perchMintTicket(ctx, client, origin, token, baseURL)
		if err != nil {
			return nil, err
		}
		slot.mu.Lock()
		slot.current = t
		slot.mu.Unlock()
	}

	slot.mu.Lock()
	slot.calls = calls + 1
	calls++
	slot.mu.Unlock()
	if calls > perchTurnCallBudget {
		return nil, fmt.Errorf("Perch standard turn ticket call budget exhausted (%d); send a new user message", perchTurnCallBudget)
	}
	_ = calls
	return t, nil
}

// perchMintTicket POSTs one turn-ticket request. The 5xx enforced:false
// branch is the official CLI's advisory-ticket path: Perch itself tolerates
// the failure, so the call proceeds unticketed. Every other failure (auth,
// plan, rate limit, timeout, malformed body) is a real error and is never
// inferred away.
func perchMintTicket(ctx context.Context, client *http.Client, origin, token, baseURL string) (*perchTurnTicket, error) {
	body, err := json.Marshal(map[string]string{"surface": "cli", "profile": "standard"})
	if err != nil {
		return nil, fmt.Errorf("perch turn-ticket: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+perchTurnTicketPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("perch turn-ticket: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", perchCLIUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perch turn-ticket: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("perch turn-ticket: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var env map[string]any
		_ = json.Unmarshal(raw, &env)
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 && env["enforced"] == false {
			return nil, perchErrAdvisoryTicket
		}
		return nil, &perchHTTPError{Status: resp.StatusCode, Body: truncatePerchBody(raw), Code: perchErrorCode(env)}
	}

	var data struct {
		Ticket    string `json:"ticket"`
		TicketID  string `json:"ticketId"`
		RunID     string `json:"runId"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("perch turn-ticket: malformed response")
	}
	exp, err := time.Parse(time.RFC3339, data.ExpiresAt)
	if err != nil || data.Ticket == "" || data.TicketID == "" || data.RunID == "" || !exp.After(time.Now()) {
		return nil, fmt.Errorf("perch turn-ticket: incomplete or expired ticket")
	}
	return &perchTurnTicket{ticket: data.Ticket, ticketID: data.TicketID, runID: data.RunID, expiresAt: exp}, nil
}

var perchErrAdvisoryTicket = errors.New("perch: advisory ticket (enforced:false)")

// perchHTTPError carries an upstream refusal without inventing a retry.
type perchHTTPError struct {
	Status int
	Body   string
	Code   string
}

func (e *perchHTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("perch upstream %d %s", e.Status, e.Code)
	}
	return fmt.Sprintf("perch upstream %d", e.Status)
}

func perchErrorCode(env map[string]any) string {
	if env == nil {
		return ""
	}
	if c, ok := env["errorCode"].(string); ok {
		return c
	}
	if c, ok := env["code"].(string); ok {
		return c
	}
	return ""
}

func truncatePerchBody(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}

// perchOrigin resolves the app origin from the connection's BaseURL override
// when present; it never falls back to a foreign host for credentials.
func perchOrigin(baseURL string) string {
	if strings.HasPrefix(baseURL, "https://app.perchai.app") || strings.HasPrefix(baseURL, "http://127.0.0.1") || strings.HasPrefix(baseURL, "http://localhost") {
		// The registered BaseURL is the full model-call path; strip it.
		if idx := strings.Index(baseURL, perchModelCallPath); idx >= 0 {
			return baseURL[:idx]
		}
	}
	return providers.PerchAppURL
}

// perchTurnScope derives the ticket boundary from the conversation: a new
// user message opens a new turn, while tool iterations share the current one
// (pi-perchai's prepare(), same rule).
func perchTurnScope(messages []any) string {
	users := 0
	var last any
	for _, m := range messages {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if mm["role"] == "user" {
			users++
			last = mm["content"]
		}
	}
	if users == 0 {
		return "direct-" + uuid.NewString()
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%d:%v", users, last)))
	return fmt.Sprintf("turn-%d-%s", users, hex.EncodeToString(h[:])[:16])
}

// perchEffortLevel clamps the requested thinking level up to the nearest the
// model advertises, never advertising a level the model does not take.
func perchEffortLevel(model, requested string) string {
	order := []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}
	levels := perchEffortsFor(model)
	idx := 0
	for i, l := range order {
		if l == requested {
			idx = i
			break
		}
	}
	for _, want := range order[idx:] {
		for _, have := range levels {
			if have == want {
				return want
			}
		}
	}
	return levels[len(levels)-1]
}

func perchEffortsFor(model string) []string {
	switch model {
	case "openrouter-z-ai-glm-5-3-flash-crusoe":
		return []string{"low", "high", "max"}
	case "openrouter-qwen-qwen3-8-max-0902":
		return []string{"off", "low", "medium", "high"}
	case "openrouter-stealth-crouton-alpha":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "openrouter-deepseek-deepseek-v4-pro-crusoe":
		return []string{"off", "low", "high", "max"}
	case "openrouter-z-ai-glm-5-3-crusoe":
		return []string{"low", "high", "max"}
	case "wandb-minimax-m3":
		return []string{"off", "high"}
	case "anthropic-claude-opus-5-5", "anthropic-claude-sonnet-5-5", "azure-foundry-gpt-6-1-sol", "azure-foundry-gpt-6-1-terra":
		return []string{"off", "low", "medium", "high", "xhigh", "max"}
	case "gemini-gemini-4-argon":
		return []string{"low", "medium", "high"}
	default:
		return []string{"off", "low", "medium", "high"}
	}
}

// perchEnvelope builds the model-call body: messages and tools pass through
// under their request names (the proxy body's nested `request` keeps the
// caller's maxTokens / toolChoice spelling, not maxOutputTokens), and the
// model choice travels as a Roost tier or a pinned manual id.
func perchEnvelope(model string, reqBody map[string]any, runID string) map[string]any {
	request := map[string]any{"messages": reqBody["messages"]}
	if mt := reqBody["max_tokens"]; mt != nil {
		request["maxTokens"] = mt
	} else if mt := reqBody["max_completion_tokens"]; mt != nil {
		request["maxTokens"] = mt
	}
	if tools := reqBody["tools"]; tools != nil {
		request["tools"] = tools
	}
	if tc := reqBody["tool_choice"]; tc != nil {
		request["toolChoice"] = tc
	}
	if temp, ok := reqBody["temperature"]; ok {
		request["temperature"] = temp
	}

	level := "off"
	if effort, ok := reqBody["reasoning_effort"].(string); ok && effort != "" {
		level = effort
	}
	level = perchEffortLevel(model, level)

	env := map[string]any{
		"request":        request,
		"runId":          runID,
		"lane":           "chat",
		"strictManual":   false,
		"clientSurface":  "cli",
		"roostReasoning": level != "off",
		"effort":         map[string]any{"level": level, "orchestration": false},
	}
	if tier, ok := perchRoostChoices[model]; ok {
		env["roostModelChoice"] = tier
	} else {
		env["manualModelOptionId"] = model
	}
	return env
}

// perchUsage normalizes Perch's usage counters: inputTokens excludes cached
// input, reasoning tokens are a subset of output, and OpenAI's prompt_tokens
// counts cached input in the total.
func perchUsage(u map[string]any) map[string]any {
	num := func(k string) float64 {
		if v, ok := u[k].(float64); ok && v >= 0 {
			return v
		}
		return 0
	}
	cache := num("cacheReadInputTokens")
	if cache == 0 {
		cache = num("cache_read_input_tokens")
	}
	cacheWrite := num("cacheCreationInputTokens")
	prompt := num("prompt_tokens")
	if prompt == 0 {
		prompt = num("inputTokens") + cache + cacheWrite
	}
	output := num("outputTokens")
	if output == 0 {
		output = num("completion_tokens")
	}
	reasoning := num("reasoningTokens")
	out := map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": output,
		"total_tokens":      prompt + output,
		"prompt_tokens_details": map[string]any{
			"cached_tokens":      cache,
			"cache_write_tokens": cacheWrite,
		},
	}
	if reasoning > 0 {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": reasoning}
	}
	return out
}

// perchToolCalls normalizes a sealed tool snapshot into OpenAI tool_calls.
func perchToolCalls(raw []any) ([]any, error) {
	seen := map[string]bool{}
	out := make([]any, 0, len(raw))
	for i, item := range raw {
		call, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("perch: incomplete tool call")
		}
		id, _ := call["id"].(string)
		name, _ := call["name"].(string)
		if name == "" {
			if fn, ok := call["function"].(map[string]any); ok {
				name, _ = fn["name"].(string)
			}
		}
		if id == "" || name == "" || seen[id] {
			return nil, fmt.Errorf("perch: incomplete or duplicate tool call")
		}
		if sealed, ok := call["sealed"].(bool); ok && !sealed {
			continue
		}
		seen[id] = true
		args := call["arguments"]
		if args == nil {
			if fn, ok := call["function"].(map[string]any); ok {
				args = fn["arguments"]
			}
		}
		argStr := "{}"
		switch a := args.(type) {
		case string:
			if a != "" {
				var m map[string]any
				if err := json.Unmarshal([]byte(a), &m); err != nil {
					return nil, fmt.Errorf("perch: tool arguments are not valid JSON")
				}
				argStr = a
			}
		case map[string]any:
			b, _ := json.Marshal(a)
			argStr = string(b)
		}
		out = append(out, map[string]any{
			"index": i, "id": id, "type": "function",
			"function": map[string]any{"name": name, "arguments": argStr},
		})
	}
	return out, nil
}

// ForwardPerch sends a Chat request through Perch's CLI model-call proxy and
// writes an OpenAI-shaped answer back.
func ForwardPerch(w http.ResponseWriter, req *Request) error {
	ctx := req.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var body map[string]any
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return fmt.Errorf("ForwardPerch: parse body: %w", err)
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
	token := req.APIKey
	if token == "" {
		token = extractPerchToken(req)
	}
	if token == "" {
		writeOpenAIError(w, http.StatusUnauthorized, "Perch connection has no access token — sign in again (Google or GitHub).")
		return nil
	}

	client := req.Client
	if client == nil {
		client = http.DefaultClient
	}
	origin := perchOrigin(req.Config.BaseURL)

	scope := perchTurnScope(msgs)
	ticket, err := perchAcquireTurn(ctx, client, origin, token, scope, req.Config.BaseURL)
	if err != nil && !errors.Is(err, perchErrAdvisoryTicket) {
		if herr, ok := err.(*perchHTTPError); ok {
			perchWriteError(w, herr)
			return nil
		}
		return fmt.Errorf("ForwardPerch: %w", err)
	}

	runID := uuid.NewString()
	if ticket != nil {
		runID = ticket.runID
	}
	if ticket != nil {
		// The previous mint counted; refresh the run binding in the envelope.
		runID = ticket.runID
	}
	env := perchEnvelope(model, body, runID)
	sendBody, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("ForwardPerch: marshal envelope: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+perchModelCallPath, bytes.NewReader(sendBody))
	if err != nil {
		return fmt.Errorf("ForwardPerch: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", perchCLIUserAgent)
	if ticket != nil {
		httpReq.Header.Set("x-perch-turn-ticket", ticket.ticket)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("ForwardPerch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var envMap map[string]any
		_ = json.Unmarshal(raw, &envMap)
		perchWriteError(w, &perchHTTPError{Status: resp.StatusCode, Body: truncatePerchBody(raw), Code: perchErrorCode(envMap)})
		return nil
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var envMap map[string]any
		_ = json.Unmarshal(raw, &envMap)
		perchWriteError(w, &perchHTTPError{Status: resp.StatusCode, Body: truncatePerchBody(raw), Code: perchErrorCode(envMap)})
		return nil
	}

	if req.IsStream {
		return streamPerchAsOpenAI(ctx, w, resp.Body, model, req)
	}
	return replayPerchAsOpenAI(ctx, w, resp.Body, model, req)
}

func extractPerchToken(req *Request) string {
	if req.ConnData == nil {
		return ""
	}
	if v, ok := req.ConnData["accessToken"].(string); ok {
		return v
	}
	return ""
}

// perchWriteError maps Perch's refusal vocabulary onto the status the gateway
// already speaks: plan-gated models are 403 insufficient_quota, rate limits
// are 429, and a dead session is a 401 the client can heal by signing in.
func perchWriteError(w http.ResponseWriter, herr *perchHTTPError) {
	log.Warn("perch", "upstream refused", "status", herr.Status, "code", herr.Code)
	status := herr.Status
	msg := herr.Body
	switch herr.Code {
	case "starter_model_blocked", "starterModel", "starterTier", "modelRestricted", "perch_surface_required", "surfaceRequired":
		status = http.StatusForbidden
	case "turn_rate_limited", "turnRateLimited":
		status = http.StatusTooManyRequests
	case "insufficient_quota", "rate_limit":
		status = http.StatusTooManyRequests
	}
	if msg == "" {
		msg = herr.Error()
	}
	writeOpenAIError(w, status, msg)
}

// readPerchEvents walks Perch's SSE frames and emits one callback per frame.
// Frames are `data: {json}` lines separated by blank lines; comments are
// keep-alives.
func readPerchEvents(r io.Reader, emit func(map[string]any) bool) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var dataLines []string
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
		return emit(ev)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !flush() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	_ = flush()
}

// perchTerminal is the decoded `done` frame: the answer, any sealed tool
// calls, the finish reason, and usage.
type perchTerminal struct {
	ok        bool
	text      string
	toolCalls []any
	finish    string
	usage     map[string]any
	model     string
	errorMsg  string
	errorCode string
	hasDone   bool
}

// decodePerchTerminal extracts the terminal frame plus any tool snapshot
// accumulated from tool_call_delta / tool_use_end events.
func decodePerchTerminal(ev map[string]any, lastSnapshot []any) *perchTerminal {
	t := &perchTerminal{hasDone: true, finish: "stop"}
	if ok, b := ev["ok"].(bool); ok {
		t.ok = b
	} else {
		t.ok = true
	}
	if txt, ok := ev["text"].(string); ok {
		t.text = txt
	}
	if fr, ok := ev["finishReason"].(string); ok && fr != "" {
		t.finish = fr
	} else if fr, ok := ev["stop_reason"].(string); ok && fr != "" {
		t.finish = fr
	}
	if u, ok := ev["usage"].(map[string]any); ok {
		t.usage = u
	}
	if m, ok := ev["model"].(string); ok {
		t.model = m
	}
	if e, ok := ev["error"].(string); ok {
		t.errorMsg = e
	}
	if e, ok := ev["errorCode"].(string); ok {
		t.errorCode = e
	}
	raw := lastSnapshot
	if tc, ok := ev["toolCalls"].([]any); ok && len(tc) > 0 {
		raw = tc
	}
	if len(raw) > 0 {
		t.toolCalls = raw
	}
	return t
}

// streamPerchAsOpenAI re-emits Perch's events as OpenAI chat chunks.
func streamPerchAsOpenAI(ctx context.Context, w http.ResponseWriter, body io.Reader, model string, req *Request) error {
	flusher := proxy.WriteSSEHeaders(w)
	id := "chatcmpl-perch-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	created := time.Now().Unix()
	_ = req

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

	var answer string
	var snapshot []any
	var term *perchTerminal
	readPerchEvents(body, func(ev map[string]any) bool {
		if ctx.Err() != nil {
			return false
		}
		typ, _ := ev["type"].(string)
		switch typ {
		case "answer_delta", "text_delta":
			if text, ok := ev["text"].(string); ok {
				answer += text
				writeChunk(map[string]any{"content": text}, nil)
			}
		case "reasoning_delta":
			if text, ok := ev["text"].(string); ok {
				writeChunk(map[string]any{"reasoning_content": text}, nil)
			}
		case "tool_call_delta", "tool_use_end":
			if tc, ok := ev["toolCalls"].([]any); ok {
				snapshot = tc
			}
		case "stream_restart":
			// Perch abandoned this attempt. Deltas the client already saw
			// cannot be retracted, so continuing would concatenate output
			// from two different generations.
			if answer != "" {
				term = &perchTerminal{hasDone: true, errorMsg: "perch: stream restarted after emitting output; retry the turn"}
			}
			snapshot = nil
		case "error":
			term = &perchTerminal{hasDone: true, errorMsg: "perch: stream error"}
			if msg, ok := ev["message"].(string); ok && msg != "" {
				term.errorMsg = msg
			}
			if code, ok := ev["errorCode"].(string); ok {
				term.errorCode = code
			}
		case "done":
			term = decodePerchTerminal(ev, snapshot)
			if !term.ok || term.errorMsg != "" {
				return false
			}
			if term.text != "" && term.text != answer {
				if !strings.HasPrefix(term.text, answer) {
					term.errorMsg = "perch: final answer differs from streamed output"
					return false
				}
				if tail := term.text[len(answer):]; tail != "" {
					writeChunk(map[string]any{"content": tail}, nil)
					answer = term.text
				}
			}
			return false
		}
		return true
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if term == nil {
		writeOpenAIError(w, http.StatusBadGateway, "perch: stream ended without a completion event")
		return nil
	}
	if term.errorMsg != "" {
		writeChunk(map[string]any{}, nil)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	finish := term.finish
	calls, err := func() ([]any, error) {
		if term.toolCalls == nil {
			return nil, nil
		}
		return perchToolCalls(term.toolCalls)
	}()
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return nil
	}
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	length := finish == "length" || finish == "max_tokens" || finish == "max_output_tokens" || finish == "incomplete"
	if len(calls) > 0 && length {
		writeOpenAIError(w, http.StatusBadGateway, "perch: output limit interrupted a tool call")
		return nil
	}
	if answer == "" && len(calls) == 0 {
		writeOpenAIError(w, http.StatusBadGateway, "perch: empty response")
		return nil
	}
	finalDelta := map[string]any{}
	if len(calls) > 0 {
		finalDelta["tool_calls"] = calls
	}
	usage := perchUsage(map[string]any{})
	if term.usage != nil {
		usage = perchUsage(term.usage)
	}
	chunk := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created,
		"model": term.modelOr(model), "system_fingerprint": nil,
		"choices": []any{map[string]any{
			"index": 0, "delta": finalDelta, "finish_reason": finish, "logprobs": nil,
		}},
		"usage": usage,
	}
	raw, _ := json.Marshal(chunk)
	_, _ = w.Write([]byte("data: "))
	_, _ = w.Write(raw)
	_, _ = w.Write([]byte("\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func (t *perchTerminal) modelOr(fallback string) string {
	if t != nil && t.model != "" {
		return t.model
	}
	return fallback
}

// replayPerchAsOpenAI aggregates the stream into one chat.completion.
func replayPerchAsOpenAI(ctx context.Context, w http.ResponseWriter, body io.Reader, model string, req *Request) error {
	_ = req
	var term *perchTerminal
	var snapshot []any
	readPerchEvents(body, func(ev map[string]any) bool {
		if ctx.Err() != nil {
			return false
		}
		typ, _ := ev["type"].(string)
		switch typ {
		case "tool_call_delta", "tool_use_end":
			if tc, ok := ev["toolCalls"].([]any); ok {
				snapshot = tc
			}
		case "done":
			term = decodePerchTerminal(ev, snapshot)
			return false
		}
		return true
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if term == nil {
		writeOpenAIError(w, http.StatusBadGateway, "perch: stream ended without a completion event")
		return nil
	}
	if term.errorMsg != "" {
		writeOpenAIError(w, http.StatusBadGateway, term.errorMsg)
		return nil
	}
	calls, err := perchToolCalls(nilToEmpty(term.toolCalls))
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return nil
	}
	finish := term.finish
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	msg := map[string]any{"role": "assistant", "content": term.text}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
	}
	out := map[string]any{
		"id":      "chatcmpl-perch-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   term.modelOr(model),
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
	}
	if term.usage != nil {
		out["usage"] = perchUsage(term.usage)
	}
	raw, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	return nil
}

func nilToEmpty(in []any) []any {
	if in == nil {
		return []any{}
	}
	return in
}
