package executor

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

func TestFreebuff_EnsureMarker(t *testing.T) {
	// 1. Empty messages
	b1 := map[string]any{"messages": []any{}}
	ensureFreebuffMarker(b1)
	msgs1 := b1["messages"].([]any)
	if len(msgs1) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs1))
	}
	m1 := msgs1[0].(map[string]any)
	if m1["role"] != "system" || m1["content"] != freebuffSystemMarker {
		t.Errorf("unexpected content: %v", m1)
	}

	// 2. Existing leading system message
	b2 := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "Custom system prompt"},
			map[string]any{"role": "user", "content": "Hello"},
		},
	}
	ensureFreebuffMarker(b2)
	msgs2 := b2["messages"].([]any)
	m2 := msgs2[0].(map[string]any)
	expected2 := freebuffSystemMarker + "\n\nCustom system prompt"
	if m2["content"] != expected2 {
		t.Errorf("expected %q, got %q", expected2, m2["content"])
	}

	// 3. Already marked system prompt - should be idempotent
	b3 := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are Buffy, the strategic coding assistant.\n\nCustom prompt"},
		},
	}
	ensureFreebuffMarker(b3)
	msgs3 := b3["messages"].([]any)
	m3 := msgs3[0].(map[string]any)
	if m3["content"] != "You are Buffy, the strategic coding assistant.\n\nCustom prompt" {
		t.Errorf("unexpected re-marker: %v", m3["content"])
	}

	// 4. Leading user message (no system message)
	b4 := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Write code"},
		},
	}
	ensureFreebuffMarker(b4)
	msgs4 := b4["messages"].([]any)
	if len(msgs4) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs4))
	}
	if msgs4[0].(map[string]any)["role"] != "system" || msgs4[1].(map[string]any)["role"] != "user" {
		t.Errorf("unexpected message order: %v", msgs4)
	}
}

func TestFreebuff_EnsureEndTurnTool(t *testing.T) {
	// 1. No tools
	b1 := map[string]any{}
	ensureFreebuffEndTurnTool(b1)
	if _, ok := b1["tools"]; ok {
		t.Errorf("expected no tools key added if original had none")
	}

	// 2. Tools present without end_turn
	b2 := map[string]any{
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "bash",
				},
			},
		},
	}
	ensureFreebuffEndTurnTool(b2)
	tools2 := b2["tools"].([]any)
	if len(tools2) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools2))
	}
	lastTool := tools2[1].(map[string]any)
	fn := lastTool["function"].(map[string]any)
	if fn["name"] != "end_turn" {
		t.Errorf("expected end_turn, got %v", fn["name"])
	}

	// 3. Tools already has end_turn
	b3 := map[string]any{
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": "end_turn",
				},
			},
		},
	}
	ensureFreebuffEndTurnTool(b3)
	tools3 := b3["tools"].([]any)
	if len(tools3) != 1 {
		t.Errorf("expected still 1 tool, got %d", len(tools3))
	}
}

func TestFreebuff_RootAgentIdForModel(t *testing.T) {
	cases := map[string]string{
		"deepseek/deepseek-v4-flash":      "base3-free-deepseek-flash",
		"z-ai/glm-5.3-flash":              "base3-free-glm-5-3-flash",
		"openai/gpt-5.6-luna":             "base3-free-luna",
		"mimo/mimo-v2.5":                  "base3-free-mimo",
		"upstage/solar-pro4":              "base3-free-solar-pro4",
		"meta/muse-spark-1.2-contributor": "base3-free-muse-spark",
		"anthropic/claude-fable-5":        "base3-free-fable",
		"unknown/random-model":            "base2-free",
	}

	for m, expected := range cases {
		got := rootAgentIdForModel(m)
		if got != expected {
			t.Errorf("rootAgentIdForModel(%q) = %q, want %q", m, got, expected)
		}
	}
}

func TestForwardFreebuff_FullCycle(t *testing.T) {
	var sessionCalls int64
	var startRunCalls int64
	var finishRunCalls int64
	var chatCalls int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == freebuffSessionPath:
			atomic.AddInt64(&sessionCalls, 1)
			if r.Header.Get("Authorization") != "Bearer test-fb-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.MarshalWrite(w, map[string]any{
				"status":     "active",
				"instanceId": "fb-inst-999",
				"expiresAt":  "2030-01-01T00:00:00Z",
			})

		case r.URL.Path == freebuffRunPath:
			var req map[string]any
			_ = json.UnmarshalRead(r.Body, &req)
			action, _ := req["action"].(string)
			if action == "START" {
				atomic.AddInt64(&startRunCalls, 1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.MarshalWrite(w, map[string]any{
					"runId": "run-xyz-123",
				})
			} else if action == "FINISH" {
				atomic.AddInt64(&finishRunCalls, 1)
				w.WriteHeader(http.StatusOK)
			}

		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			atomic.AddInt64(&chatCalls, 1)
			bodyBytes, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(bodyBytes, &body)

			// Assert codebuff_metadata
			meta, ok := body["codebuff_metadata"].(map[string]any)
			if !ok {
				t.Errorf("missing codebuff_metadata")
			} else {
				if meta["run_id"] != "run-xyz-123" {
					t.Errorf("expected run_id run-xyz-123, got %v", meta["run_id"])
				}
				if meta["freebuff_instance_id"] != "fb-inst-999" {
					t.Errorf("expected freebuff_instance_id fb-inst-999, got %v", meta["freebuff_instance_id"])
				}
				// client_id cloaking: bare "9router-" branding must never leak
				// upstream; default requests carry an unbranded random id.
				clientID, _ := meta["client_id"].(string)
				if clientID == "" {
					t.Errorf("expected non-empty client_id")
				}
				if strings.HasPrefix(clientID, "9router-") {
					t.Errorf("client_id must not carry router branding, got %v", clientID)
				}
			}

			// Assert system prompt
			msgs := body["messages"].([]any)
			firstMsg := msgs[0].(map[string]any)
			if !strings.HasPrefix(firstMsg["content"].(string), freebuffSystemMarker) {
				t.Errorf("system prompt missing marker: %v", firstMsg["content"])
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.MarshalWrite(w, map[string]any{
				"id":      "chatcmpl-fb-1",
				"object":  "chat.completion",
				"created": 12345678,
				"model":   "deepseek/deepseek-v4-flash",
				"choices": []any{
					map[string]any{
						"index": 0,
						"message": map[string]any{
							"role":    "assistant",
							"content": "Hello from Freebuff!",
						},
						"finish_reason": "stop",
					},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	// Clear session cache for clean test
	clearFreebuffSession("test-fb-token", "deepseek/deepseek-v4-flash")

	reqBody := []byte(`{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"Hi"}]}`)
	recorder := httptest.NewRecorder()
	req := &Request{
		Ctx:    context.Background(),
		Client: ts.Client(),
		Config: &providers.ProviderConfig{
			BaseURL: ts.URL + "/chat/completions",
		},
		APIKey:   "test-fb-token",
		Body:     reqBody,
		IsStream: false,
	}

	err := ForwardFreebuff(recorder, req)
	if err != nil {
		t.Fatalf("ForwardFreebuff failed: %v", err)
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if atomic.LoadInt64(&sessionCalls) != 1 {
		t.Errorf("expected 1 session call, got %d", sessionCalls)
	}
	if atomic.LoadInt64(&startRunCalls) != 1 {
		t.Errorf("expected 1 startRun calls total, got %d", startRunCalls)
	}
	if atomic.LoadInt64(&chatCalls) != 1 {
		t.Errorf("expected 1 chat call, got %d", chatCalls)
	}

	// Second call with same token + model should reuse session (sessionCalls remains 1)
	recorder2 := httptest.NewRecorder()
	err2 := ForwardFreebuff(recorder2, req)
	if err2 != nil {
		t.Fatalf("second ForwardFreebuff failed: %v", err2)
	}
	if atomic.LoadInt64(&sessionCalls) != 1 {
		t.Errorf("expected session to be cached, but sessionCalls is %d", sessionCalls)
	}
	if atomic.LoadInt64(&startRunCalls) != 2 {
		t.Errorf("expected 2 startRun calls total, got %d", startRunCalls)
	}

	// Third call: a stored per-account fingerprintId must be reused verbatim
	// as client_id (CLI-shaped), not replaced by a random id.
	var cloakedClientID string
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == freebuffSessionPath:
			_, _ = w.Write([]byte(`{"status":"active","instanceId":"fb-inst-999","expiresAt":"2030-01-01T00:00:00Z"}`))
		case r.URL.Path == freebuffRunPath:
			var req map[string]any
			_ = json.UnmarshalRead(r.Body, &req)
			if action, _ := req["action"].(string); action == "START" {
				_, _ = w.Write([]byte(`{"runId":"run-xyz-123"}`))
			} else {
				w.WriteHeader(http.StatusOK)
			}
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			bodyBytes, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(bodyBytes, &body)
			meta, _ := body["codebuff_metadata"].(map[string]any)
			cloakedClientID, _ = meta["client_id"].(string)
			_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
		default:
			http.NotFound(w, r)
		}
	})
	clearFreebuffSession("test-fb-token", "deepseek/deepseek-v4-flash")
	req.ConnData = map[string]any{"fingerprintId": "fp-account-123"}
	recorder3 := httptest.NewRecorder()
	if err := ForwardFreebuff(recorder3, req); err != nil {
		t.Fatalf("third ForwardFreebuff failed: %v", err)
	}
	if cloakedClientID != "fp-account-123" {
		t.Errorf("expected client_id 'fp-account-123', got %q", cloakedClientID)
	}
}

func TestForwardFreebuff_MissingToken(t *testing.T) {
	req := &Request{
		Ctx:    context.Background(),
		Client: http.DefaultClient,
		Config: &providers.ProviderConfig{
			BaseURL: "http://localhost:1234/chat/completions",
		},
		APIKey: "",
		Body:   []byte(`{"model":"deepseek/deepseek-v4-flash","messages":[]}`),
	}
	rec := httptest.NewRecorder()
	err := ForwardFreebuff(rec, req)
	if err == nil {
		t.Fatalf("expected error on missing token, got nil")
	}
}

func TestForwardFreebuff_InvalidJSON(t *testing.T) {
	req := &Request{
		Ctx:    context.Background(),
		Client: http.DefaultClient,
		Config: &providers.ProviderConfig{
			BaseURL: "http://localhost:1234/chat/completions",
		},
		APIKey: "token",
		Body:   []byte(`invalid-json`),
	}
	rec := httptest.NewRecorder()
	err := ForwardFreebuff(rec, req)
	if err == nil {
		t.Fatalf("expected error on invalid JSON, got nil")
	}
}

func TestFreebuff_RequestSession_ModelLocked_Recovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != freebuffSessionPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status": "model_locked",
			"currentModel": "z-ai/glm-5.3-flash",
			"requestedModel": "z-ai/glm-5.3-flash",
			"instanceId": "inst-recovered-123",
			"expiresAt": "2099-01-01T00:00:00Z"
		}`))
	}))
	defer srv.Close()

	token := "tok-recovery-test"
	model := "z-ai/glm-5.3-flash"
	clearFreebuffSession(token, model)

	sess, err := requestFreebuffSession(context.Background(), srv.Client(), srv.URL, token, model)
	if err != nil {
		t.Fatalf("expected recovery when requestedModel==currentModel, got err: %v", err)
	}
	if sess == nil || sess.InstanceID != "inst-recovered-123" {
		t.Fatalf("expected instanceId inst-recovered-123, got %v", sess)
	}

	// Verify cached
	cached, ok := getFreebuffSession(token, model)
	if !ok || cached.InstanceID != "inst-recovered-123" {
		t.Fatalf("expected session to be cached")
	}
}

func TestFreebuff_RequestSession_ModelLocked_ConflictError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != freebuffSessionPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{
			"status": "model_locked",
			"currentModel": "z-ai/glm-5.3-flash",
			"requestedModel": "mimo/mimo-v2.5",
			"accessTier": "limited"
		}`))
	}))
	defer srv.Close()

	token := "tok-lock-test"
	model := "mimo/mimo-v2.5"
	clearFreebuffSession(token, model)

	sess, err := requestFreebuffSession(context.Background(), srv.Client(), srv.URL, token, model)
	if sess != nil {
		t.Fatalf("expected nil session on model_locked mismatch, got %v", sess)
	}
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var ue *proxy.UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *proxy.UpstreamError, got %T: %v", err, err)
	}
	if ue.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", ue.StatusCode)
	}

	var errJson struct {
		Error struct {
			Message      string `json:"message"`
			Type         string `json:"type"`
			Code         string `json:"code"`
			CurrentModel string `json:"currentModel"`
		} `json:"error"`
	}
	if err := json.Unmarshal(ue.Body, &errJson); err != nil {
		t.Fatalf("failed to unmarshal error body %s: %v", string(ue.Body), err)
	}

	if errJson.Error.Type != "model_locked" || errJson.Error.Code != "model_locked" {
		t.Errorf("expected type/code=model_locked, got %v", errJson.Error)
	}
	if errJson.Error.CurrentModel != "z-ai/glm-5.3-flash" {
		t.Errorf("expected currentModel=z-ai/glm-5.3-flash, got %q", errJson.Error.CurrentModel)
	}
	expectedMsg := `Freebuff session is locked to "z-ai/glm-5.3-flash" — it cannot serve mimo/mimo-v2.5. Use "z-ai/glm-5.3-flash" or wait for the session to expire (~1h).`
	if errJson.Error.Message != expectedMsg {
		t.Errorf("expected message %q, got %q", expectedMsg, errJson.Error.Message)
	}
}

func TestForwardFreebuff_ModelLocked_Writes409(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == freebuffSessionPath {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{
				"status": "model_locked",
				"currentModel": "z-ai/glm-5.3-flash",
				"requestedModel": "mimo/mimo-v2.5"
			}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	token := "tok-fwd-lock-test"
	model := "mimo/mimo-v2.5"
	clearFreebuffSession(token, model)

	req := &Request{
		Ctx:    context.Background(),
		Client: srv.Client(),
		Config: &providers.ProviderConfig{
			BaseURL: srv.URL + "/chat/completions",
		},
		APIKey: token,
		Body:   []byte(`{"model":"mimo/mimo-v2.5","messages":[{"role":"user","content":"hello"}]}`),
	}

	rec := httptest.NewRecorder()
	err := ForwardFreebuff(rec, req)
	if err == nil {
		t.Fatalf("expected error on model_locked, got nil")
	}

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected recorder HTTP 409, got %d", rec.Code)
	}

	var errJson struct {
		Error struct {
			Message      string `json:"message"`
			Type         string `json:"type"`
			Code         string `json:"code"`
			CurrentModel string `json:"currentModel"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errJson); err != nil {
		t.Fatalf("failed to unmarshal recorder body %s: %v", rec.Body.String(), err)
	}

	if errJson.Error.Type != "model_locked" || errJson.Error.Code != "model_locked" {
		t.Errorf("expected type/code=model_locked, got %v", errJson.Error)
	}
	if errJson.Error.CurrentModel != "z-ai/glm-5.3-flash" {
		t.Errorf("expected currentModel=z-ai/glm-5.3-flash, got %q", errJson.Error.CurrentModel)
	}
}

func TestFreebuff_RequestSession_EmptyInstanceID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != freebuffSessionPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status": "ok",
			"instanceId": ""
		}`))
	}))
	defer srv.Close()

	token := "tok-empty-instance"
	model := "z-ai/glm-5.3-flash"
	clearFreebuffSession(token, model)

	sess, err := requestFreebuffSession(context.Background(), srv.Client(), srv.URL, token, model)
	if sess != nil {
		t.Fatalf("expected nil session on empty instanceId, got %v", sess)
	}
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "freebuff session returned empty instanceId") {
		t.Errorf("unexpected error message: %v", err)
	}
}
