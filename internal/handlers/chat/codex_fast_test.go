package chat

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestApplyCodexFastMode_ByteImmutability(t *testing.T) {
	origJSON := `{"model":"gpt-4o","service_tier":"fast"}`
	body := []byte(origJSON)
	bodyCopy := make([]byte, len(body))
	copy(bodyCopy, body)

	out, modified := ApplyCodexFastMode(body, "codex", nil)
	if !modified {
		t.Fatal("expected modified=true")
	}

	// Caller slice must not be mutated
	if !bytes.Equal(body, bodyCopy) {
		t.Fatalf("caller body was mutated in-place: got %s, want %s", string(body), string(bodyCopy))
	}

	// Mutating output slice must not affect caller body
	if len(out) > 0 {
		out[0] = 'X'
		if !bytes.Equal(body, bodyCopy) {
			t.Fatal("mutating returned slice corrupted original caller body")
		}
	}
}

func TestApplyCodexFastMode_PrecedenceLevel1_Fast(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","service_tier":"fast"}`)
	accFalse := false
	out, modified := ApplyCodexFastMode(body, "codex", &accFalse)
	if !modified {
		t.Fatal("expected modified=true")
	}
	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if res["service_tier"] != "priority" {
		t.Fatalf("service_tier = %v, want 'priority'", res["service_tier"])
	}
}

func TestApplyCodexFastMode_PrecedenceLevel1_Standard(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","service_tier":"standard"}`)
	accTrue := true
	out, modified := ApplyCodexFastMode(body, "codex", &accTrue)
	if modified {
		t.Fatal("expected modified=false, explicit standard should block account override")
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("got %s, want %s", string(out), string(body))
	}
}

func TestApplyCodexFastMode_PrecedenceLevel1_OtherValuesBlockAccount(t *testing.T) {
	accTrue := true
	testCases := []struct {
		name string
		json string
	}{
		{"flex", `{"model":"gpt-4o","service_tier":"flex"}`},
		{"null", `{"model":"gpt-4o","service_tier":null}`},
		{"numeric", `{"model":"gpt-4o","service_tier":123}`},
		{"already_priority", `{"model":"gpt-4o","service_tier":"priority"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(tc.json)
			out, modified := ApplyCodexFastMode(b, "codex", &accTrue)
			if modified {
				t.Fatalf("expected modified=false for service_tier %s, should block account default", tc.name)
			}
			if !bytes.Equal(out, b) {
				t.Fatalf("expected untouched body for service_tier %s", tc.name)
			}
		})
	}
}

// "speed" is a client hint, not a Codex service_tier. The executor's
// normalizeCodexServiceTier drops it and never promotes on it, and
// internal/integration/codex_service_tier_test.go pins the same expectation
// ("speed alone does not promote"), so promoting here would put a tier on the
// wire the caller never asked for.
func TestApplyCodexFastMode_PrecedenceLevel2_SpeedFastDoesNotPromote(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","speed":"fast"}`)
	accFalse := false
	out, modified := ApplyCodexFastMode(body, "codex", &accFalse)
	if modified {
		t.Fatalf("expected modified=false, speed alone must not create a service_tier: got %s", string(out))
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("got %s, want %s", string(out), string(body))
	}
}

func TestApplyCodexFastMode_PrecedenceLevel3_SpeedStandard(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","speed":"standard"}`)
	accTrue := true
	out, modified := ApplyCodexFastMode(body, "codex", &accTrue)
	if modified {
		t.Fatal("expected modified=false, speed standard should block account override")
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("got %s, want %s", string(out), string(body))
	}
}

func TestApplyCodexFastMode_PrecedenceLevel4_SpeedCustom(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","speed":"custom"}`)
	accTrue := true
	out, modified := ApplyCodexFastMode(body, "codex", &accTrue)
	if modified {
		t.Fatal("expected modified=false, speed custom should block account override")
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("got %s, want %s", string(out), string(body))
	}
}

func TestApplyCodexFastMode_PrecedenceLevel4_OtherSpeedValuesBlockAccount(t *testing.T) {
	accTrue := true
	testCases := []struct {
		name string
		json string
	}{
		{"null", `{"model":"gpt-4o","speed":null}`},
		{"numeric", `{"model":"gpt-4o","speed":456}`},
		{"empty", `{"model":"gpt-4o","speed":""}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(tc.json)
			out, modified := ApplyCodexFastMode(b, "codex", &accTrue)
			if modified {
				t.Fatalf("expected modified=false for speed %s, should block account default", tc.name)
			}
			if !bytes.Equal(out, b) {
				t.Fatalf("expected untouched body for speed %s", tc.name)
			}
		})
	}
}

func TestApplyCodexFastMode_PrecedenceLevel5_AccountDefaultTrue(t *testing.T) {
	body := []byte(`{"model":"gpt-4o"}`)
	accTrue := true
	out, modified := ApplyCodexFastMode(body, "codex", &accTrue)
	if !modified {
		t.Fatal("expected modified=true when no client intent and account fast mode is enabled")
	}
	var res map[string]any
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if res["service_tier"] != "priority" {
		t.Fatalf("service_tier = %v, want 'priority'", res["service_tier"])
	}
}

func TestApplyCodexFastMode_PrecedenceLevel5_AccountDefaultFalse(t *testing.T) {
	body := []byte(`{"model":"gpt-4o"}`)
	accFalse := false
	out, modified := ApplyCodexFastMode(body, "codex", &accFalse)
	if modified {
		t.Fatal("expected modified=false when account fast mode is false")
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("got %s, want %s", string(out), string(body))
	}

	outNil, modifiedNil := ApplyCodexFastMode(body, "codex", nil)
	if modifiedNil {
		t.Fatal("expected modified=false when account fast mode is nil")
	}
	if !bytes.Equal(outNil, body) {
		t.Fatalf("got %s, want %s", string(outNil), string(body))
	}
}

func TestApplyCodexFastMode_NonCodexProvider(t *testing.T) {
	accTrue := true
	testCases := []struct {
		name     string
		provider string
		body     string
	}{
		{"openai_fast_service_tier", "openai", `{"model":"gpt-4o","service_tier":"fast"}`},
		{"openai_speed_fast", "openai", `{"model":"gpt-4o","speed":"fast"}`},
		{"openai_acc_true", "openai", `{"model":"gpt-4o"}`},
		{"claude_fast_service_tier", "claude", `{"model":"claude-3-5-sonnet","service_tier":"fast"}`},
		{"claude_acc_true", "claude", `{"model":"claude-3-5-sonnet"}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			b := []byte(tc.body)
			out, modified := ApplyCodexFastMode(b, tc.provider, &accTrue)
			if modified {
				t.Fatalf("provider %s must not be modified by ApplyCodexFastMode", tc.provider)
			}
			if !bytes.Equal(out, b) {
				t.Fatalf("expected identical bytes for non-codex provider %s", tc.provider)
			}
		})
	}
}

func TestApplyCodexFastMode_AccountRetryIsolation(t *testing.T) {
	// Original request without client intent
	origReq := []byte(`{"messages":[{"content":"hi","role":"user"}],"model":"codex-model"}`)
	origCopy := slices.Clone(origReq)

	// Attempt 1: Account 1 with fast = false
	acc1False := false
	out1, mod1 := ApplyCodexFastMode(origReq, "codex", &acc1False)
	if mod1 {
		t.Fatal("attempt 1: expected mod1=false")
	}
	if !bytes.Equal(out1, origReq) {
		t.Fatal("attempt 1: out1 must match origReq")
	}
	if !bytes.Equal(origReq, origCopy) {
		t.Fatal("attempt 1 mutated origReq!")
	}

	// Attempt 2: Account 2 with fast = true
	acc2True := true
	out2, mod2 := ApplyCodexFastMode(origReq, "codex", &acc2True)
	if !mod2 {
		t.Fatal("attempt 2: expected mod2=true")
	}
	if bytes.Equal(out2, origReq) {
		t.Fatal("attempt 2: out2 should differ from origReq")
	}
	if !bytes.Equal(origReq, origCopy) {
		t.Fatal("attempt 2 mutated origReq!")
	}

	// Verify attempt 2 got priority
	var res2 map[string]any
	if err := json.Unmarshal(out2, &res2); err != nil {
		t.Fatal(err)
	}
	if res2["service_tier"] != "priority" {
		t.Fatalf("attempt 2: service_tier = %v, want 'priority'", res2["service_tier"])
	}

	// Attempt 3: Fallback to non-Codex (e.g. "openai")
	out3, mod3 := ApplyCodexFastMode(origReq, "openai", &acc2True)
	if mod3 {
		t.Fatal("attempt 3: non-codex fallback must not be modified")
	}
	if !bytes.Equal(out3, origReq) {
		t.Fatal("attempt 3: out3 must match pristine origReq")
	}
}

func TestApplyCodexFastMode_TryForwardWithConnectionIntegration(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"codex response\"}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	h, cleanup := setupHandlerForForward(t)
	defer cleanup()

	// 1. Account with codexFastMode: true and no client intent (using Responses API format so service_tier is preserved across buildResponsesBody)
	rec := httptest.NewRecorder()
	body := []byte(`{"input":[{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],"model":"codex-model"}`)
	bodyCopy := slices.Clone(body)

	err := h.tryForwardWithConnection(forwardRequestParams{
		Ctx:          context.Background(),
		W:            rec,
		Provider:     "codex",
		Model:        "codex-model",
		ConnectionID: "conn-fast",
		ConnData: &ConnectionData{
			APIKey:  "sk-test",
			BaseURL: srv.URL,
			ProviderSpecificData: map[string]any{
				"codexFastMode": true,
			},
		},
		Body:              body,
		IsStream:          false,
		TranslateResponse: false,
		Endpoint:          "/v1/chat/completions",
	})
	if err != nil {
		t.Fatalf("tryForwardWithConnection error: %v", err)
	}

	// Verify original body passed to tryForwardWithConnection was NOT mutated
	if !bytes.Equal(body, bodyCopy) {
		t.Fatalf("caller body was mutated by tryForwardWithConnection: got %s, want %s", string(body), string(bodyCopy))
	}

	// Verify upstream server received service_tier: priority
	var reqMap map[string]any
	if err := json.Unmarshal(capturedBody, &reqMap); err != nil {
		t.Fatalf("unmarshal capturedBody: %v", err)
	}
	if reqMap["service_tier"] != "priority" {
		t.Fatalf("expected service_tier 'priority' forwarded to upstream, got %v", reqMap["service_tier"])
	}
}
