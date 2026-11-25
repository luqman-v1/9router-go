package executor

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// --- helpers --------------------------------------------------------------

// freePiTestToken builds a JWT-shaped credential carrying `sub`, which is all
// the provider reads it for: a stable account identity, never a security check
// (upstream verifies the signature).
func freePiTestToken(sub string) string {
	payload, _ := json.Marshal(map[string]any{"sub": sub, "exp": time.Now().Add(24 * time.Hour).Unix()})
	enc := base64.RawURLEncoding.EncodeToString(payload)
	return "hdr." + enc + ".sig"
}

// freePiJSONCompletion is a minimal well-formed completion, matching the shape
// the passthrough validates before it will write a 200.
const freePiJSONCompletion = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"deepseek/deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`

// freePiSSEStream is one delta frame plus the terminator.
const freePiSSEStream = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"

// fakeLeaseStore is an in-memory LeaseStore. `holder` is what ReadLease reports;
// counting the mutating calls lets a test prove a reset did or did not touch the
// coordination layer.
type fakeLeaseStore struct {
	mu      sync.Mutex
	holder  string // value reported by ReadLease; "" means no live lease
	expires time.Time

	acquireCalls int
	releaseCalls int
	acquiredBy   []string
}

func (f *fakeLeaseStore) ReadLease(scope, key string) (*db.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.holder == "" {
		return nil, nil
	}
	exp := f.expires
	if exp.IsZero() {
		exp = time.Now().Add(time.Minute)
	}
	return &db.Lease{Scope: scope, Key: key, Value: f.holder, ExpiresAt: exp}, nil
}

func (f *fakeLeaseStore) AcquireLease(scope, key, value string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquireCalls++
	f.acquiredBy = append(f.acquiredBy, value)
	// A preset foreign holder means "a sibling is driving this account": the
	// acquire must lose. A zero expiry still counts as live here, because the
	// test is asserting ownership, not TTL arithmetic.
	if f.holder != "" && f.holder != value {
		return false, nil
	}
	f.holder = value
	f.expires = time.Now().Add(ttl)
	return true, nil
}

func (f *fakeLeaseStore) ReleaseLease(scope, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	if f.holder == value {
		f.holder = ""
	}
	return nil
}

func (f *fakeLeaseStore) snapshot() (holder string, acquire, release int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.holder, f.acquireCalls, f.releaseCalls
}

// freePiTestRequest builds the executor Request the handler would hand over.
func freePiTestRequest(t *testing.T, srv *httptest.Server, token string, connData map[string]any, stream bool) *Request {
	t.Helper()
	cfg := providers.KnownProviders[providers.FreePiProviderID]
	cfg.BaseURL = srv.URL + "/v1/chat/completions"
	body := `{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`
	if stream {
		body = `{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`
	}
	return &Request{
		Ctx: t.Context(),
		// srv.Client() dials the loopback test server directly; the shared
		// transport would refuse it as a private address.
		Client:    srv.Client(),
		Config:    &cfg,
		APIKey:    token,
		Body:      []byte(body),
		IsStream:  stream,
		ConnData:  connData,
		StartTime: time.Now(),
	}
}

// --- P2: transport contract ----------------------------------------------

func TestFreePiForward_SendsSessionHeadersAndLeavesBodyAlone(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, freePiJSONCompletion)
	}))
	defer srv.Close()

	token := freePiTestToken("user_abc")
	req := freePiTestRequest(t, srv, token, nil, false)
	rec := httptest.NewRecorder()

	if err := ForwardFreePi(rec, req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}

	if got := gotHeaders.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization = %q, want bearer token", got)
	}
	if got := gotHeaders.Get("x-session-id"); got == "" {
		t.Error("x-session-id missing: upstream ties every completion to it")
	}
	if got := gotHeaders.Get("x-client-version"); got != providers.FreePiClientVersion {
		t.Errorf("x-client-version = %q, want %q", got, providers.FreePiClientVersion)
	}
	if got := gotHeaders.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}

	// free-pi needs no body rewriting (no request-shape checks, no fingerprint
	// gate), so the only transformation allowed is the repo-wide canonical
	// re-serialisation every provider gets for prompt-cache determinism
	// (proxy.newJSONRequest -> canonicalChatBody). Assert the JSON value is
	// untouched: no field added, dropped, or coerced.
	var sent, want map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("sent body is not JSON: %v", err)
	}
	if err := json.Unmarshal(req.Body, &want); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if !reflect.DeepEqual(sent, want) {
		t.Errorf("body was rewritten:\n got %v\nwant %v", sent, want)
	}

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "chat.completion") {
		t.Errorf("client did not receive the completion: %s", rec.Body.String())
	}
}

func TestFreePiForward_StreamsSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q, want text/event-stream", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, freePiSSEStream)
	}))
	defer srv.Close()

	req := freePiTestRequest(t, srv, freePiTestToken("user_stream"), nil, true)
	rec := httptest.NewRecorder()

	if err := ForwardFreePi(rec, req); err != nil {
		t.Fatalf("ForwardFreePi: %v", err)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want an event stream", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("stream frame missing from passthrough: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("stream terminator missing: %s", body)
	}
}

// TestFreePiForward_DoesNotLaunderStatusInto200 is the reason this provider has
// its own executor at all. executor.Default() never reads resp.StatusCode and
// jsonResponse always writes 200, so a 409 would reach the client as a
// successful turn carrying an error body — and be recorded in usageHistory as
// served. Here the conflict must stay a conflict.
func TestFreePiForward_DoesNotLaunderStatusInto200(t *testing.T) {
	conflict := `{"code":"concurrent_session","message":"You've already got 4 free-pi CLI sessions running."}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, conflict)
	}))
	defer srv.Close()

	resetFreePiCooldownsForTest()

	// Auto-reset off, so the 409 is reported rather than repaired.
	req := freePiTestRequest(t, srv, freePiTestToken("user_conflict"),
		map[string]any{"freePiAutoReset": false}, false)
	rec := httptest.NewRecorder()

	err := ForwardFreePi(rec, req)
	if err == nil {
		t.Fatal("expected an upstream error, got nil (status was laundered into a 200)")
	}
	var ue *proxy.UpstreamError
	if !asUpstreamError(err, &ue) {
		t.Fatalf("error is %T, want *proxy.UpstreamError", err)
	}
	if ue.StatusCode != http.StatusConflict {
		t.Errorf("upstream status = %d, want 409", ue.StatusCode)
	}
	if !strings.Contains(string(ue.Body), providers.FreePiCodeConcurrentSession) {
		t.Errorf("body lost the upstream envelope: %s", ue.Body)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("nothing should have been written to the client, got %q", rec.Body.String())
	}
}

func TestFreePiForward_Non200IsAnUpstreamError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"message":"unauthorized"}}`},
		{name: "ad gate", status: http.StatusForbidden, body: `{"code":"ads_not_showing","message":"` + providers.FreePiAdGateMessage + `"}`},
		// Deliberately 500, not 502/503/504: those are retried inside
		// proxy.DoRequest (retryTransientUpstream, with backoff), which would
		// make this test spend nine seconds re-proving proxy's own behaviour.
		{name: "server error", status: http.StatusInternalServerError, body: `{"error":"upstream down"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// An unlabelled body is sniffed as text/plain, which the
				// shared transport reads as a proxy refusal and replays
				// direct. Real upstream always labels its JSON.
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			req := freePiTestRequest(t, srv, freePiTestToken("user_err"), nil, false)
			rec := httptest.NewRecorder()

			err := ForwardFreePi(rec, req)
			var ue *proxy.UpstreamError
			if !asUpstreamError(err, &ue) {
				t.Fatalf("error = %v (%T), want *proxy.UpstreamError", err, err)
			}
			if ue.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", ue.StatusCode, tt.status)
			}
			if rec.Body.Len() != 0 {
				t.Errorf("a failed upstream must not write a body, got %q", rec.Body.String())
			}
		})
	}
}

func TestFreePiForward_RequiresToken(t *testing.T) {
	cfg := providers.KnownProviders[providers.FreePiProviderID]
	req := &Request{Ctx: t.Context(), Client: http.DefaultClient, Config: &cfg, Body: []byte(`{}`)}
	if err := ForwardFreePi(httptest.NewRecorder(), req); err == nil {
		t.Fatal("expected an error for an empty credential")
	}
}
