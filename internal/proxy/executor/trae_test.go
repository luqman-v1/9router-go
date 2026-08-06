package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"9router/proxy/internal/providers"
)

// TestForwardTrae_StreamsAccumulatedThought drives a full streaming round-trip
// against a mock Trae backend: create session, then plan_item events where the
// first id's thought grows cumulatively (longest wins) alongside a second id.
func TestForwardTrae_StreamsAccumulatedThought(t *testing.T) {
	var mu sync.Mutex
	sawCreate := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat_sessions"):
			if auth := r.Header.Get("Authorization"); auth != "Cloud-IDE-JWT jwt-token" {
				t.Errorf("expected Cloud-IDE-JWT auth, got %q", auth)
			}
			var body struct {
				Mode           string `json:"mode"`
				InitialMessage struct {
					Query        string `json:"query"`
					ModelName    string `json:"model_name"`
					CommonParams string `json:"common_params"`
				} `json:"initial_message"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			if body.Mode != "work" {
				t.Errorf("expected mode work, got %q", body.Mode)
			}
			if !strings.Contains(body.InitialMessage.CommonParams, `"solo_chat_mode":"work"`) {
				t.Errorf("expected solo_chat_mode work in common_params, got %q", body.InitialMessage.CommonParams)
			}
			sawCreate = true
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"chat_session_id": "sess-1", "message_id": "msg-1"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/events"):
			if q := r.URL.Query().Get("reply_to_message_id"); q != "msg-1" {
				t.Errorf("expected reply_to_message_id=msg-1, got %q", q)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			frames := []string{
				"event: plan_item\ndata: {\"id\":\"p1\",\"thought\":\"Hel\"}\n\n",
				"event: plan_item\ndata: {\"id\":\"p1\",\"thought\":\"Hello\"}\n\n",
				"event: plan_item\ndata: {\"id\":\"p2\",\"thought\":\" world\"}\n\n",
				"event: token_usage\ndata: {\"prompt_tokens\":5,\"completion_tokens\":11,\"total_tokens\":16}\n\n",
				"event: done\ndata: {}\n\n",
			}
			for _, f := range frames {
				fmt.Fprint(w, f)
				fl.Flush()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{BaseURL: upstream.URL}
	body, _ := json.Marshal(map[string]any{
		"model":    "work",
		"stream":   true,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	rec := httptest.NewRecorder()
	err := ForwardTrae(rec, &Request{
		Ctx:      context.Background(),
		Client:   upstream.Client(),
		Config:   cfg,
		APIKey:   "jwt-token",
		Body:     body,
		IsStream: true,
	})
	if err != nil {
		t.Fatalf("ForwardTrae: %v", err)
	}
	if !sawCreate {
		t.Error("expected create session call")
	}

	got := rec.Body.String()
	for _, want := range []string{
		`"content":"Hel"`,
		`"content":"lo"`,
		`"content":" world"`,
		`"finish_reason":"stop"`,
		`"total_tokens":16`,
		"data: [DONE]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

// TestForwardTrae_NonStream drives the non-streaming path: the events stream is
// drained to `done`, then a single chat.completion JSON with full accumulated
// text and usage is returned.
func TestForwardTrae_NonStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat_sessions"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"chat_session_id": "sess-2", "message_id": "msg-2"},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/events"):
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			fmt.Fprint(w, "event: plan_item\ndata: {\"id\":\"p1\",\"thought\":\"Hello world\"}\n\n")
			fl.Flush()
			fmt.Fprint(w, "event: token_usage\ndata: {\"prompt_tokens\":2,\"completion_tokens\":11,\"total_tokens\":13}\n\n")
			fl.Flush()
			fmt.Fprint(w, "event: done\ndata: {}\n\n")
			fl.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{BaseURL: upstream.URL}
	body, _ := json.Marshal(map[string]any{
		"model":    "auto",
		"stream":   false,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})
	rec := httptest.NewRecorder()
	err := ForwardTrae(rec, &Request{
		Ctx:      context.Background(),
		Client:   upstream.Client(),
		Config:   cfg,
		APIKey:   "jwt-token",
		Body:     body,
		IsStream: false,
	})
	if err != nil {
		t.Fatalf("ForwardTrae: %v", err)
	}

	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage traeUsage `json:"usage"`
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
	if out.Usage.TotalTokens != 13 {
		t.Errorf("expected total_tokens 13, got %d", out.Usage.TotalTokens)
	}
}
