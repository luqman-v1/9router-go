package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"9router/proxy/internal/providers"
)

func TestIsLongDecodeModel_Gemini4Family(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"gemini-4-carbon", true},
		{"gemini-4-argon", true},
		{"gemini-4-neon", true},
		{"gemini-4-hydrogen", true},
		{"gemini-1.5-pro", false},
		{"gemini-2.5-flash", false},
	}
	for _, c := range cases {
		if got := isLongDecodeModel(c.model); got != c.want {
			t.Errorf("isLongDecodeModel(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}

func TestFollowLongDecodeContinuation_ChainsHops(t *testing.T) {
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var gemResp map[string]any
		if n == 1 {
			gemResp = map[string]any{
				"candidates": []map[string]any{
					{"finishReason": "CONTINUATION"},
				},
				"continuationToken": "tok-abc",
			}
		} else {
			gemResp = map[string]any{
				"candidates": []map[string]any{
					{
						"content": map[string]any{
							"parts": []map[string]any{{"text": "final answer"}},
						},
						"finishReason": "STOP",
					},
				},
			}
		}
		wrapped := map[string]any{"response": gemResp}
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(wrapped)
		_, _ = w.Write(b)
	}))
	defer upstream.Close()

	cfg := &providers.ProviderConfig{BaseURL: upstream.URL}
	client := &http.Client{}

	initialInner := map[string]any{
		"candidates":        []map[string]any{{"finishReason": "CONTINUATION"}},
		"continuationToken": "tok-abc",
	}
	initialWrapped := map[string]any{"response": initialInner}
	initialBytes, _ := json.Marshal(initialWrapped)
	initialResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(initialBytes)),
	}

	h := &ChatHandler{}
	body := []byte(`{"model":"gemini-4-hydrogen","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := h.followLongDecodeContinuation(context.Background(), client, cfg, "key", body, "gemini-4-hydrogen", "proj-1", initialResp, false, nil)
	if err != nil {
		t.Fatalf("followLongDecodeContinuation: %v", err)
	}
	if resp == nil || resp.Body == nil {
		t.Fatalf("nil response")
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(raw), "final answer") {
		t.Errorf("expected final answer in response, got %s", string(raw))
	}
	if calls != 2 {
		t.Errorf("expected 2 upstream calls (chained hop), got %d", calls)
	}
}
