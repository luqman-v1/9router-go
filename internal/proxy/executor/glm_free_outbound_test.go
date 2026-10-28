package executor

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

func TestGLM53FlashFree_OutboundWireScenarios(t *testing.T) {
	t.Run("CapabilityCheck", func(t *testing.T) {
		capsOpencode := providers.GetCapabilitiesForModel("opencode", "glm-5.3-flash")
		if !capsOpencode.Reasoning {
			t.Errorf("expected Reasoning=true for (opencode, glm-5.3-flash), got false: %+v", capsOpencode)
		}

		capsBareFree := providers.GetCapabilitiesForModel("", "glm-5.3-flash-free")
		if !capsBareFree.Reasoning {
			t.Errorf("expected Reasoning=true for ('', glm-5.3-flash-free), got false: %+v", capsBareFree)
		}

		capsOpencodeFree := providers.GetCapabilitiesForModel("opencode", "glm-5.3-flash-free")
		if !capsOpencodeFree.Reasoning {
			t.Errorf("expected Reasoning=true for (opencode, glm-5.3-flash-free), got false: %+v", capsOpencodeFree)
		}

		capsOpencodeGo := providers.GetCapabilitiesForModel("opencode-go", "glm-5.3-flash")
		if !capsOpencodeGo.Reasoning {
			t.Errorf("expected Reasoning=true for (opencode-go, glm-5.3-flash), got false: %+v", capsOpencodeGo)
		}

		detailOpencode := providers.GetCapabilitiesDetailForModel("opencode", "glm-5.3-flash")
		if !detailOpencode.Reasoning {
			t.Errorf("expected Detail.Reasoning=true for (opencode, glm-5.3-flash), got false")
		}

		detailFree := providers.GetCapabilitiesDetailForModel("", "glm-5.3-flash-free")
		if !detailFree.Reasoning {
			t.Errorf("expected Detail.Reasoning=true for ('', glm-5.3-flash-free), got false")
		}
	})

	t.Run("NoIntent_DoesNotInjectReasoningParams", func(t *testing.T) {
		var parsed map[string]any

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &parsed)

			// Upstream check: if reasoning params were injected without client intent,
			// upstream returns 400 Bad Request
			if parsed["reasoning_effort"] != nil || parsed["thinking"] != nil || parsed["enable_thinking"] != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"unexpected reasoning parameter without intent","type":"invalid_request_error","code":400}}`))
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello from glm\"}}]}\n\ndata: [DONE]\n\n"))
		}))
		defer srv.Close()

		cfg := &providers.ProviderConfig{
			BaseURL: srv.URL,
		}

		rec := httptest.NewRecorder()
		req := &Request{
			Ctx:           context.Background(),
			Client:        srv.Client(),
			Config:        cfg,
			APIKey:        "test-key",
			Body:          []byte(`{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"ping"}]}`),
			IsStream:      true,
			TranslateResp: false,
		}

		err := ForwardOpencode(rec, req)
		if err != nil {
			t.Fatalf("unexpected ForwardOpencode error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}

		if parsed["reasoning_effort"] != nil {
			t.Errorf("outbound wire MUST NOT contain reasoning_effort when client has no intent, got: %v", parsed["reasoning_effort"])
		}
		if parsed["thinking"] != nil {
			t.Errorf("outbound wire MUST NOT contain thinking when client has no intent, got: %v", parsed["thinking"])
		}
		if parsed["enable_thinking"] != nil {
			t.Errorf("outbound wire MUST NOT contain enable_thinking when client has no intent, got: %v", parsed["enable_thinking"])
		}
		if parsed["stream"] != true {
			t.Errorf("outbound wire must enforce stream=true for opencode free tier, got: %v", parsed["stream"])
		}
		if parsed["model"] != "glm-5.3-flash-free" {
			t.Errorf("expected model glm-5.3-flash-free, got: %v", parsed["model"])
		}
	})

	t.Run("ExplicitIntent_PreservedOnOutboundWire", func(t *testing.T) {
		type intentCase struct {
			name       string
			clientJSON string
			checkKey   string
			expectVal  any
		}

		cases := []intentCase{
			{
				name:       "effort_low",
				clientJSON: `{"model":"glm-5.3-flash-free","reasoning_effort":"low","messages":[{"role":"user","content":"calc"}]}`,
				checkKey:   "reasoning_effort",
				expectVal:  "low",
			},
			{
				name:       "effort_medium",
				clientJSON: `{"model":"glm-5.3-flash-free","reasoning_effort":"medium","messages":[{"role":"user","content":"calc"}]}`,
				checkKey:   "reasoning_effort",
				expectVal:  "medium",
			},
			{
				name:       "effort_high",
				clientJSON: `{"model":"glm-5.3-flash-free","reasoning_effort":"high","messages":[{"role":"user","content":"calc"}]}`,
				checkKey:   "reasoning_effort",
				expectVal:  "high",
			},
			{
				name:       "thinking_enabled",
				clientJSON: `{"model":"glm-5.3-flash-free","thinking":{"type":"enabled"},"messages":[{"role":"user","content":"calc"}]}`,
				checkKey:   "thinking",
				expectVal:  map[string]any{"type": "enabled"},
			},
			{
				name:       "thinking_budget",
				clientJSON: `{"model":"glm-5.3-flash-free","thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"calc"}]}`,
				checkKey:   "thinking",
				expectVal:  map[string]any{"type": "enabled", "budget_tokens": float64(2048)},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var captured map[string]any
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(body, &captured)
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
				}))
				defer srv.Close()

				rec := httptest.NewRecorder()
				req := &Request{
					Ctx:      context.Background(),
					Client:   srv.Client(),
					Config:   &providers.ProviderConfig{BaseURL: srv.URL},
					APIKey:   "test-key",
					Body:     []byte(tc.clientJSON),
					IsStream: true,
				}

				if err := ForwardOpencode(rec, req); err != nil {
					t.Fatalf("ForwardOpencode failed: %v", err)
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("expected status 200, got %d", rec.Code)
				}
				if !reflect.DeepEqual(captured[tc.checkKey], tc.expectVal) {
					t.Errorf("expected wire field %s=%v, got: %v", tc.checkKey, tc.expectVal, captured[tc.checkKey])
				}
			})
		}
	})

	t.Run("ModelPrefixStripping", func(t *testing.T) {
		models := []struct {
			input    string
			expected string
		}{
			{"opencode/glm-5.3-flash-free", "glm-5.3-flash-free"},
			{"oc/glm-5.3-flash-free", "glm-5.3-flash-free"},
			{"glm-5.3-flash-free(medium)", "glm-5.3-flash-free"},
		}

		for _, m := range models {
			t.Run(m.input, func(t *testing.T) {
				var captured map[string]any
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					_ = json.Unmarshal(body, &captured)
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
				}))
				defer srv.Close()

				rec := httptest.NewRecorder()
				req := &Request{
					Ctx:      context.Background(),
					Client:   srv.Client(),
					Config:   &providers.ProviderConfig{BaseURL: srv.URL},
					APIKey:   "test-key",
					Body:     []byte(`{"model":"` + m.input + `","messages":[{"role":"user","content":"hi"}]}`),
					IsStream: true,
				}

				if err := ForwardOpencode(rec, req); err != nil {
					t.Fatalf("ForwardOpencode failed: %v", err)
				}
				if captured["model"] != m.expected {
					t.Errorf("expected model %s on wire, got: %v", m.expected, captured["model"])
				}
			})
		}
	})

	t.Run("NonStreamingConversion", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"non-streaming answer\"}}]}\n\ndata: [DONE]\n\n"))
		}))
		defer srv.Close()

		rec := httptest.NewRecorder()
		req := &Request{
			Ctx:           context.Background(),
			Client:        srv.Client(),
			Config:        &providers.ProviderConfig{BaseURL: srv.URL},
			APIKey:        "test-key",
			Body:          []byte(`{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"hi"}]}`),
			IsStream:      false,
			TranslateResp: false,
		}

		err := ForwardOpencode(rec, req)
		if err != nil {
			t.Fatalf("ForwardOpencode failed in non-streaming mode: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		var respObj map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &respObj); err != nil {
			t.Fatalf("failed to parse non-streaming converted response: %v, raw: %s", err, rec.Body.String())
		}
		choices, ok := respObj["choices"].([]any)
		if !ok || len(choices) == 0 {
			t.Fatalf("expected choices array in non-streaming response, got: %v", respObj)
		}
	})

	t.Run("UpstreamErrorHandling", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded","code":400}}`))
		}))
		defer srv.Close()

		rec := httptest.NewRecorder()
		req := &Request{
			Ctx:      context.Background(),
			Client:   srv.Client(),
			Config:   &providers.ProviderConfig{BaseURL: srv.URL},
			APIKey:   "test-key",
			Body:     []byte(`{"model":"glm-5.3-flash-free","messages":[{"role":"user","content":"hi"}]}`),
			IsStream: true,
		}

		err := ForwardOpencode(rec, req)
		if err == nil {
			t.Fatal("expected error from upstream 400, got nil")
		}

		var upstreamErr *proxy.UpstreamError
		if !errors.As(err, &upstreamErr) {
			t.Fatalf("expected *proxy.UpstreamError, got %T: %v", err, err)
		}
		if upstreamErr.StatusCode != http.StatusBadRequest {
			t.Errorf("expected upstream error status 400, got %d", upstreamErr.StatusCode)
		}
	})
}
