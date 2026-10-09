package executor

import (
	"bufio"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"io"
	"strings"
	"time"
)

// Qoder response unwrapping — port of open-sse/shared/qoder/sse.js
// (canonicalizeQoderUsage, finishReasonOf, hasValuableDelta, parseInner,
// createQoderSseCoalescer) and the inner-loop half of
// open-sse/executors/qoder.js wrapQoderSSE.
//
// Qoder's event stream is OpenAI-shaped, but wrapped one level deep: every
// frame is an envelope whose `body` holds the real chunk as a JSON *string*.
//
//	data: {"headers":{...},"body":"{\"choices\":[{\"delta\":{...}}]}",
//	        "statusCodeValue":200,"statusCode":"OK"}
//
// Handing those frames to the shared SSE folder finds no `choices`, which is
// how a working upstream turned into "200 without a completion" (HTTP 502).
// This peels the envelope and re-emits the inner chunk as a plain SSE frame.

// qoderSSEDone terminates a rewound stream.
const qoderSSEDone = "data: [DONE]\n\n"

// qoderEnvelope is one `data:` frame from Qoder.
type qoderEnvelope struct {
	Body            jsontext.Value `json:"body"`
	StatusCodeValue int            `json:"statusCodeValue"`
	StatusCode      string         `json:"statusCode"`
}

// qoderUsage is the normalized usage block the gateway's billing and token
// accounting already understand.
type qoderUsage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	TotalTokens             int `json:"total_tokens"`
	CachedTokens            int `json:"cached_tokens,omitempty"`
	CacheCreationTokens     int `json:"cache_creation_tokens,omitempty"`
	ReasoningTokens         int `json:"reasoning_tokens,omitempty"`
	PromptTokensDetails     any `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails any `json:"completion_tokens_details,omitempty"`
}

// qoderCoalescer rewrites Qoder's frames into plain OpenAI SSE.
//
// Qoder sends an empty finish chunk and then a separate `choices: []` frame
// carrying usage. Downstream clients read usage off the finish chunk and drop
// `choices: []` entirely, so both are held and re-emitted as one terminal
// chunk that carries `finish_reason` and `usage` together.
type qoderCoalescer struct {
	// model is stamped on emitted frames. Qoder reports "auto" regardless of
	// which model served the turn, so the requested model is the honest label.
	model string

	pendingFinish   string
	pendingUsage    *qoderUsage
	lastID          string
	lastCreated     int64
	finishForwarded bool
	doneEmitted     bool
	out             io.Writer
}

// newQoderCoalescer starts a rewound stream writing to out.
func newQoderCoalescer(out io.Writer, model string) *qoderCoalescer {
	if model == "" {
		model = "qoder"
	}
	return &qoderCoalescer{model: model, out: out}
}

// qoderSSERewrite streams a Qoder event stream out as plain OpenAI SSE.
// It returns when the upstream ends or the caller closes the writer.
func qoderSSERewrite(upstream io.Reader, out io.Writer, model string) error {
	c := newQoderCoalescer(out, model)
	scanner := bufio.NewScanner(upstream)
	// A single frame carries a whole escaped chunk; Qoder's own frames run to
	// several kilobytes, so the default 64KiB line cap is raised well past it.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		if c.doneEmitted {
			return nil
		}
		if err := c.handleLine(scanner.Text()); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("qoder sse read: %w", err)
	}
	return c.flush()
}

// handleLine processes one already-split upstream line.
func (c *qoderCoalescer) handleLine(line string) error {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if data == "" || data == "[DONE]" {
		return c.flush()
	}

	var envelope qoderEnvelope
	if err := json.Unmarshal([]byte(data), &envelope); err != nil {
		// Not an envelope: forward it untouched so nothing is silently lost.
		return c.emitRaw(data)
	}
	status := envelope.StatusCodeValue
	if status == 0 {
		status = 200
	}
	inner := qoderEnvelopeBody(envelope.Body)

	if status != 200 {
		return c.emitError(status, inner)
	}
	if inner == "" {
		return nil
	}
	return c.handleInner(inner)
}

// qoderEnvelopeBody recovers the inner payload as text. Qoder sends it as an
// escaped JSON string inside the envelope; a body that is not a string is
// passed through as raw JSON, which is the shape a future revision would send.
func qoderEnvelopeBody(body jsontext.Value) string {
	if len(body) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(body, &asString); err != nil {
		return string(body)
	}
	return asString
}

// qoderFinishReason reads the finish reason off a chunk, which Qoder places on
// the delta rather than the choice.
func qoderFinishReason(chunk map[string]any) string {
	choices, _ := chunk["choices"].([]any)
	if len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				return fr
			}
			if delta, ok := choice["delta"].(map[string]any); ok {
				if fr, ok := delta["finish_reason"].(string); ok && fr != "" {
					return fr
				}
			}
		}
	}
	if fr, ok := chunk["finish_reason"].(string); ok && fr != "" {
		return fr
	}
	return ""
}

// qoderHasValuableDelta reports whether a chunk carries anything a client
// should see. Empty deltas are the frames the coalescer holds back.
func qoderHasValuableDelta(chunk map[string]any) bool {
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return false
	}
	delta, ok := choices[0].(map[string]any)
	if !ok {
		return false
	}
	inner, _ := delta["delta"].(map[string]any)
	if inner == nil {
		return false
	}
	if s, ok := inner["content"].(string); ok && s != "" {
		return true
	}
	if s, ok := inner["reasoning_content"].(string); ok && s != "" {
		return true
	}
	if calls, ok := inner["tool_calls"].([]any); ok && len(calls) > 0 {
		return true
	}
	if role, ok := inner["role"].(string); ok && role != "" {
		return true
	}
	return false
}

// handleInner processes one inner chunk body.
func (c *qoderCoalescer) handleInner(inner string) error {
	if inner == "[DONE]" {
		return c.flush()
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(inner), &chunk); err != nil {
		return c.emitRaw(inner)
	}
	if id, ok := chunk["id"].(string); ok && id != "" {
		c.lastID = id
	}
	if created, ok := chunk["created"].(float64); ok {
		c.lastCreated = int64(created)
	}

	if usage := qoderCanonicalUsage(chunk["usage"]); usage != nil {
		c.pendingUsage = usage
	}
	finish := qoderFinishReason(chunk)

	if qoderHasValuableDelta(chunk) {
		if err := c.emitRaw(inner); err != nil {
			return err
		}
		if finish != "" {
			c.finishForwarded = true
			// Only worth holding the finish back when a usage trailer still
			// has to attach to it.
			if c.pendingUsage != nil {
				c.pendingFinish = finish
			} else {
				c.pendingFinish = ""
			}
		}
		if c.pendingFinish != "" && c.pendingUsage != nil {
			return c.emitTerminal()
		}
		return nil
	}

	if finish != "" {
		c.pendingFinish = finish
	}
	// Qoder orders finish before usage, so once both are in hand the terminal
	// chunk can go out without waiting for the later [DONE].
	if (c.pendingFinish != "" || c.finishForwarded) && c.pendingUsage != nil {
		if c.pendingFinish == "" {
			c.pendingFinish = "stop"
		}
		return c.emitTerminal()
	}
	return nil
}

// emitTerminal writes the combined finish+usage chunk.
func (c *qoderCoalescer) emitTerminal() error {
	if c.pendingFinish == "" && c.pendingUsage == nil {
		return nil
	}
	chunk := map[string]any{
		"id":      c.lastID,
		"object":  "chat.completion.chunk",
		"created": c.lastCreated,
		"model":   c.model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": orDefault(c.pendingFinish, "stop"),
		}},
	}
	if c.pendingUsage != nil {
		chunk["usage"] = c.pendingUsage
	}
	c.pendingFinish = ""
	c.pendingUsage = nil
	return c.emitChunk(chunk)
}

// flush writes whatever is pending, then the terminal [DONE].
func (c *qoderCoalescer) flush() error {
	if c.doneEmitted {
		return nil
	}
	if c.pendingUsage != nil || (c.pendingFinish != "" && !c.finishForwarded) {
		if err := c.emitTerminal(); err != nil {
			return err
		}
	}
	return c.emitDone()
}

func (c *qoderCoalescer) emitDone() error {
	if c.doneEmitted {
		return nil
	}
	c.doneEmitted = true
	_, err := io.WriteString(c.out, qoderSSEDone)
	return err
}

// emitChunk writes one JSON object as an SSE frame.
func (c *qoderCoalescer) emitChunk(chunk map[string]any) error {
	raw, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	return c.emitRaw(string(raw))
}

// emitRaw writes pre-serialized text as an SSE frame. Newlines are stripped
// because they would terminate the frame early.
func (c *qoderCoalescer) emitRaw(text string) error {
	_, err := fmt.Fprintf(c.out, "data: %s\n\n", strings.NewReplacer("\r", "", "\n", "").Replace(text))
	return err
}

// emitError turns a non-200 envelope into a marked error chunk.
//
// The marker is what lets qoderNonStream raise the upstream status instead of
// folding an error into a successful-looking answer, and a billing block is
// reported as a quota error rather than as assistant text so the client can
// fall back instead of believing the refusal was the answer.
func (c *qoderCoalescer) emitError(status int, inner string) error {
	if qoderIsBillingBlock(inner) {
		chunk, _ := json.Marshal(map[string]any{
			"error": map[string]any{
				"message": orDefault(inner, fmt.Sprintf("qoder billing block (%d)", status)),
				"code":    "qoder_billing_block",
				"status":  403,
				"type":    "quota_error",
			},
		})
		if err := c.emitRaw(string(chunk)); err != nil {
			return err
		}
		return c.emitDone()
	}

	chunk, _ := json.Marshal(map[string]any{
		"id":      fmt.Sprintf("qoder-error-%d", time.Now().UnixMilli()),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   c.model,
		"qoder_error": map[string]any{
			"status":  status,
			"message": orDefault(inner, fmt.Sprintf("upstream status %d", status)),
		},
		"choices": []any{},
	})
	if err := c.emitRaw(string(chunk)); err != nil {
		return err
	}
	return c.emitDone()
}

// orDefault returns fallback when value is empty.
func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// qoderIsBillingBlock recognizes Qoder's billing/quota envelopes. Upstream's
// signatures: code 110 (billing daily count exceeded), 112 (quota exhausted),
// 10605 (queue throttle), and a pricingUrl field.
func qoderIsBillingBlock(inner string) bool {
	if inner == "" {
		return false
	}
	for _, marker := range []string{`"code":110`, `"code":112`, `10605`, "pricingUrl", "pricing_url"} {
		if strings.Contains(inner, marker) {
			return true
		}
	}
	return false
}

// qoderCanonicalUsage normalizes Qoder's usage block into the shape the
// gateway's token accounting and the Claude translator already read.
func qoderCanonicalUsage(raw any) *qoderUsage {
	usage, ok := raw.(map[string]any)
	if !ok || len(usage) == 0 {
		return nil
	}
	prompt := qoderNumber(usage["prompt_tokens"], usage["input_tokens"])
	completion := qoderNumber(usage["completion_tokens"], usage["output_tokens"])
	if prompt == 0 && completion == 0 {
		return nil
	}
	total := qoderNumber(usage["total_tokens"])
	if total == 0 {
		total = prompt + completion
	}

	out := &qoderUsage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
	}

	var details map[string]any
	if existing, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		details = map[string]any{}
		for k, v := range existing {
			details[k] = v
		}
	}
	cached := qoderNumber(details["cached_tokens"], usage["cached_tokens"], usage["prompt_cache_hit_tokens"], usage["cache_read_input_tokens"])
	created := qoderNumber(details["cache_creation_tokens"], usage["cache_creation_input_tokens"])
	if cached > 0 {
		out.CachedTokens = cached
		if details == nil {
			details = map[string]any{}
		}
		details["cached_tokens"] = cached
	}
	if created > 0 {
		out.CacheCreationTokens = created
		if details == nil {
			details = map[string]any{}
		}
		details["cache_creation_tokens"] = created
	}
	if len(details) > 0 {
		out.PromptTokensDetails = details
	}
	if completionDetails, ok := usage["completion_tokens_details"].(map[string]any); ok {
		out.CompletionTokensDetails = completionDetails
	}
	reasoning := qoderNumber(usage["reasoning_tokens"])
	if reasoning == 0 {
		if completionDetails, ok := usage["completion_tokens_details"].(map[string]any); ok {
			reasoning = qoderNumber(completionDetails["reasoning_tokens"])
		}
	}
	out.ReasoningTokens = reasoning
	return out
}

// qoderNumber returns the first key present as a number, else 0.
func qoderNumber(values ...any) int {
	for _, v := range values {
		if n, ok := v.(float64); ok && n > 0 {
			return int(n)
		}
	}
	return 0
}
