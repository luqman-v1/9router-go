package proxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

// kiroRuntimeRegions are the regions the Kiro runtime data plane serves.
// Only these may be spliced into the hostname that receives the bearer token.
var kiroRuntimeRegions = map[string]bool{"us-east-1": true, "eu-central-1": true}

// kiroRegionPattern mirrors AWS_REGION_PATTERN (open-sse/config/awsConstants.js).
var kiroRegionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// KiroCreateResponseURL builds the CreateResponse API URL for the configured AWS region.
func KiroCreateResponseURL(args ...any) string {
	region := ""
	for _, a := range args {
		switch v := a.(type) {
		case string:
			region = v
		case map[string]any:
			if r, ok := v["region"].(string); ok {
				region = r
			}
		}
	}
	requested := strings.TrimSpace(region)
	reg := requested
	if !kiroRegionPattern.MatchString(requested) || !kiroRuntimeRegions[requested] {
		reg = "us-east-1"
	}
	return "https://runtime." + reg + ".kiro.dev/v1/responses"
}

// KiroCreateResponseHeaders builds headers for CreateResponse API.
func KiroCreateResponseHeaders(args ...any) map[string]string {
	var credentials, body map[string]any
	for _, a := range args {
		switch v := a.(type) {
		case map[string]any:
			if _, ok := v["credentials"]; ok {
				if c, ok2 := v["credentials"].(map[string]any); ok2 {
					credentials = c
				}
				if b, ok2 := v["body"].(map[string]any); ok2 {
					body = b
				}
				continue
			}
			if credentials == nil {
				credentials = v
			} else if body == nil {
				body = v
			}
		}
	}
	if credentials == nil {
		credentials = map[string]any{}
	}
	if body == nil {
		body = map[string]any{}
	}
	token := ""
	if v, ok := credentials["accessToken"].(string); ok && v != "" {
		token = v
	} else if v, ok := credentials["apiKey"].(string); ok {
		token = v
	}
	psd, _ := credentials["providerSpecificData"].(map[string]any)
	authMethod := ""
	if psd != nil {
		if v, ok := psd["authMethod"].(string); ok {
			authMethod = v
		}
	}
	profileArn := ""
	if psd != nil {
		if v, ok := psd["profileArn"].(string); ok {
			profileArn = v
		}
	}
	if profileArn == "" && body != nil {
		if v, ok := body["profileArn"].(string); ok {
			profileArn = v
		}
	}
	headers := map[string]string{
		"Content-Type":                "application/json",
		"Authorization":               "Bearer " + token,
		"x-amzn-kiro-origin":          "AI_EDITOR",
		"x-amzn-codewhisperer-optout": "true",
		"User-Agent":                  "KiroIDE-1.4.154-KiroIDE/acp-client",
		"x-amzn-kiro-attempt":         "1;max=3",
	}
	if profileArn != "" {
		headers["x-amzn-kiro-profile"] = profileArn
	}
	if authMethod == "api_key" && (credentials["apiKey"] != nil || credentials["accessToken"] != nil) {
		headers["TokenType"] = "API_KEY"
	} else if authMethod == "external_idp" {
		headers["TokenType"] = "EXTERNAL_IDP"
	}
	if v, ok := credentials["accessToken"].(string); ok && v != "" {
		headers["x-amz-sso-bearer"] = v
	}
	return headers
}

// KiroCreateResponseBody converts Kiro conversationState body to CreateResponse body.
func KiroCreateResponseBody(args ...any) map[string]any {
	var body, credentials map[string]any
	model := ""
	for _, a := range args {
		switch v := a.(type) {
		case string:
			model = v
		case map[string]any:
			if _, ok := v["body"]; ok {
				if b, ok2 := v["body"].(map[string]any); ok2 {
					body = b
				}
				if c, ok2 := v["credentials"].(map[string]any); ok2 {
					credentials = c
				}
				if m, ok2 := v["model"].(string); ok2 {
					model = m
				}
				continue
			}
			if body == nil {
				body = v
			} else if credentials == nil {
				credentials = v
			}
		}
	}
	if body == nil {
		body = map[string]any{}
	}
	if credentials == nil {
		credentials = map[string]any{}
	}
	_ = credentials
	_ = model
	__body := body
	_ = __body
	// original function body follows with `body` variable

	cs, _ := body["conversationState"].(map[string]any)
	var current map[string]any
	if cs != nil {
		current, _ = cs["currentMessage"].(map[string]any)
	}
	var history []any
	if cs != nil {
		if h, ok := cs["history"].([]any); ok {
			history = h
		}
	}
	turns := []any{}
	for _, h := range history {
		turns = append(turns, h)
	}
	if current != nil {
		turns = append(turns, current)
	}

	input := []any{}
	for _, turnAny := range turns {
		turn, _ := turnAny.(map[string]any)
		if turn == nil {
			continue
		}
		if u, ok := turn["userInputMessage"].(map[string]any); ok && u != nil {
			// 1. Tool result outputs
			ctx, _ := u["userInputMessageContext"].(map[string]any)
			var toolResults []any
			if ctx != nil {
				if tr, ok := ctx["toolResults"].([]any); ok {
					toolResults = tr
				}
			}
			for _, rAny := range toolResults {
				r, _ := rAny.(map[string]any)
				if r == nil {
					continue
				}
				output := ""
				if content, ok := r["content"].([]any); ok {
					parts := []string{}
					for _, pAny := range content {
						p, _ := pAny.(map[string]any)
						if p != nil {
							if t, ok := p["text"].(string); ok {
								parts = append(parts, t)
							}
						}
					}
					output = strings.Join(parts, "\n")
				}
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": r["toolUseId"],
					"output":  output,
				})
			}
			// 2. Input images
			var images []any
			if imgs, ok := u["images"].([]any); ok {
				for _, imgAny := range imgs {
					img, _ := imgAny.(map[string]any)
					if img == nil {
						continue
					}
					src, _ := img["source"].(map[string]any)
					if src == nil || src["bytes"] == nil {
						continue
					}
					format := "png"
					if f, ok := img["format"].(string); ok && f != "" {
						format = strings.ToLower(f)
					}
					images = append(images, map[string]any{
						"type":      "input_image",
						"image_url": "data:image/" + format + ";base64," + fmt.Sprintf("%v", src["bytes"]),
					})
				}
			}
			// 3. User content
			text := ""
			if t, ok := u["content"].(string); ok {
				text = t
			}
			if len(images) > 0 {
				content := []any{}
				if text != "" {
					content = append(content, map[string]any{"type": "input_text", "text": text})
				}
				content = append(content, images...)
				input = append(input, map[string]any{"role": "user", "content": content})
			} else if text != "" {
				input = append(input, map[string]any{"role": "user", "content": text})
			}
		}
		if m, ok := turn["assistantResponseMessage"].(map[string]any); ok && m != nil {
			if c, ok := m["content"].(string); ok && c != "" {
				input = append(input, map[string]any{"role": "assistant", "content": c})
			}
			var toolUses []any
			if tu, ok := m["toolUses"].([]any); ok {
				toolUses = tu
			}
			for _, cAny := range toolUses {
				c, _ := cAny.(map[string]any)
				if c == nil {
					continue
				}
				args := c["input"]
				if s, ok := args.(string); ok {
					// already string
					_ = s
				} else {
					b, _ := json.Marshal(args)
					args = string(b)
				}
				if args == nil {
					args = "{}"
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   c["toolUseId"],
					"name":      c["name"],
					"arguments": args,
				})
			}
		}
	}

	// Extract tool specifications from current message context
	tools := []any{}
	if current != nil {
		if u, ok := current["userInputMessage"].(map[string]any); ok && u != nil {
			if ctx, ok := u["userInputMessageContext"].(map[string]any); ok && ctx != nil {
				if rawTools, ok := ctx["tools"].([]any); ok {
					for _, tAny := range rawTools {
						t, _ := tAny.(map[string]any)
						if t == nil {
							continue
						}
						s, _ := t["toolSpecification"].(map[string]any)
						if s == nil {
							continue
						}
						name, _ := s["name"].(string)
						if name == "" {
							continue
						}
						desc := ""
						if d, ok := s["description"].(string); ok {
							desc = d
						}
						params := map[string]any{}
						if is, ok := s["inputSchema"].(map[string]any); ok && is != nil {
							if j, ok := is["json"].(map[string]any); ok && j != nil {
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
				}
			}
		}
	}

	// Extract thinking / reasoning configuration
	var reasoning map[string]any
	if f, ok := body["additionalModelRequestFields"].(map[string]any); ok && f != nil {
		str := func(v any) string {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
			return ""
		}
		thinking, _ := f["thinking"].(map[string]any)
		typeVal := ""
		if thinking != nil {
			typeVal = str(thinking["type"])
		}
		effort := ""
		if oc, ok := f["output_config"].(map[string]any); ok && oc != nil {
			effort = str(oc["effort"])
		}
		if effort == "" {
			if r, ok := f["reasoning"].(map[string]any); ok && r != nil {
				effort = str(r["effort"])
			}
		}
		summary := ""
		if thinking != nil {
			summary = str(thinking["display"])
		}
		if typeVal != "" || effort != "" || summary != "" {
			reasoning = map[string]any{}
			if typeVal != "" {
				reasoning["type"] = typeVal
			}
			if effort != "" {
				reasoning["effort"] = effort
			}
			if summary != "" {
				reasoning["summary"] = summary
			}
		}
	}

	psd, _ := credentials["providerSpecificData"].(map[string]any)
	profileArn := ""
	if body != nil {
		if v, ok := body["profileArn"].(string); ok {
			profileArn = v
		}
	}
	if profileArn == "" && psd != nil {
		if v, ok := psd["profileArn"].(string); ok {
			profileArn = v
		}
	}

	conversationId := ""
	if cs != nil {
		if v, ok := cs["conversationId"].(string); ok {
			conversationId = v
		}
	}

	resolvedModel := model
	if current != nil {
		if u, ok := current["userInputMessage"].(map[string]any); ok && u != nil {
			if v, ok := u["modelId"].(string); ok && v != "" {
				resolvedModel = v
			}
		}
	}
	// strip provider prefix if present
	if idx := strings.Index(resolvedModel, "/"); idx >= 0 {
		resolvedModel = resolvedModel[idx+1:]
	}

	metadata := map[string]any{
		"kiro:agent_mode": "vibe",
	}
	if conversationId != "" {
		metadata["kiro:conversation_id"] = conversationId
	}

	origin := "AI_EDITOR"
	if current != nil {
		if u, ok := current["userInputMessage"].(map[string]any); ok && u != nil {
			if v, ok := u["origin"].(string); ok && v != "" {
				origin = v
			}
		}
	}

	result := map[string]any{
		"model":    resolvedModel,
		"input":    input,
		"stream":   true,
		"origin":   origin,
		"metadata": metadata,
	}
	if len(tools) > 0 {
		result["tools"] = tools
	}
	if profileArn != "" {
		result["profileArn"] = profileArn
	}
	if reasoning != nil {
		result["reasoning"] = reasoning
	}
	return result
}

// TransformCreateResponseStreamToSSE transforms CreateResponse SSE into OpenAI SSE chunks.
// It returns a reader over the transformed OpenAI SSE stream.
func TransformCreateResponseStreamToSSE(upstream io.Reader, model ...string) io.Reader {
	m := ""
	if len(model) > 0 {
		m = model[0]
	}
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		id := fmt.Sprintf("chatcmpl-cr-%d", time.Now().UnixNano())
		created := time.Now().Unix()
		sseChunk := func(delta map[string]any, finish string, usage map[string]any) []byte {
			chunk := map[string]any{
				"id":      id,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   m,
				"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": nil}},
			}
			if finish != "" {
				chunk["choices"].([]map[string]any)[0]["finish_reason"] = finish
			}
			if usage != nil {
				chunk["usage"] = usage
			}
			b, _ := json.Marshal(chunk)
			out := []byte("data: " + string(b) + "\n\n")
			return out
		}

		toolIdx := -1
		curCallId := ""
		openCalls := map[string]bool{}
		arguedCalls := map[string]bool{}
		hasToolCalls := false
		emittedRole := false

		scanner := bufio.NewScanner(upstream)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataStr == "" || dataStr == "[DONE]" {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(dataStr), &ev); err != nil {
				continue
			}
			handleEvent(ev, sseChunk, &toolIdx, &curCallId, openCalls, arguedCalls, &hasToolCalls, &emittedRole, pw)
		}
		pw.Write([]byte("data: [DONE]\n\n"))
	}()
	return pr
}

// TransformCreateResponseStreamToSSEToWriter is a writer-based variant.
func TransformCreateResponseStreamToSSEToWriter(upstream io.Reader, w io.Writer, model string) error {
	r := TransformCreateResponseStreamToSSE(upstream, model)
	_, err := io.Copy(w, r)
	return err
}

func handleEvent(ev map[string]any, sseChunk func(map[string]any, string, map[string]any) []byte, toolIdx *int, curCallId *string, openCalls map[string]bool, arguedCalls map[string]bool, hasToolCalls *bool, emittedRole *bool, w io.Writer) {
	etype, _ := ev["type"].(string)
	delta, _ := ev["delta"].(string)
	switch {
	case etype == "response.output_text.delta" && delta != "":
		d := map[string]any{"content": delta}
		if !*emittedRole {
			d = map[string]any{"role": "assistant", "content": delta}
		}
		*emittedRole = true
		w.Write(sseChunk(d, "", nil))
	case (etype == "response.reasoning_text.delta" || etype == "response.reasoning.delta") && delta != "":
		d := map[string]any{"reasoning_content": delta}
		if !*emittedRole {
			d = map[string]any{"role": "assistant", "reasoning_content": delta}
		}
		*emittedRole = true
		w.Write(sseChunk(d, "", nil))
	case etype == "response.refusal.delta" && delta != "":
		d := map[string]any{"content": delta}
		if !*emittedRole {
			d = map[string]any{"role": "assistant", "content": delta}
		}
		*emittedRole = true
		w.Write(sseChunk(d, "", nil))
	case etype == "response.refusal.done":
		if r, ok := ev["refusal"].(string); ok && r != "" {
			w.Write(sseChunk(map[string]any{"content": r}, "", nil))
		}
	case etype == "response.output_item.added":
		item, _ := ev["item"].(map[string]any)
		if item != nil && item["type"] == "function_call" {
			cid := ""
			if v, ok := item["call_id"].(string); ok {
				cid = v
			} else if v, ok := item["id"].(string); ok {
				cid = v
			}
			*curCallId = cid
			openCalls[cid] = true
			*hasToolCalls = true
			*toolIdx++
			name := ""
			if v, ok := item["name"].(string); ok {
				name = v
			}
			tc := []map[string]any{{
				"index":    *toolIdx,
				"id":       cid,
				"type":     "function",
				"function": map[string]any{"name": name, "arguments": ""},
			}}
			w.Write(sseChunk(map[string]any{"tool_calls": tc}, "", nil))
			*emittedRole = true
		}
	case etype == "response.function_call_arguments.delta" && delta != "":
		if *curCallId != "" {
			arguedCalls[*curCallId] = true
		}
		tc := []map[string]any{{"index": *toolIdx, "function": map[string]any{"arguments": delta}}}
		w.Write(sseChunk(map[string]any{"tool_calls": tc}, "", nil))
	case etype == "response.output_item.done":
		item, _ := ev["item"].(map[string]any)
		if item != nil && item["type"] == "function_call" {
			cid := ""
			if v, ok := item["call_id"].(string); ok {
				cid = v
			} else if v, ok := item["id"].(string); ok {
				cid = v
			}
			if openCalls[cid] {
				delete(openCalls, cid)
				if !arguedCalls[cid] {
					if args, ok := item["arguments"].(string); ok && args != "" {
						tc := []map[string]any{{"index": *toolIdx, "function": map[string]any{"arguments": args}}}
						w.Write(sseChunk(map[string]any{"tool_calls": tc}, "", nil))
					}
				}
				delete(arguedCalls, cid)
			}
		}
	case etype == "response.completed" || etype == "response.incomplete":
		res, _ := ev["response"].(map[string]any)
		incomplete := etype == "response.incomplete"
		if res != nil {
			if st, ok := res["status"].(string); ok && st == "incomplete" {
				incomplete = true
			}
		}
		finish := "stop"
		if *hasToolCalls {
			finish = "tool_calls"
		}
		if incomplete && res != nil {
			if idr, ok := res["incomplete_details"].(map[string]any); ok {
				if r, ok := idr["reason"].(string); ok {
					m := map[string]string{
						"max_output_tokens":                     "length",
						"content_filter":                        "content_filter",
						"compaction_required_for_model_handoff": "length",
					}
					if v, ok := m[r]; ok {
						finish = v
					}
				}
			}
		}
		usage := map[string]any(nil)
		if res != nil {
			if u, ok := res["usage"].(map[string]any); ok {
				inTokens := 0.0
				outTokens := 0.0
				if v, ok := u["input_tokens"].(float64); ok {
					inTokens = v
				}
				if v, ok := u["output_tokens"].(float64); ok {
					outTokens = v
				}
				usage = map[string]any{
					"prompt_tokens":     inTokens,
					"completion_tokens": outTokens,
					"total_tokens":      inTokens + outTokens,
				}
			}
		}
		w.Write(sseChunk(map[string]any{}, finish, usage))
	}
}

// Aliases for possible upstream naming.
func BuildKiroCreateResponseURL(region ...any) string {
	if len(region) == 0 {
		return KiroCreateResponseURL("")
	}
	if r, ok := region[0].(string); ok {
		return KiroCreateResponseURL(r)
	}
	return KiroCreateResponseURL(region[0])
}
func BuildKiroCreateResponseHeaders(args ...any) map[string]string {
	if len(args) == 1 {
		if m, ok := args[0].(map[string]any); ok {
			return KiroCreateResponseHeaders(m, map[string]any{})
		}
	}
	if len(args) >= 2 {
		c, _ := args[0].(map[string]any)
		b, _ := args[1].(map[string]any)
		if c == nil {
			c = map[string]any{}
		}
		if b == nil {
			b = map[string]any{}
		}
		return KiroCreateResponseHeaders(c, b)
	}
	// support struct-arg form {credentials, body}
	if len(args) == 1 {
		if m, ok := args[0].(map[string]any); ok {
			if _, hasCreds := m["credentials"]; hasCreds {
				creds, _ := m["credentials"].(map[string]any)
				body, _ := m["body"].(map[string]any)
				if creds == nil {
					creds = map[string]any{}
				}
				if body == nil {
					body = map[string]any{}
				}
				return KiroCreateResponseHeaders(creds, body)
			}
		}
	}
	return KiroCreateResponseHeaders(map[string]any{}, map[string]any{})
}
func BuildKiroCreateResponseBody(args ...any) map[string]any {
	// normalize args
	body := map[string]any{}
	creds := map[string]any{}
	model := ""
	for _, a := range args {
		switch v := a.(type) {
		case string:
			model = v
		case map[string]any:
			if _, ok := v["body"]; ok {
				if b, ok2 := v["body"].(map[string]any); ok2 {
					body = b
				}
				if c, ok2 := v["credentials"].(map[string]any); ok2 {
					creds = c
				}
				if m, ok2 := v["model"].(string); ok2 {
					model = m
				}
			} else {
				// ambiguous: first map as body
				if len(body) == 0 {
					body = v
				} else {
					creds = v
				}
			}
		}
	}
	return KiroCreateResponseBody(body, creds, model)
}

// KiroCreateResponseBody already variadic-compatible? It is not; keep wrapper name same?
