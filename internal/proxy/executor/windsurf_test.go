package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// wsContentFrame / wsDoneFrame / wsTrailerFrame build the gRPC-web response
// frames the mock upstream serves (mirroring the JS decoder's expectations).
func wsContentFrame(text string) []byte { return wsGRPCWebFrame(wsMsg(1, wsStr(1, text))) }

func wsDoneFrame(prompt, completion int) []byte {
	usage := append(wsVarintField(1, uint64(prompt)), wsVarintField(2, uint64(completion))...)
	return wsGRPCWebFrame(wsMsg(3, wsMsg(1, usage)))
}

func wsTrailerFrame(status string) []byte {
	frame := make([]byte, 5+len(status))
	frame[0] = 0x80
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(status)))
	copy(frame[5:], status)
	return frame
}

// TestForwardWindsurf_StreamsGRPCWeb drives a streaming round-trip against a
// mock gRPC-web backend, asserting the request is a well-formed framed protobuf
// with the model alias applied, and the decoded frames become OpenAI SSE chunks.
func TestForwardWindsurf_StreamsGRPCWeb(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/grpc-web+proto" {
			t.Errorf("expected grpc-web content type, got %q", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer ws-key" {
			t.Errorf("expected Bearer auth, got %q", auth)
		}
		if x := r.Header.Get("X-Grpc-Web"); x != "1" {
			t.Errorf("expected X-Grpc-Web: 1, got %q", x)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) < 5 || body[0] != 0x00 {
			t.Fatalf("request body is not a gRPC-web frame")
		}
		if ln := binary.BigEndian.Uint32(body[1:5]); int(ln) != len(body)-5 {
			t.Errorf("frame length %d != payload %d", ln, len(body)-5)
		}
		if !bytes.Contains(body, []byte("claude-opus-4-7-max")) {
			t.Error("expected aliased wire model in request")
		}
		if bytes.Contains(body, []byte("claude-opus-4.7-max")) {
			t.Error("catalog model name leaked into wire request")
		}
		if !bytes.Contains(body, []byte("hi")) {
			t.Error("expected message content in request")
		}

		w.Write(wsContentFrame("Hel"))
		w.Write(wsContentFrame("lo"))
		w.Write(wsDoneFrame(5, 7))
		w.Write(wsTrailerFrame("grpc-status: 0"))
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{BaseURL: upstream.URL}
	body, _ := json.Marshal(map[string]any{
		"model":    "claude-opus-4.7-max",
		"stream":   true,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	rec := httptest.NewRecorder()
	err := ForwardWindsurf(rec, &Request{
		Ctx:      context.Background(),
		Client:   upstream.Client(),
		Config:   cfg,
		APIKey:   "ws-key",
		Body:     body,
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardWindsurf: %v", err)
	}

	got := rec.Body.String()
	for _, want := range []string{
		`"role":"assistant"`,
		`"content":"Hel"`,
		`"content":"lo"`,
		`"finish_reason":"stop"`,
		`"total_tokens":12`,
		"data: [DONE]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

// TestForwardWindsurf_NonStream aggregates decoded frames into a single
// chat.completion JSON (matching trae's non-stream pattern).
func TestForwardWindsurf_NonStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(wsContentFrame("Hello"))
		w.Write(wsContentFrame(" world"))
		w.Write(wsDoneFrame(3, 9))
		w.Write(wsTrailerFrame("grpc-status: 0"))
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{BaseURL: upstream.URL}
	body, _ := json.Marshal(map[string]any{
		"model":    "gpt-5.4-medium",
		"stream":   false,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	rec := httptest.NewRecorder()
	err := ForwardWindsurf(rec, &Request{
		Ctx:      context.Background(),
		Client:   upstream.Client(),
		Config:   cfg,
		APIKey:   "ws-key",
		Body:     body,
		IsStream: false,
	})
	if err != nil {
		t.Fatalf("ForwardWindsurf: %v", err)
	}

	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if out.Object != "chat.completion" {
		t.Errorf("expected chat.completion, got %q", out.Object)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "Hello world" || out.Choices[0].FinishReason != "stop" {
		t.Errorf("unexpected choices: %+v", out.Choices)
	}
	if out.Usage.TotalTokens != 12 || out.Usage.PromptTokens != 3 || out.Usage.CompletionTokens != 9 {
		t.Errorf("unexpected usage: %+v", out.Usage)
	}
}

// TestForwardWindsurf_UpstreamError passes through a non-200 response.
func TestForwardWindsurf_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"bad model"}}`)
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{BaseURL: upstream.URL}
	body, _ := json.Marshal(map[string]any{"model": "x", "stream": true, "messages": []map[string]any{{"role": "user", "content": "hi"}}})
	rec := httptest.NewRecorder()
	err := ForwardWindsurf(rec, &Request{
		Ctx:      context.Background(),
		Client:   upstream.Client(),
		Config:   cfg,
		APIKey:   "ws-key",
		Body:     body,
		IsStream: true,
	})
	var ue *proxy.UpstreamError
	if err == nil || !errors.As(err, &ue) {
		t.Fatalf("expected upstream error, got %v", err)
	}
	if ue.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", ue.StatusCode)
	}
}
