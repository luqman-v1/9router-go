package executor

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/customjson"
	"9router/proxy/internal/proxy"

	json "encoding/json/v2"

	"github.com/google/uuid"
)

// ForwardCustomJSON serves a custom JSON node (upstream
// open-sse/executors/custom-json.js). The endpoint answers one JSON document,
// so the reply is lifted out of it and re-presented as an OpenAI Chat
// Completion — a completion object, or two SSE chunks and [DONE] for a
// streaming client — and handed to the same writers every OpenAI upstream
// uses, which translate it for Claude and Responses clients.
//
// A business request may have side effects, so it is sent exactly once:
// no transient-status retry here, and the caller stops account and combo
// fallback after it (see chat.isCustomJSONConn).
func ForwardCustomJSON(w http.ResponseWriter, req *Request) error {
	cfg, endpoint, err := customjson.FromConnection(req.ConnData)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadRequest, "Custom JSON configuration: "+err.Error())
	}
	model := bodyModel(req.Body)
	payload, err := customjson.BuildRequest(cfg, model, req.Body)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadRequest, "Custom JSON request: "+err.Error())
	}
	headers, err := customjson.Headers(cfg, req.APIKey)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusUnauthorized, err.Error())
	}

	resp, err := proxy.DoRequestOnce(req.Ctx, req.Client, cfg.Method, endpoint, headers, payload)
	if err != nil {
		return fmt.Errorf("ForwardCustomJSON upstream: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, constants.MaxUpstreamBodyBytes))
	if err != nil {
		return fmt.Errorf("ForwardCustomJSON read: %w", err)
	}
	text, err := customjson.ExtractText(cfg, data)
	if err != nil {
		return proxy.UpstreamFailure(http.StatusBadGateway, "Custom JSON response: "+err.Error())
	}

	// An empty reply is still the business endpoint's answer: serve it
	// rather than report an empty upstream, which would invite a replay.
	ctx := proxy.WithEmptyCompletionAllowed(proxy.WithZeroContentProbe(req.Ctx, false))
	reply := customJSONReply{ID: "chatcmpl-" + uuid.NewString(), Created: time.Now().Unix(), Model: model}
	if req.IsStream {
		local := *req
		local.Ctx = ctx
		return execSSEStream(w, bytes.NewReader(reply.sse(text)), &local)
	}
	return jsonResponse(ctx, w, bytes.NewReader(reply.completion(text)), req.TranslateResp, req.ResponseBuf)
}

// bodyModel is the upstream model id the router already wrote into the
// translated body (provider prefix stripped).
func bodyModel(body []byte) string {
	var head struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &head)
	return head.Model
}

type customJSONReply struct {
	ID      string
	Created int64
	Model   string
}

type customJSONChoice struct {
	Index        int            `json:"index"`
	Message      map[string]any `json:"message,omitzero"`
	Delta        map[string]any `json:"delta,omitzero"`
	FinishReason *string        `json:"finish_reason"`
}

type customJSONChunk struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []customJSONChoice `json:"choices"`
}

func (r customJSONReply) chunk(object string, choice customJSONChoice) []byte {
	out, _ := json.Marshal(customJSONChunk{ID: r.ID, Object: object, Created: r.Created, Model: r.Model, Choices: []customJSONChoice{choice}})
	return out
}

func (r customJSONReply) completion(text string) []byte {
	stop := "stop"
	return r.chunk("chat.completion", customJSONChoice{
		Message:      map[string]any{"role": "assistant", "content": text},
		FinishReason: &stop,
	})
}

func (r customJSONReply) sse(text string) []byte {
	stop := "stop"
	var buf bytes.Buffer
	for _, payload := range [][]byte{
		r.chunk("chat.completion.chunk", customJSONChoice{Delta: map[string]any{"role": "assistant", "content": text}}),
		r.chunk("chat.completion.chunk", customJSONChoice{Delta: map[string]any{}, FinishReason: &stop}),
	} {
		buf.WriteString("data: ")
		buf.Write(payload)
		buf.WriteString("\n\n")
	}
	buf.WriteString("data: [DONE]\n\n")
	return buf.Bytes()
}
