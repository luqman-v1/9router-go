package guardrails

import (
	"bytes"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubStore serves fixed policies per scope, so the middleware can be tested
// without a database.
type stubStore struct {
	byScope map[string][]policy
}

func (s stubStore) ListGuardrailPolicies(scope, scopeID string) ([]policy, error) {
	return s.byScope[scope+":"+scopeID], nil
}

func jsonStore(cfg string) stubStore {
	return stubStore{byScope: map[string][]policy{
		"global:": {{Config: cfg}},
	}}
}

// runMiddleware pushes a body through the tap and reports what the next
// handler saw.
func runMiddleware(t *testing.T, store PolicyStore, body string) (status int, seen string, audits int) {
	t.Helper()

	var seenBody string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("downstream read: %v", err)
		}
		seenBody = string(b)
		w.WriteHeader(http.StatusOK)
	})

	handler := Inbound(store, func(Decision, Target) { audits++ }, nil)(next)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, seenBody, audits
}

func chatBody(content string) string {
	return `{"model":"claude-sonnet","messages":[{"role":"user","content":"` + content + `"}]}`
}

// TestInboundIsNoOpWithoutPolicy is the backward-compatibility guarantee: an
// install that never configured guardrails must pass traffic through byte for
// byte.
func TestInboundIsNoOpWithoutPolicy(t *testing.T) {
	empty := stubStore{byScope: map[string][]policy{}}
	body := chatBody("my email is bob@corp.io and card 4111111111111111")

	status, seen, audits := runMiddleware(t, empty, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if seen != body {
		t.Errorf("body was rewritten with no policy configured:\n got %s\nwant %s", seen, body)
	}
	if audits != 0 {
		t.Errorf("audited %d decisions with no policy configured", audits)
	}
}

// TestInboundLogOnlyPassesUnchanged proves log_only observes without touching
// the request — the safe default an operator enables first.
func TestInboundLogOnlyPassesUnchanged(t *testing.T) {
	store := jsonStore(`{"detectors":["pii"],"action":"log_only"}`)
	body := chatBody("my email is bob@corp.io")

	status, seen, audits := runMiddleware(t, store, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if seen != body {
		t.Error("log_only rewrote the body; it must only observe")
	}
	if audits != 1 {
		t.Errorf("audits = %d, want 1", audits)
	}
}

// TestInboundBlockStopsTheRequest proves a blocked request never reaches the
// provider-facing handler at all.
func TestInboundBlockStopsTheRequest(t *testing.T) {
	store := jsonStore(`{"detectors":["pii"],"action":"block"}`)
	status, seen, audits := runMiddleware(t, store, chatBody("my email is bob@corp.io"))

	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if seen != "" {
		t.Errorf("the downstream handler ran with body %q; a blocked request must not reach it", seen)
	}
	if audits != 1 {
		t.Errorf("audits = %d, want 1", audits)
	}
}

// TestInboundMaskRewritesBeforeDispatch is the core mask contract: the handler
// downstream must receive the redacted text, so the secret never leaves the
// gateway.
func TestInboundMaskRewritesBeforeDispatch(t *testing.T) {
	store := jsonStore(`{"detectors":["pii"],"action":"mask"}`)
	status, seen, _ := runMiddleware(t, store, chatBody("my email is bob@corp.io"))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.Contains(seen, "bob@corp.io") {
		t.Errorf("mask left the address in the dispatched body: %s", seen)
	}
	if !strings.Contains(seen, "[REDACTED]") {
		t.Errorf("mask did not redact anything: %s", seen)
	}
	// The rewritten body must still be valid JSON carrying the same model,
	// or the provider request breaks.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(seen), &decoded); err != nil {
		t.Fatalf("rewritten body is not valid JSON: %v", err)
	}
	if decoded["model"] != "claude-sonnet" {
		t.Errorf("rewritten body lost the model: %v", decoded)
	}
}

// TestInboundSkipsNonJSONBody proves a body the scanner cannot walk is passed
// through untouched rather than rejected — the guardrails tap must not become
// the thing that breaks multipart uploads or proxy relays.
func TestInboundSkipsNonJSONBody(t *testing.T) {
	store := jsonStore(`{"detectors":["pii"],"action":"block"}`)
	const body = "not json at all, just bytes"

	status, seen, audits := runMiddleware(t, store, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a non-JSON body", status)
	}
	if seen != body {
		t.Errorf("non-JSON body was altered: %q", seen)
	}
	if audits != 0 {
		t.Errorf("audited %d decisions for a non-scannable body", audits)
	}
}

// TestInboundRestoresReadableBody proves the handler downstream gets a working
// body stream, not one already drained by the scanner.
func TestInboundRestoresReadableBody(t *testing.T) {
	store := jsonStore(`{"detectors":["pii"],"action":"mask"}`)

	var reads int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("downstream read: %v", err)
		}
		reads++
		w.WriteHeader(http.StatusOK)
	})
	handler := Inbound(store, nil, nil)(next)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader([]byte(chatBody("mail bob@corp.io"))))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if reads != 1 {
		t.Fatalf("downstream handler ran %d times, want 1", reads)
	}
}
// TestInboundKillSwitchStopsEnforcement is the property an operator needs when
// a false positive starts blocking real traffic: turning the switch off must
// take effect without deleting the policy, which would take the audit trail
// with it.
func TestInboundKillSwitchStopsEnforcement(t *testing.T) {
	store := jsonStore(`{"detectors":["pii"],"action":"block"}`)
	body := chatBody("my email is bob@corp.io")

	status, seen, audits := runMiddlewareWithSwitch(t, store, body, func() bool { return false })
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200: a disabled switch must not block", status)
	}
	if seen != body {
		t.Errorf("body was rewritten while disabled:\n got %s\nwant %s", seen, body)
	}
	if audits != 0 {
		t.Errorf("recorded %d decisions while disabled", audits)
	}
}

// TestInboundSwitchOnByDefault guards the other direction. A policy row is an
// explicit operator decision, so a settings row that was never written must not
// read as "off" and silently discard it.
func TestInboundSwitchOnByDefault(t *testing.T) {
	store := jsonStore(`{"detectors":["pii"],"action":"block"}`)

	status, _, audits := runMiddlewareWithSwitch(t, store, chatBody("my email is bob@corp.io"), nil)
	if status == http.StatusOK {
		t.Error("a nil switch must mean enabled; a configured policy cannot be ignored by omission")
	}
	if audits != 1 {
		t.Errorf("audits = %d, want 1", audits)
	}
}

// runMiddlewareWithSwitch is runMiddleware with the global kill-switch attached.
func runMiddlewareWithSwitch(t *testing.T, store PolicyStore, body string, sw Switch) (status int, seen string, audits int) {
	t.Helper()

	var seenBody string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("downstream read: %v", err)
		}
		seenBody = string(b)
		w.WriteHeader(http.StatusOK)
	})

	handler := Inbound(store, func(Decision, Target) { audits++ }, sw)(next)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, seenBody, audits
}

// TestSwitchDefaultsOn covers the Switch contract itself: a nil switch is on,
// and a reading error never disables enforcement.
func TestSwitchDefaultsOn(t *testing.T) {
	if !Switch(nil).Enabled() {
		t.Error("a nil switch must read as enabled")
	}
	if !Switch(func() bool { return true }).Enabled() {
		t.Error("a switch returning true must read as enabled")
	}
	if Switch(func() bool { return false }).Enabled() {
		t.Error("a switch returning false must read as disabled")
	}
}
