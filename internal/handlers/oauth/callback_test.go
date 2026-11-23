package oauth

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleCallbackPage_ok(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("GET", "/callback?code=abc123&state=s1", nil)
	rec := httptest.NewRecorder()
	handler.HandleCallbackPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html, got %q", ct)
	}
	for _, want := range []string{"OAuth callback", `id="code"`, "Copy", "paste"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("callback page missing %q", want)
		}
	}
}

func TestHandleCallbackPage_noReflection(t *testing.T) {
	// Query values must never be reflected into the HTML (page is fully static;
	// extraction happens client-side via textContent).
	handler := NewOAuthHandler(nil)
	payload := "<script>alert(1)</script>"
	req := httptest.NewRequest("GET", "/callback?code="+payload, nil)
	rec := httptest.NewRecorder()
	handler.HandleCallbackPage(rec, req)
	if strings.Contains(rec.Body.String(), payload) {
		t.Errorf("callback page reflects query input (XSS)")
	}
}

func TestHandleCallbackPage_methodNotAllowed(t *testing.T) {
	handler := NewOAuthHandler(nil)
	req := httptest.NewRequest("POST", "/callback", nil)
	rec := httptest.NewRecorder()
	handler.HandleCallbackPage(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestCallbackRelay_RecordAndConsume(t *testing.T) {
	newIsolated := func() *OAuthHandler {
		h := NewOAuthHandler(nil)
		h.callbacks = newCallbackRelay()
		return h
	}

	t.Run("one-shot code relay across origins", func(t *testing.T) {
		h := newIsolated()

		cbReq := httptest.NewRequest(http.MethodGet, "http://localhost:20130/callback?code=abc123&state=s1", nil)
		cbRec := httptest.NewRecorder()
		h.HandleCallbackPage(cbRec, cbReq)
		if cbRec.Code != http.StatusOK {
			t.Fatalf("HandleCallbackPage status = %d, want 200", cbRec.Code)
		}
		if !strings.Contains(cbRec.Body.String(), "127.0.0.1") {
			t.Errorf("callbackPage missing loopback peer 127.0.0.1 postMessage handling")
		}

		pollReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:20130/api/oauth/callback-result?state=s1", nil)
		pollRec := httptest.NewRecorder()
		h.HandleCallbackResult(pollRec, pollReq)
		if pollRec.Code != http.StatusOK {
			t.Fatalf("poll status = %d, want 200: %s", pollRec.Code, pollRec.Body.String())
		}
		var res struct {
			Found     bool   `json:"found"`
			State     string `json:"state"`
			Raw       string `json:"raw"`
			Error     string `json:"error"`
			ErrorDesc string `json:"errorDesc"`
		}
		if err := json.Unmarshal(pollRec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode poll response: %v", err)
		}
		if !res.Found || res.State != "s1" || res.Raw != "abc123" || res.Error != "" {
			t.Fatalf("unexpected poll result: %+v", res)
		}

		// Second poll must return found=false (one-shot).
		pollRec2 := httptest.NewRecorder()
		h.HandleCallbackResult(pollRec2, httptest.NewRequest(http.MethodGet, "/api/oauth/callback-result?state=s1", nil))
		var res2 struct {
			Found bool `json:"found"`
		}
		if err := json.Unmarshal(pollRec2.Body.Bytes(), &res2); err != nil {
			t.Fatalf("decode second poll: %v", err)
		}
		if res2.Found {
			t.Fatal("second poll returned found=true, want one-shot removal")
		}
	})

	t.Run("error callback is relayed", func(t *testing.T) {
		h := newIsolated()
		cbReq := httptest.NewRequest(http.MethodGet, "/callback?error=access_denied&error_description=User+denied&state=s2", nil)
		cbRec := httptest.NewRecorder()
		h.HandleCallbackPage(cbRec, cbReq)

		pollRec := httptest.NewRecorder()
		h.HandleCallbackResult(pollRec, httptest.NewRequest(http.MethodGet, "/api/oauth/callback-result?state=s2", nil))
		var res struct {
			Found     bool   `json:"found"`
			Error     string `json:"error"`
			ErrorDesc string `json:"errorDesc"`
		}
		if err := json.Unmarshal(pollRec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !res.Found || res.Error != "access_denied" || res.ErrorDesc != "User denied" {
			t.Fatalf("unexpected error relay: %+v", res)
		}
	})

	t.Run("multi-param query preserves full raw query", func(t *testing.T) {
		h := newIsolated()
		cbReq := httptest.NewRequest(http.MethodGet, "/callback?refreshToken=tok&loginHost=api.trae.ai&state=s3", nil)
		h.HandleCallbackPage(httptest.NewRecorder(), cbReq)

		pollRec := httptest.NewRecorder()
		h.HandleCallbackResult(pollRec, httptest.NewRequest(http.MethodGet, "/api/oauth/callback-result?state=s3", nil))
		var res struct {
			Found bool   `json:"found"`
			Raw   string `json:"raw"`
		}
		if err := json.Unmarshal(pollRec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !res.Found || res.Raw != "refreshToken=tok&loginHost=api.trae.ai&state=s3" {
			t.Fatalf("unexpected multi-param relay: %+v", res)
		}
	})

	t.Run("missing state returns 400", func(t *testing.T) {
		h := newIsolated()
		rec := httptest.NewRecorder()
		h.HandleCallbackResult(rec, httptest.NewRequest(http.MethodGet, "/api/oauth/callback-result", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("expired entry returns found=false", func(t *testing.T) {
		h := newIsolated()
		h.callbacks.record("s-old", "code-old", "", "")
		h.callbacks.mu.Lock()
		entry := h.callbacks.entries["s-old"]
		entry.at = time.Now().Add(-callbackRelayTTL - time.Second)
		h.callbacks.entries["s-old"] = entry
		h.callbacks.mu.Unlock()

		pollRec := httptest.NewRecorder()
		h.HandleCallbackResult(pollRec, httptest.NewRequest(http.MethodGet, "/api/oauth/callback-result?state=s-old", nil))
		var res struct {
			Found bool `json:"found"`
		}
		if err := json.Unmarshal(pollRec.Body.Bytes(), &res); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if res.Found {
			t.Fatal("expired relay returned found=true, want false")
		}
	})

	t.Run("bounded eviction caps entries and rejects oversized code", func(t *testing.T) {
		h := newIsolated()
		for i := range callbackRelayMaxEntries + 10 {
			state := "s-" + string(rune('a'+(i%26))) + "-" + string(rune('0'+(i/26)))
			h.callbacks.record(state, "c", "", "")
		}
		h.callbacks.mu.Lock()
		count := len(h.callbacks.entries)
		h.callbacks.mu.Unlock()
		if count > callbackRelayMaxEntries {
			t.Fatalf("entries len = %d, want <= %d", count, callbackRelayMaxEntries)
		}

		bigCode := strings.Repeat("x", callbackRelayMaxValueLen+1)
		h.HandleCallbackPage(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/callback?code="+bigCode+"&state=s-big", nil))
		if _, ok := h.callbacks.take("s-big"); ok {
			t.Fatal("oversized callback code should not be stored in relay")
		}
	})
}
