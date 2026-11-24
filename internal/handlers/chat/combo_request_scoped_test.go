package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A combo whose every member refuses answers with the first refusal that named
// the request itself, not with the last member's failure and not with a
// synthesized 502/503. The 400 carries the parameter the client has to change;
// the second member's 500 or 404 names nothing actionable, so returning it
// hides the reason the turn failed.
//
// Upstream parity: decolua/9router@5cdcddf55, fixing upstream #5057.
func TestComboAllRefusedReturnsFirstRequestScopedError(t *testing.T) {
	cases := []struct {
		name        string
		secondCode  int
		secondBody  string
		endpoint    string
		wantCode    int
		wantMessage string
	}{
		{
			name:        "chat completions keeps the first 400 over a later 500",
			secondCode:  http.StatusInternalServerError,
			secondBody:  `{"error":{"message":"internal error","type":"server_error"}}`,
			endpoint:    "chat",
			wantCode:    http.StatusBadRequest,
			wantMessage: "custom param rejected",
		},
		{
			name:        "messages keeps the first 400 over a later 404",
			secondCode:  http.StatusNotFound,
			secondBody:  `{"error":{"message":"model not found","type":"invalid_request_error"}}`,
			endpoint:    "messages",
			wantCode:    http.StatusBadRequest,
			wantMessage: "custom param rejected",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var firstCalls, secondCalls int
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				firstCalls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"custom param rejected","type":"invalid_request_error","code":"bad_param"}}`))
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				secondCalls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.secondCode)
				_, _ = w.Write([]byte(tt.secondBody))
			}))
			defer second.Close()

			h := NewChatHandler(seedComboWithTwoUpstreams(t, "combo-refuse", first.URL, second.URL, "refuse-combo"))
			rec := httptest.NewRecorder()
			if tt.endpoint == "messages" {
				req := httptest.NewRequest(http.MethodPost, "/v1/messages",
					strings.NewReader(`{"model":"refuse-combo","messages":[{"role":"user","content":"hi"}],"max_tokens":64,"stream":false}`))
				h.HandleMessages(rec, req)
			} else {
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
					strings.NewReader(`{"model":"refuse-combo","messages":[{"role":"user","content":"hi"}],"stream":false}`))
				h.HandleChatCompletions(rec, req)
			}

			// Every member must be tried: another seat may accept the request.
			if firstCalls != 1 {
				t.Errorf("first member received %d requests, want 1", firstCalls)
			}
			if secondCalls != 1 {
				t.Errorf("second member received %d requests, want 1 — the 400 must advance the combo, not end it", secondCalls)
			}

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (the first member's 400, not the later %d); body: %s",
					rec.Code, tt.wantCode, tt.secondCode, rec.Body.String())
			}
			if body := rec.Body.String(); !strings.Contains(body, tt.wantMessage) {
				t.Errorf("body = %s, want it to carry the first member's message %q", body, tt.wantMessage)
			}
			// The second member's verdict must not leak into the answer.
			if strings.Contains(rec.Body.String(), tt.secondBody) {
				t.Errorf("body = %s, want the first member's body only", rec.Body.String())
			}
		})
	}
}

// A single request-scoped refusal still comes back as itself, with its own
// status and body, rather than being flattened into a 502 "all combo models
// failed". This is the shape the pass-through contract already promised for
// one-member combos; it must not change when the first-refusal rule exists.
func TestComboSingleRequestScopedRefusalPassesThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"message":"tools are not supported","type":"invalid_request_error"}}`))
	}))
	defer upstream.Close()

	h := NewChatHandler(seedComboWithTwoUpstreams(t, "combo-422", upstream.URL, upstream.URL, "single-422"))
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"single-422","messages":[{"role":"user","content":"hi"}],"stream":false}`)))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tools are not supported") {
		t.Errorf("body = %s, want the upstream body forwarded unchanged", rec.Body.String())
	}
}

// 403 is account-scoped: a Gemini/antigravity daily-quota refusal arrives as a
// 403 and the account's other connections may still have quota. It is in
// providers.RetryableStatusCodes, so the first-refusal rule must not touch it:
// the combo keeps rotating, and because the 403 is not request-scoped it does
// not outrank the later member's 500 either — the last failure stands, exactly
// as it did before this change.
func TestComboAccountScoped403KeepsRotatingConnections(t *testing.T) {
	var secondCalls int
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exceeded for this key","type":"permission_error"}}`))
	}))
	defer forbidden.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"internal error","type":"server_error"}}`))
	}))
	defer broken.Close()

	h := NewChatHandler(seedComboWithTwoUpstreams(t, "combo-403", forbidden.URL, broken.URL, "account-403"))
	rec := httptest.NewRecorder()
	h.HandleChatCompletions(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"account-403","messages":[{"role":"user","content":"hi"}],"stream":false}`)))

	if secondCalls != 1 {
		t.Errorf("second member received %d requests, want 1: a 403 is account-scoped, so the combo must move on", secondCalls)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (the 403 is not request-scoped, so it must not outrank the later member); body: %s",
			rec.Code, rec.Body.String())
	}
}

// TestFreePiConcurrentSessionIsNotRequestScoped is the failover decision: a
// free-pi 409 `concurrent_session` says the ACCOUNT is at its session cap, not
// that the request is wrong, so the next account must be tried with the request
// unchanged. Classifying it as request-scoped ended the pass and handed the
// conflict to the client — which is why a second free-pi account never picked up
// a mid-conversation switch.
func TestFreePiConcurrentSessionIsNotRequestScoped(t *testing.T) {
	concurrent := &upstreamError{
		StatusCode: http.StatusConflict,
		Body:       []byte(`{"code":"concurrent_session","message":"You've already got 4 free-pi CLI sessions running."}`),
	}
	if isRequestScopedComboError(concurrent) {
		t.Error("concurrent_session was treated as request-scoped; the next account is never tried")
	}
	if !isRetryableComboError(concurrent) {
		t.Error("concurrent_session was treated as non-retryable; failover cannot run")
	}

	// A different 409 stays request-scoped: an ordinary conflict is about the
	// request, and only this provider's session code means "another account can
	// serve this unchanged".
	other := &upstreamError{
		StatusCode: http.StatusConflict,
		Body:       []byte(`{"code":"some_other_conflict"}`),
	}
	if !isRequestScopedComboError(other) {
		t.Error("an unrelated 409 became retryable; failover would mask a real conflict")
	}
}
