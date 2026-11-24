package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/proxy"
)

// Kiro CreateResponse (/v1/responses) fallback ported from upstream
// open-sse/executors/kiroCreateResponse.js and kiro.js executeCreateResponse.
// Accounts enrolled in the create_response_api experiment receive 429
// USER_REQUEST_RATE_EXCEEDED on legacy generateAssistantResponse even with full
// quota. This protocol fallback keeps those turns alive without touching the
// generic 429 account cooldown / combo failover behavior.

const (
	kiroCreateResponseACPUserAgent = "KiroIDE-1.4.154-KiroIDE/acp-client"
	kiroCreateResponseMarker       = "USER_REQUEST_RATE_EXCEEDED"
)

var (
	// kiroCreateResponseURL is a package var so tests can point at httptest.
	// Empty in production: the region-validated endpoint is derived per call.
	kiroCreateResponseURL = ""
	// kiroCreateResponseRetryDelay is the backoff before the single 429 retry.
	kiroCreateResponseRetryDelay = 1500 * time.Millisecond
)

// kiroRuntimeRegions mirrors upstream KIRO_RUNTIME_REGIONS: only these regions
// may be spliced into the host that receives the bearer token.
var kiroRuntimeRegions = map[string]bool{
	"us-east-1":    true,
	"eu-central-1": true,
}

// kiroCreateResponseMode reads NINEROUTER_KIRO_CREATE_RESPONSE then
// KIRO_CREATE_RESPONSE. Values: auto (default), always, off.
func kiroCreateResponseMode() string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NINEROUTER_KIRO_CREATE_RESPONSE")))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(os.Getenv("KIRO_CREATE_RESPONSE")))
	}
	if mode != "always" && mode != "off" {
		return "auto"
	}
	return mode
}

// kiroCreateResponseEndpoint builds the /v1/responses URL for a region. A
// region outside the validated runtime set falls back to us-east-1.
func kiroCreateResponseEndpoint(region string) string {
	region = strings.TrimSpace(region)
	if !kiroRuntimeRegions[region] {
		region = "us-east-1"
	}
	return "https://runtime." + region + ".kiro.dev/v1/responses"
}

// buildKiroCreateResponseBody converts a Kiro conversationState body into the
// CreateResponse request shape, mirroring upstream buildKiroCreateResponseBody.
func buildKiroCreateResponseBody(kiroBody []byte, model string, psd map[string]any) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(kiroBody, &body); err != nil {
		return nil, fmt.Errorf("parse kiro body: %w", err)
	}

	cs, _ := body["conversationState"].(map[string]any)
	current, _ := cs["currentMessage"].(map[string]any)
	history, _ := cs["history"].([]any)
	turns := make([]any, 0, len(history)+1)
	turns = append(turns, history...)
	if current != nil {
		turns = append(turns, current)
	}

	input := make([]any, 0)
	for _, raw := range turns {
		turn, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if u, ok := turn["userInputMessage"].(map[string]any); ok {
			input = appendKiroUserInput(input, u)
		}
		if m, ok := turn["assistantResponseMessage"].(map[string]any); ok {
			input = appendKiroAssistantInput(input, m)
		}
	}

	out := map[string]any{
		"model":    resolvedCreateResponseModel(current, body, model),
		"input":    input,
		"stream":   true,
		"origin":   kiroCreateResponseOrigin(current),
		"metadata": kiroCreateResponseMetadata(cs),
	}
	if tools := kiroCreateResponseTools(current); len(tools) > 0 {
		out["tools"] = tools
	}
	if arn := kiroCreateResponseProfileArn(body, psd); arn != "" {
		out["profileArn"] = arn
	}
	if reasoning := kiroCreateResponseReasoning(body); len(reasoning) > 0 {
		out["reasoning"] = reasoning
	}
	return json.Marshal(out, json.Deterministic(true))
}

func appendKiroUserInput(input []any, u map[string]any) []any {
	ctx, _ := u["userInputMessageContext"].(map[string]any)
	if results, ok := ctx["toolResults"].([]any); ok {
		for _, raw := range results {
			r, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			parts, _ := r["content"].([]any)
			texts := make([]string, 0, len(parts))
			for _, p := range parts {
				if pm, ok := p.(map[string]any); ok {
					if text, ok := pm["text"].(string); ok {
						texts = append(texts, text)
					} else if text, ok := pm["content"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": r["toolUseId"],
				"output":  strings.Join(texts, "\n"),
			})
		}
	}

	images := make([]any, 0)
	if rawImages, ok := u["images"].([]any); ok {
		for _, raw := range rawImages {
			img, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			source, _ := img["source"].(map[string]any)
			bytes, ok := source["bytes"].(string)
			if !ok || bytes == "" {
				continue
			}
			format, _ := img["format"].(string)
			if format == "" {
				format = "png"
			}
			images = append(images, map[string]any{
				"type":      "input_image",
				"image_url": "data:image/" + strings.ToLower(format) + ";base64," + bytes,
			})
		}
	}

	text, _ := u["content"].(string)
	if len(images) > 0 {
		parts := make([]any, 0, len(images)+1)
		if text != "" {
			parts = append(parts, map[string]any{"type": "input_text", "text": text})
		}
		parts = append(parts, images...)
		input = append(input, map[string]any{"role": "user", "content": parts})
	} else if text != "" {
		input = append(input, map[string]any{"role": "user", "content": text})
	}
	return input
}

func appendKiroAssistantInput(input []any, m map[string]any) []any {
	if content, ok := m["content"].(string); ok && content != "" {
		input = append(input, map[string]any{"role": "assistant", "content": content})
	}
	if uses, ok := m["toolUses"].([]any); ok {
		for _, raw := range uses {
			c, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			args := ""
			switch v := c["input"].(type) {
			case string:
				args = v
			default:
				if b, err := json.Marshal(v); err == nil {
					args = string(b)
				} else {
					args = "{}"
				}
			}
			name, _ := c["name"].(string)
			input = append(input, map[string]any{
				"type":      "function_call",
				"call_id":   c["toolUseId"],
				"name":      name,
				"arguments": args,
			})
		}
	}
	return input
}

func kiroCreateResponseTools(current map[string]any) []any {
	u, _ := current["userInputMessage"].(map[string]any)
	ctx, _ := u["userInputMessageContext"].(map[string]any)
	raw, _ := ctx["tools"].([]any)
	tools := make([]any, 0, len(raw))
	for _, item := range raw {
		t, ok := item.(map[string]any)
		if !ok {
			continue
		}
		spec, ok := t["toolSpecification"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := spec["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := spec["description"].(string)
		params := map[string]any{}
		if schema, ok := spec["inputSchema"].(map[string]any); ok {
			if j, ok := schema["json"].(map[string]any); ok {
				params = j
			}
		}
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        name,
			"description": desc,
			"parameters":  params,
		})
	}
	return tools
}

func kiroCreateResponseReasoning(body map[string]any) map[string]any {
	f, ok := body["additionalModelRequestFields"].(map[string]any)
	if !ok {
		return nil
	}
	str := func(v any) string {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
		return ""
	}
	out := map[string]any{}
	if thinking, ok := f["thinking"].(map[string]any); ok {
		if t := str(thinking["type"]); t != "" {
			out["type"] = t
		}
		if s := str(thinking["display"]); s != "" {
			out["summary"] = s
		}
	}
	effort := ""
	if oc, ok := f["output_config"].(map[string]any); ok {
		effort = str(oc["effort"])
	}
	if effort == "" {
		if r, ok := f["reasoning"].(map[string]any); ok {
			effort = str(r["effort"])
		}
	}
	if effort != "" {
		out["effort"] = effort
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func kiroCreateResponseMetadata(cs map[string]any) map[string]any {
	metadata := map[string]any{"kiro:agent_mode": "vibe"}
	if id, ok := cs["conversationId"].(string); ok && id != "" {
		metadata["kiro:conversation_id"] = id
	}
	return metadata
}

func resolvedCreateResponseModel(current, body map[string]any, fallback string) string {
	if u, ok := current["userInputMessage"].(map[string]any); ok {
		if id, ok := u["modelId"].(string); ok && id != "" {
			return id
		}
	}
	if id, ok := body["_kiroUpstreamModel"].(string); ok && id != "" {
		return id
	}
	return fallback
}

func kiroCreateResponseOrigin(current map[string]any) string {
	if u, ok := current["userInputMessage"].(map[string]any); ok {
		if o, ok := u["origin"].(string); ok && o != "" {
			return o
		}
	}
	return "AI_EDITOR"
}

func kiroCreateResponseProfileArn(body map[string]any, psd map[string]any) string {
	if arn, ok := body["profileArn"].(string); ok && arn != "" {
		return arn
	}
	if psd != nil {
		if arn, ok := psd["profileArn"].(string); ok && arn != "" {
			return arn
		}
	}
	return ""
}

// buildKiroCreateResponseHeaders builds the exact header set upstream sends to
// /v1/responses. token is the bearer credential; psd carries auth method and
// profile ARN; body is the translated request (profile ARN fallback).
func buildKiroCreateResponseHeaders(token string, psd map[string]any, body []byte) map[string]string {
	headers := map[string]string{
		"Content-Type":                "application/json",
		"Authorization":               "Bearer " + token,
		"x-amzn-kiro-origin":          "AI_EDITOR",
		"x-amzn-codewhisperer-optout": "true",
		"User-Agent":                  kiroCreateResponseACPUserAgent,
		"x-amzn-kiro-attempt":         "1;max=3",
	}
	profileArn := ""
	if psd != nil {
		profileArn, _ = psd["profileArn"].(string)
	}
	if profileArn == "" {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err == nil {
			profileArn, _ = m["profileArn"].(string)
		}
	}
	if profileArn != "" {
		headers["x-amzn-kiro-profile"] = profileArn
	}
	authMethod := ""
	if psd != nil {
		authMethod, _ = psd["authMethod"].(string)
	}
	apiKey := ""
	if psd != nil {
		apiKey, _ = psd["apiKey"].(string)
	}
	if authMethod == "api_key" && (apiKey != "" || token != "") {
		headers["TokenType"] = "API_KEY"
	} else if authMethod == "external_idp" {
		headers["TokenType"] = "EXTERNAL_IDP"
	}
	if token != "" {
		headers["x-amz-sso-bearer"] = token
	}
	return headers
}

// executeKiroCreateResponse performs the CreateResponse call. A non-nil
// response is returned for any HTTP answer (ok or not); nil means a network
// failure. That distinction drives the "always" mode call site.
func executeKiroCreateResponse(ctx context.Context, client *http.Client, url string, headers map[string]string, body []byte) (*http.Response, error) {
	resp, err := proxy.SendOnce(ctx, client, http.MethodPost, url, headers, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests && ctx.Err() == nil {
		_ = resp.Body.Close()
		select {
		case <-time.After(kiroCreateResponseRetryDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		resp, err = proxy.SendOnce(ctx, client, http.MethodPost, url, headers, body)
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// kiroCreateResponseStream transforms upstream /v1/responses SSE events into
// OpenAI chat.completion.chunk SSE, incrementally through an io.Pipe.
func kiroCreateResponseStream(ctx context.Context, upstream io.Reader, model string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		s := &kiroCRStream{
			w:       pw,
			ctx:     ctx,
			model:   model,
			id:      "chatcmpl-cr-" + strconv.FormatInt(time.Now().UnixMilli(), 10),
			created: time.Now().Unix(),
		}
		err := s.run(upstream)
		_ = pw.CloseWithError(err)
	}()
	return pr
}

type kiroCRStream struct {
	w       *io.PipeWriter
	ctx     context.Context
	model   string
	id      string
	created int64

	emittedRole bool
	toolIdx     int
	curCallID   string
	openCalls   map[string]bool
	arguedCalls map[string]bool
	hasTools    bool
}

func (s *kiroCRStream) run(upstream io.Reader) error {
	s.openCalls = map[string]bool{}
	s.arguedCalls = map[string]bool{}
	defer func() {
		_, _ = s.w.Write([]byte("data: [DONE]\n\n"))
	}()

	err := proxy.ScanStreamEvents(upstream, func(ev proxy.SSEEvent) bool {
		if s.ctx.Err() != nil {
			return false
		}
		var event map[string]any
		if json.Unmarshal(ev.Data, &event) != nil {
			return true
		}
		if stop := s.process(event); !stop {
			return false
		}
		return true
	})
	if err != nil && s.ctx.Err() == nil {
		// Upstream stream failure surfaces in-band, never as a silent success.
		s.emitChunk(map[string]any{"error": map[string]any{
			"message": err.Error(), "type": "server_error", "code": 502,
		}}, nil, nil)
	}
	return err
}

// process returns false only on a terminal stream error.
func (s *kiroCRStream) process(event map[string]any) bool {
	typeName, _ := event["type"].(string)
	delta, _ := event["delta"].(string)

	switch typeName {
	case "response.output_text.delta":
		if delta != "" {
			s.emitText(delta)
		}
	case "response.reasoning_text.delta", "response.reasoning.delta":
		if delta != "" {
			if !s.emittedRole {
				s.emitChunk(map[string]any{"role": "assistant", "reasoning_content": delta}, nil, nil)
				s.emittedRole = true
			} else {
				s.emitChunk(map[string]any{"reasoning_content": delta}, nil, nil)
			}
		}
	case "response.refusal.delta":
		if delta != "" {
			s.emitText(delta)
		}
	case "response.refusal.done":
		if refusal, ok := event["refusal"].(string); ok && refusal != "" {
			s.emitChunk(map[string]any{"content": refusal}, nil, nil)
		}
	case "response.output_item.added":
		item, _ := event["item"].(map[string]any)
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			s.startToolCall(item)
		}
	case "response.function_call_arguments.delta":
		if delta != "" {
			if s.curCallID != "" {
				s.arguedCalls[s.curCallID] = true
			}
			s.emitChunk(map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    s.toolIdx,
					"function": map[string]any{"arguments": delta},
				}},
			}, nil, nil)
		}
	case "response.output_item.done":
		item, _ := event["item"].(map[string]any)
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			callID := kiroCRCallID(item)
			if s.openCalls[callID] {
				delete(s.openCalls, callID)
				if !s.arguedCalls[callID] {
					delete(s.arguedCalls, callID)
					if args, ok := item["arguments"].(string); ok && args != "" {
						s.emitChunk(map[string]any{
							"tool_calls": []any{map[string]any{
								"index":    s.toolIdx,
								"function": map[string]any{"arguments": args},
							}},
						}, nil, nil)
					}
				}
			}
		}
	case "response.completed", "response.incomplete":
		s.emitCompletion(typeName, event)
	case "error", "response.failed":
		var detail any
		if typeName == "error" {
			detail = event["error"]
		} else {
			if resp, ok := event["response"].(map[string]any); ok {
				detail = resp["error"]
			}
		}
		if detail == nil {
			detail = map[string]any{}
		}
		s.emitChunk(map[string]any{"error": detail}, nil, nil)
		return false
	}
	return true
}

func (s *kiroCRStream) emitText(delta string) {
	if s.emittedRole {
		s.emitChunk(map[string]any{"content": delta}, nil, nil)
		return
	}
	s.emitChunk(map[string]any{"role": "assistant", "content": delta}, nil, nil)
	s.emittedRole = true
}

func kiroCRCallID(item map[string]any) string {
	if id, ok := item["call_id"].(string); ok && id != "" {
		return id
	}
	if id, ok := item["id"].(string); ok {
		return id
	}
	return ""
}

func (s *kiroCRStream) startToolCall(item map[string]any) {
	callID := kiroCRCallID(item)
	s.curCallID = callID
	s.openCalls[callID] = true
	s.hasTools = true
	s.toolIdx++
	name, _ := item["name"].(string)
	s.emitChunk(map[string]any{
		"tool_calls": []any{map[string]any{
			"index": s.toolIdx,
			"id":    callID,
			"type":  "function",
			"function": map[string]any{
				"name": name, "arguments": "",
			},
		}},
	}, nil, nil)
	s.emittedRole = true
}

func (s *kiroCRStream) emitCompletion(typeName string, event map[string]any) {
	resp, _ := event["response"].(map[string]any)
	incomplete := typeName == "response.incomplete"
	if status, ok := resp["status"].(string); ok && status == "incomplete" {
		incomplete = true
	}
	finish := "stop"
	if s.hasTools {
		finish = "tool_calls"
	}
	if incomplete {
		if details, ok := resp["incomplete_details"].(map[string]any); ok {
			reason, _ := details["reason"].(string)
			switch reason {
			case "max_output_tokens", "compaction_required_for_model_handoff":
				finish = "length"
			case "content_filter":
				finish = "content_filter"
			}
		}
	}
	var usage any
	if u, ok := resp["usage"].(map[string]any); ok {
		input := kiroCRTokenCount(u["input_tokens"])
		output := kiroCRTokenCount(u["output_tokens"])
		usage = map[string]any{
			"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output,
		}
	}
	s.emitChunk(map[string]any{}, &finish, usage)
}

func kiroCRTokenCount(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func (s *kiroCRStream) emitChunk(delta map[string]any, finish *string, usage any) {
	chunk := map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []any{map[string]any{
			"index": 0, "delta": delta, "finish_reason": finish,
		}},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return
	}
	_, _ = s.w.Write(append(append([]byte("data: "), b...), []byte("\n\n")...))
}
