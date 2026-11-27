package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/constants"
)

func mimoBody(t *testing.T, model, effort string) []byte {
	t.Helper()
	body := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	if effort != "" {
		body["reasoning_effort"] = effort
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func mimoEffort(t *testing.T, body []byte) string {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, ok := out["reasoning_effort"].(string); ok {
		return v
	}
	return ""
}

func TestInjectMimoMarker_ClampsEffort(t *testing.T) {
	tests := []struct {
		name  string
		model string
		in    string
		want  string
	}{
		{
			// mimo-v2.5-pro and v2.6 answer 400 to "max" on this lane; v2.5
			// accepts it, so the clamp must follow the declared levels and not
			// the model family.
			name:  "v2.5-pro downgrades max",
			model: "mimo-v2.5-pro",
			in:    "max",
			want:  "high",
		},
		{
			name:  "v2.6 downgrades xhigh",
			model: "mimo-v2.6-flash",
			in:    "xhigh",
			want:  "high",
		},
		{
			name:  "v2.5 keeps max",
			model: "mimo-v2.5-free",
			in:    "max",
			want:  "max",
		},
		{
			name:  "high is untouched",
			model: "mimo-v2.5-pro",
			in:    "high",
			want:  "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := injectMimoMarker(mimoBody(t, tt.model, tt.in))
			if got := mimoEffort(t, out); got != tt.want {
				t.Errorf("reasoning_effort = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInjectMimoMarker_NoEffortIsLeftAlone(t *testing.T) {
	out := injectMimoMarker(mimoBody(t, "mimo-v2.5-pro", ""))

	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["reasoning_effort"]; ok {
		t.Error("reasoning_effort must not be invented for a request that had none")
	}
}

// The marker injection must survive the refactor that added the effort clamp:
// the anti-abuse system message is what keeps the free endpoint from 403ing.
func TestInjectMimoMarker_StillInjectsMarker(t *testing.T) {
	out := injectMimoMarker(mimoBody(t, "mimo-v2.5-pro", "max"))

	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %v, want the marker prepended", decoded["messages"])
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" || first["content"] != mimoSystemMarker {
		t.Errorf("first message = %v, want the anti-abuse system marker", first)
	}
}

// TestClampMimoFreeReasoning pins the mandatory-reasoning clamp on the
// mimo-free lane. The lane's endpoints are hardcoded (mimoBootstrapURL,
// mimoChatURL), so it cannot be faked end to end; the clamp itself is the
// part that decides what the upstream receives.
func TestClampMimoFreeReasoning(t *testing.T) {
	out := clampMimoFreeReasoning(mimoBody(t, "mimo-v2.6", "none"), "mimo-v2.6")
	if got := mimoEffort(t, out); got != "minimal" {
		t.Errorf("reasoning_effort = %q, want minimal (the free lane forbids disabling)", got)
	}
	out = clampMimoFreeReasoning(mimoBody(t, "mimo-v2.6", "high"), "mimo-v2.6")
	if got := mimoEffort(t, out); got != "high" {
		t.Errorf("reasoning_effort = %q, want high (another level is untouched)", got)
	}
	out = clampMimoFreeReasoning(mimoBody(t, "mimo-v2.6", ""), "mimo-v2.6")
	if got := mimoEffort(t, out); got != "" {
		t.Errorf("reasoning_effort = %q, want empty (no ask stays no ask)", got)
	}
}

type mimoCountingBody struct {
	io.Reader
	n int64
}

func (c *mimoCountingBody) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *mimoCountingBody) Close() error { return nil }

func seedMimoJWTForTest(t *testing.T, jwt string, exp time.Time) {
	t.Helper()
	mimoJWTMu.Lock()
	origJWT, origExp := mimoJWT, mimoJWTExp
	mimoJWT = jwt
	mimoJWTExp = exp
	mimoJWTMu.Unlock()
	t.Cleanup(func() {
		mimoJWTMu.Lock()
		mimoJWT = origJWT
		mimoJWTExp = origExp
		mimoJWTMu.Unlock()
	})
}

func blockDefaultHTTPClientForTest(t *testing.T) {
	t.Helper()
	origDefault := http.DefaultClient
	http.DefaultClient = &http.Client{
		Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return nil, errors.New("unexpected http.DefaultClient call")
		}),
	}
	t.Cleanup(func() { http.DefaultClient = origDefault })
}

func TestMimoFreeChat_ClientRetryAndCaps(t *testing.T) {
	t.Run("uses_handler_client_for_bootstrap_and_rewinds_body_on_401_retry", func(t *testing.T) {
		seedMimoJWTForTest(t, "stale-jwt", time.Now().Add(time.Hour))
		blockDefaultHTTPClientForTest(t)

		var chatAttempts atomic.Int32
		var bootstrapCalls atomic.Int32

		client := &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/api/free-ai/bootstrap":
					bootstrapCalls.Add(1)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"jwt":"fresh-jwt"}`)),
					}, nil
				case "/api/free-ai/openai/chat":
					attempt := chatAttempts.Add(1)
					bodyBytes, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatalf("attempt %d read body: %v", attempt, err)
					}
					if attempt == 1 {
						if len(bodyBytes) == 0 {
							t.Fatal("attempt 1 received empty body")
						}
						return &http.Response{
							StatusCode: http.StatusUnauthorized,
							Header:     http.Header{"Content-Type": []string{"application/json"}},
							Body:       io.NopCloser(strings.NewReader(`{"error":"expired"}`)),
						}, nil
					}
					if len(bodyBytes) == 0 {
						return &http.Response{
							StatusCode: http.StatusBadRequest,
							Header:     http.Header{"Content-Type": []string{"application/json"}},
							Body:       io.NopCloser(strings.NewReader(`{"error":"empty retry body"}`)),
						}, nil
					}
					if !strings.Contains(string(bodyBytes), mimoSystemMarker) {
						t.Fatalf("attempt %d missing system marker: %s", attempt, string(bodyBytes))
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)),
					}, nil
				default:
					return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody}, nil
				}
			}),
		}

		h := &ChatHandler{Client: client}
		rec := httptest.NewRecorder()
		if err := h.MimoFreeChat(context.Background(), rec, mimoBody(t, "mimo-v2.5-free", ""), false, nil); err != nil {
			t.Fatalf("MimoFreeChat failed: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
		}
		if chatAttempts.Load() != 2 || bootstrapCalls.Load() != 1 {
			t.Fatalf("attempts=%d bootstraps=%d, want 2 and 1", chatAttempts.Load(), bootstrapCalls.Load())
		}
	})

	t.Run("nil_client_falls_back_without_panic", func(t *testing.T) {
		seedMimoJWTForTest(t, "tok", time.Now().Add(time.Hour))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("MimoFreeChat panicked on nil Client: %v", r)
				}
			}()
			err := (&ChatHandler{Client: nil}).MimoFreeChat(ctx, httptest.NewRecorder(), mimoBody(t, "mimo-v2.5-free", ""), false, nil)
			if err == nil {
				t.Fatal("expected canceled context error, got nil")
			}
		}()
	})

	t.Run("caps_oversized_error_and_bootstrap_bodies", func(t *testing.T) {
		blockDefaultHTTPClientForTest(t)

		seedMimoJWTForTest(t, "", time.Time{})
		bootstrapBody := &mimoCountingBody{Reader: bytes.NewReader(bytes.Repeat([]byte("a"), 2<<20))}
		client := &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       bootstrapBody,
				}, nil
			}),
		}
		h := &ChatHandler{Client: client}
		_ = h.MimoFreeChat(context.Background(), httptest.NewRecorder(), mimoBody(t, "mimo-v2.5-free", ""), false, nil)
		if bootstrapBody.n == 0 || bootstrapBody.n > 1<<20 {
			t.Fatalf("bootstrap body read %d bytes, want in (0, %d]", bootstrapBody.n, 1<<20)
		}

		seedMimoJWTForTest(t, "valid-jwt", time.Now().Add(time.Hour))
		errBody := &mimoCountingBody{Reader: bytes.NewReader(bytes.Repeat([]byte("e"), int(constants.MaxUpstreamBodyBytes)+(1<<20)))}
		client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       errBody,
			}, nil
		})
		_ = h.MimoFreeChat(context.Background(), httptest.NewRecorder(), mimoBody(t, "mimo-v2.5-free", ""), false, nil)
		if errBody.n == 0 || errBody.n > constants.MaxUpstreamBodyBytes {
			t.Fatalf("error body read %d bytes, want in (0, %d]", errBody.n, constants.MaxUpstreamBodyBytes)
		}
	})
}
