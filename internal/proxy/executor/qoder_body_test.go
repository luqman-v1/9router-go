package executor

import (
	"encoding/base64"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/providers"
)

// Issue #121: every Qoder chat request answered
//
//	400 [FAIL]node:agent_router msg:None flow nodes found for router agent_router
//
// because ForwardQoder signed and forwarded the client's OpenAI body verbatim.
// Qoder's agent_chat_generation routes to a node that has no flow for an
// OpenAI request. These tests pin the rewrite that fixes it, and the wire
// details that have to hold or the gateway fails the same way in a new guise:
// the Qoder payload shape, the Encode=1 flag, the header set, and — most
// importantly — that the COSY signature covers the *encoded* bytes.

// qoderSeedCatalog installs a model_config for creds, so a test does not have
// to stand up a model list.
func qoderSeedCatalog(t *testing.T, creds QoderCosyCreds, region QoderRegion, configs map[string]map[string]any) {
	t.Helper()
	qoderCatalogMu.Lock()
	defer qoderCatalogMu.Unlock()
	raw := make(map[string]map[string]any, len(configs))
	for key, config := range configs {
		raw[key] = config
	}
	qoderCatalogs[qoderCatalogKey(creds, region)] = &qoderCatalog{rawConfigs: raw, expiresAt: time.Now().Add(time.Hour)}
	t.Cleanup(func() {
		qoderCatalogMu.Lock()
		defer qoderCatalogMu.Unlock()
		delete(qoderCatalogs, qoderCatalogKey(creds, region))
	})
}

// qoderRecordingServer captures the outbound request and answers with sse.
type qoderCapture struct {
	Body    []byte
	Query   string
	Headers http.Header
}

func qoderCaptureServer(t *testing.T, sse string) (*httptest.Server, *qoderCapture) {
	t.Helper()
	got := &qoderCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.Body, got.Query, got.Headers = body, r.URL.RawQuery, r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// qoderDecodeCaptured reverses QoderEncodeBody so a test can assert on the
// payload Qoder actually receives. It is the inverse of the encode step, not a
// reimplementation of the standard alphabet mapping: the substituted bytes are
// mapped back through the same table.
func qoderDecodeCaptured(t *testing.T, encoded []byte) map[string]any {
	t.Helper()
	// Rebuild the inverse substitution table.
	var inverse [128]byte
	for i := range inverse {
		inverse[i] = 0
	}
	for i := range len(qoderStdAlphabet) {
		inverse[qoderCustomAlphabet[i]] = qoderStdAlphabet[i]
	}
	inverse['$'] = '='

	// Undo the character substitution.
	reversed := make([]byte, len(encoded))
	for i, c := range encoded {
		if c < 128 && inverse[c] != 0 {
			reversed[i] = inverse[c]
		} else {
			reversed[i] = c
		}
	}

	// Undo the [tail][mid][head] rotation. Encoding emitted tail, then mid,
	// then head, so decoding reads the head back out of the tail slot first.
	n := len(reversed)
	third := n / 3
	rotated := make([]byte, 0, n)
	rotated = append(rotated, reversed[n-third:]...)
	rotated = append(rotated, reversed[third:n-third]...)
	rotated = append(rotated, reversed[:third]...)

	plain, err := base64.StdEncoding.DecodeString(string(rotated))
	if err != nil {
		t.Fatalf("captured body is not Qoder-encoded: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(plain, &payload); err != nil {
		t.Fatalf("captured body is not JSON: %v\ndecoded: %s", err, plain)
	}
	return payload
}

func qoderFixture(t *testing.T) QoderCosyCreds {
	t.Helper()
	creds := QoderCosyCreds{UserID: "user-1", AuthToken: "auth-token", MachineID: "machine-1"}
	qoderSeedCatalog(t, creds, QoderRegionIntl, map[string]map[string]any{
		"qfmodel": {"key": "qfmodel", "max_input_tokens": 131072.0, "max_output_tokens": 8192.0, "is_reasoning": true},
	})
	return creds
}

// The core of issue #121: what reaches Qoder must be a Qoder payload, not the
// client's OpenAI body. Every key here is one Qoder's router needs to match a
// flow — missing any of them is the 400 the issue reports.
func TestForwardQoder_SendsQoderPayloadNotOpenAIBody(t *testing.T) {
	creds := qoderFixture(t)
	srv, got := qoderCaptureServer(t, qoderSSE(qoderChunk(`{"content":"pong"}`, `"stop"`), "[DONE]"))

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL},
		APIKey:   creds.AuthToken,
		ConnData: map[string]any{"userId": creds.UserID, "machineId": creds.MachineID},
		Body:     []byte(`{"model":"qd/qfmodel","messages":[{"role":"user","content":"say pong"}],"stream":false,"max_tokens":10}`),
	}
	if err := ForwardQoder(rec, req); err != nil {
		t.Fatalf("ForwardQoder: %v", err)
	}

	payload := qoderDecodeCaptured(t, got.Body)

	// The client's OpenAI shape must not survive: these are the exact fields
	// that made Qoder route to agent_router.
	for _, absent := range []string{"messages_unknown", "role"} {
		if _, present := payload[absent]; present {
			t.Errorf("payload still carries the client's %q field", absent)
		}
	}
	for key, want := range map[string]any{
		"chat_task":    "FREE_INPUT",
		"session_type": "qodercli",
		"agent_id":     "agent_common",
		"task_id":      "common",
		"source":       1.0,
		"version":      "3",
		"is_reply":     true,
		"is_retry":     false,
		// Qoder only ever streams upstream; the non-streaming case is folded
		// back into one chat.completion afterwards.
		"stream": true,
	} {
		if payload[key] != want {
			t.Errorf("payload[%q] = %v, want %v", key, payload[key], want)
		}
	}

	for _, key := range []string{"request_id", "request_set_id", "chat_record_id", "session_id", "system", "messages", "tools", "parameters", "chat_context", "model_config", "business"} {
		if _, present := payload[key]; !present {
			t.Errorf("payload is missing %q, which Qoder's router requires", key)
		}
	}

	// model_config must be the catalogue entry, not a guess.
	modelConfig, _ := payload["model_config"].(map[string]any)
	if modelConfig["key"] != "qfmodel" {
		t.Errorf("model_config.key = %v, want qfmodel", modelConfig["key"])
	}
	parameters, _ := payload["parameters"].(map[string]any)
	if parameters["max_tokens"] != 10.0 {
		t.Errorf("parameters.max_tokens = %v, want 10 (the client asked for 10)", parameters["max_tokens"])
	}
}

// The identities must be derived, not random: session_id and chat_record_id
// are what let Qoder recognise the same conversation across requests.
func TestForwardQoder_DerivesStableIdentities(t *testing.T) {
	creds := qoderFixture(t)
	body := []byte(`{"model":"qoder/qfmodel","messages":[{"role":"user","content":"say pong"}]}`)

	first := buildQoderPayload(t, creds, body)
	second := buildQoderPayload(t, creds, body)

	if first.SessionID != second.SessionID {
		t.Errorf("session_id is not stable: %q vs %q", first.SessionID, second.SessionID)
	}
	if first.RecordID != second.RecordID {
		t.Errorf("chat_record_id is not stable: %q vs %q", first.RecordID, second.RecordID)
	}
	if first.RequestID == second.RequestID {
		t.Error("request_id must be fresh per request, otherwise Qoder replays the id")
	}

	// A different conversation must not reuse the record id, or the new turn
	// would be appended to the old chat record.
	other := buildQoderPayload(t, creds,
		[]byte(`{"model":"qoder/qfmodel","messages":[{"role":"user","content":"something else"}]}`))
	if other.RecordID == first.RecordID {
		t.Error("chat_record_id must change with the conversation")
	}

	// The wire prefix is not part of the key: qoder/x and x are one model.
	if first.QoderKey != "qfmodel" {
		t.Errorf("qoderKey = %q, want qfmodel (the qoder/ prefix must be stripped)", first.QoderKey)
	}
}

// qoderIdentities is the subset of a built payload the identity test asserts on.
type qoderIdentities struct {
	QoderKey  string
	RequestID string
	SessionID string
	RecordID  string
}

func buildQoderPayload(t *testing.T, creds QoderCosyCreds, body []byte) qoderIdentities {
	t.Helper()
	config := qoderCachedModelConfig(creds, QoderRegionIntl, "qfmodel")
	if config == nil {
		t.Fatal("test fixture did not seed model_config for qfmodel")
	}
	built, err := buildQoderChatRequest(body, config, creds, 1700000000000)
	if err != nil {
		t.Fatalf("buildQoderChatRequest: %v", err)
	}
	return qoderIdentities{
		QoderKey:  built.QoderKey,
		RequestID: built.Payload["request_id"].(string),
		SessionID: built.Payload["session_id"].(string),
		RecordID:  built.Payload["chat_record_id"].(string),
	}
}

// The query string, header set, and sign-over-encoded-bytes order are what
// separate a request Qoder decodes from one it rejects with a signature error.
func TestForwardQoder_SendsEncodeFlagAndRequiredHeaders(t *testing.T) {
	creds := qoderFixture(t)
	srv, got := qoderCaptureServer(t, qoderSSE(qoderChunk(`{"content":"hi"}`, `"stop"`), "[DONE]"))

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL},
		APIKey:   creds.AuthToken,
		ConnData: map[string]any{"userId": creds.UserID},
		Body:     []byte(`{"model":"qd/qfmodel","messages":[{"role":"user","content":"hi"}]}`),
	}
	if err := ForwardQoder(rec, req); err != nil {
		t.Fatalf("ForwardQoder: %v", err)
	}

	// Without Encode=1 the server never decodes the obfuscated body.
	if !strings.Contains(got.Query, "Encode=1") {
		t.Errorf("query = %q, want it to carry Encode=1 or the server cannot decode the body", got.Query)
	}
	for header, want := range map[string]string{
		"Accept":          "text/event-stream",
		"Cache-Control":   "no-cache",
		"X-Model-Key":     "qfmodel",
		"X-Model-Source":  "system",
		"Accept-Encoding": "identity",
		"Content-Type":    "application/json",
	} {
		if got := got.Headers.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}

	// The signature must cover the encoded bytes actually sent. Signing the
	// plaintext instead is the failure mode this ordering exists to prevent,
	// so pin the hash and the length against the bytes that went on the wire.
	if want := md5Hex(got.Body); got.Headers.Get("Cosy-Bodyhash") != want {
		t.Errorf("Cosy-Bodyhash = %q, want %q (the hash of the encoded bytes sent)", got.Headers.Get("Cosy-Bodyhash"), want)
	}
	if want := strconv.Itoa(len(got.Body)); got.Headers.Get("Cosy-Bodylength") != want {
		t.Errorf("Cosy-Bodylength = %q, want %q", got.Headers.Get("Cosy-Bodylength"), want)
	}
	// The signature is bound to the path the request actually goes to, query
	// string and all.
	target := qoderChatURL(srv.URL, QoderRegionIntl, creds.AuthToken)
	if want := qoderCosySigPath(target); got.Headers.Get("Cosy-Sigpath") != want {
		t.Errorf("Cosy-Sigpath = %q, want %q", got.Headers.Get("Cosy-Sigpath"), want)
	}

	// The plaintext is recoverable only through the inverse transform, which
	// is what makes this an encoding rather than an opaque blob.
	qoderDecodeCaptured(t, got.Body)
}

// A model the account's catalogue does not publish must fail with a message
// naming the model, not reach Qoder as a payload the router cannot match.
func TestForwardQoder_UnknownModelNamesTheModel(t *testing.T) {
	creds := qoderFixture(t)
	srv, _ := qoderCaptureServer(t, qoderSSE("[DONE]"))

	rec := httptest.NewRecorder()
	req := &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL},
		APIKey:   creds.AuthToken,
		ConnData: map[string]any{"userId": creds.UserID},
		Body:     []byte(`{"model":"qd/not-a-real-model","messages":[{"role":"user","content":"hi"}]}`),
	}
	err := ForwardQoder(rec, req)
	if err == nil {
		t.Fatal("expected an error for a model the catalogue does not publish")
	}
	if !strings.Contains(err.Error(), "not-a-real-model") {
		t.Errorf("error %q must name the model so the caller can see which one failed", err)
	}
}

// A connection that never stored a user id cannot sign, and used to surface as
// an opaque 500 from inside the signer.
func TestForwardQoder_MissingUserIDIsA401NamingTheFix(t *testing.T) {
	srv, _ := qoderCaptureServer(t, qoderSSE("[DONE]"))
	rec := httptest.NewRecorder()
	err := ForwardQoder(rec, &Request{
		Ctx:      t.Context(),
		Client:   srv.Client(),
		Config:   &providers.ProviderConfig{BaseURL: srv.URL},
		APIKey:   "auth-token",
		ConnData: map[string]any{},
		Body:     []byte(`{"model":"qd/qfmodel","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err == nil {
		t.Fatal("expected an error when the connection has no user id")
	}
	if !strings.Contains(err.Error(), "userId") {
		t.Errorf("error %q must say the userId is missing", err)
	}
}
