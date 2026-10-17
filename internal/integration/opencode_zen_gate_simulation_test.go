//go:build integration

package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/pricing"
	"9router/proxy/internal/providers"
)

// End-to-end simulation of the OpenCode free-tier gate answering HTTP 200 for
// big-pickle.
//
// big-pickle is free but carries no "-free" suffix, so nothing about the id
// reveals its tier — the curated catalog does. That makes it the single most
// useful case to pin: it is the one free id where suffix matching would send
// keyed traffic down the paid /zen/v1 lane and get a 403.
//
// The fake upstream implements the gate both in its headers and in its body,
// using the rules recorded for the /inference gate (upstream decolua/9router
// #4842, probed 2026-09-18), and answers 403 FreeTierError when the outbound
// request violates them. Driving it through the production router exercises the
// whole pipeline: lane choice, credential swap, header fingerprinting, and body
// shaping.
//
// Header rules encoded by inferenceGate:
//   - User-Agent must match opencode/<channel>/<major.minor.patch>/<client>
//     with major >= 2;
//   - x-opencode-session, x-opencode-session-id, x-session-affinity and
//     x-session-id must all carry the same value;
//   - x-opencode-request must be absent (rejected with 403 since 2026-10-14);
//   - Authorization must be the keyless "Bearer public", not a user key.
//
// Body rules encoded by inferenceGate:
//   - streaming is mandatory (stream:false answers 403);
//   - the exact "read" tool must be declared with the verbatim CLI 2.x
//     description and parameter schema — a stub is refused here, unlike the
//     legacy /zen/v1 lane;
//   - two tools sharing a name answer 500.

const (
	inferenceFreeTierError = `{"error":{"type":"FreeTierError","message":"free tier can only be used from within OpenCode"}}`
	inferenceDuplicate     = `{"error":{"type":"server_error","message":"duplicate tool names"}}`
)

// inferenceUARegex is the 2.x client shape the inference gate requires.
var inferenceUARegex = regexp.MustCompile(`^opencode/[a-z0-9-]+/\d+\.\d+\.\d+/[a-z0-9-]+$`)

// inferenceGate is a fake /inference/v1 endpoint that only answers 200 to a
// request the real gate would accept. It records why it refused so a failure
// names the rule that was broken.
type inferenceGate struct {
	refusals []string
}

func (g *inferenceGate) refuse(reason string) {
	g.refusals = append(g.refusals, reason)
}

// status validates one request against the gate contract, records the first
// rule it violates, and reports the status the real gate would answer.
func (g *inferenceGate) status(r *http.Request, body []byte) int {
	ua := r.Header.Get("User-Agent")
	if !inferenceUARegex.MatchString(ua) || !hasMajorAtLeastTwo(ua) {
		g.refuse("User-Agent " + ua + " is not the OpenCode 2.x CLI shape (403)")
		return http.StatusForbidden
	}

	session := r.Header.Get("x-opencode-session")
	if session == "" {
		g.refuse("x-opencode-session missing (403)")
		return http.StatusForbidden
	}
	for _, mirror := range []string{"x-opencode-session-id", "x-session-affinity", "x-session-id"} {
		if got := r.Header.Get(mirror); got != session {
			g.refuse(mirror + " = " + got + ", want the same value as x-opencode-session " + session + " (403)")
			return http.StatusForbidden
		}
	}

	if got := r.Header.Get("x-opencode-request"); got != "" {
		g.refuse("x-opencode-request must not be sent on the inference gate, got " + got + " (403)")
		return http.StatusForbidden
	}

	if got := r.Header.Get("Authorization"); got != "Bearer public" {
		g.refuse("Authorization = " + got + ", want the keyless Bearer public credential (403)")
		return http.StatusForbidden
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		g.refuse("body is not JSON: " + err.Error())
		return http.StatusBadRequest
	}

	if stream, _ := payload["stream"].(bool); !stream {
		g.refuse("stream is not true — the gate rejects non-streaming free requests (403)")
		return http.StatusForbidden
	}

	rawTools, _ := payload["tools"].([]any)
	if len(rawTools) == 0 {
		g.refuse("no tools declared — the gate rejects tool-less requests (403)")
		return http.StatusForbidden
	}

	seen := make(map[string]bool, len(rawTools))
	var read map[string]any
	for _, tool := range rawTools {
		m, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		name := gateToolName(m)
		if name == "" {
			continue
		}
		if seen[name] {
			g.refuse("duplicate tool name " + name + " — collapsing did not run (500)")
			return http.StatusInternalServerError
		}
		seen[name] = true
		if name == "read" {
			read = m
		}
	}
	if read == nil {
		g.refuse(`no tool named exactly "read" (403)`)
		return http.StatusForbidden
	}
	if reason, ok := g.validateReadDefinition(read); !ok {
		g.refuse(reason)
		return http.StatusForbidden
	}
	return http.StatusOK
}

// validateReadDefinition enforces the part of the gate the legacy lane ignores:
// the read tool must carry the genuine CLI 2.x definition, not a stub.
//
// A Chat-lane tool nests everything under "function"; a flat Responses or
// Messages tool keeps it at the top level. The gate checks whichever shape it
// was handed, exactly as the real one does.
func (g *inferenceGate) validateReadDefinition(read map[string]any) (string, bool) {
	definition := read
	if fn, ok := read["function"].(map[string]any); ok {
		definition = fn
	}
	description, _ := definition["description"].(string)
	if !strings.HasPrefix(description, "Read the contents of a file or directory.") {
		return "read description is not the verbatim CLI 2.x text, got " + description + " (403)", false
	}
	// strict:false is only asserted on the flat Responses lane; the Chat lane
	// does not carry it.
	if _, flat := read["name"]; flat && read["strict"] != false {
		return "flat read tool must carry strict:false like upstream (403)", false
	}
	params, _ := definition["parameters"].(map[string]any)
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["path"]; !ok {
		return "read parameters must require path (403)", false
	}
	return "", true
}

// hasMajorAtLeastTwo reports whether the UA claims an OpenCode 2.x-or-later
// client. The gate rejects a v1 identity outright.
func hasMajorAtLeastTwo(ua string) bool {
	parts := strings.Split(ua, "/")
	if len(parts) < 3 {
		return false
	}
	major, _, _ := strings.Cut(parts[2], ".")
	return major == "2" || (len(major) > 1 && major[0] >= '3')
}

// gateToolName reads a tool name from either declaration shape.
func gateToolName(m map[string]any) string {
	if fn, ok := m["function"].(map[string]any); ok {
		if n, ok := fn["name"].(string); ok {
			return n
		}
	}
	if n, ok := m["name"].(string); ok {
		return n
	}
	return ""
}

// respond validates one request and answers 200 only when every rule holds.
func (g *inferenceGate) respond(w http.ResponseWriter, r *http.Request, body []byte) {
	switch g.status(r, body) {
	case http.StatusOK:
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(inferenceChatStream))
	case http.StatusInternalServerError:
		writeGateRefusal(w, http.StatusInternalServerError, inferenceDuplicate)
	case http.StatusBadRequest:
		writeGateRefusal(w, http.StatusBadRequest, `{"error":{"message":"invalid JSON"}}`)
	default:
		writeGateRefusal(w, http.StatusForbidden, inferenceFreeTierError)
	}
}

func writeGateRefusal(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// inferenceChatStream is a minimal complete SSE completion.
const inferenceChatStream = "data: {\"id\":\"chatcmpl-zen\",\"object\":\"chat.completion.chunk\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-zen\",\"object\":\"chat.completion.chunk\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"big-pickle-200\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"chatcmpl-zen\",\"object\":\"chat.completion.chunk\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
	"data: [DONE]\n\n"

// gateRequest is one request the fake gate validated.
type gateRequest struct {
	path   string
	header http.Header
	body   []byte
}

// gateRequests is the gate's request log, returned by pointer because a slice
// header returned by value would not observe later appends.
type gateRequests struct {
	mu   sync.Mutex
	list []*gateRequest
}

// last returns the most recent request the fake gate saw.
func (l *gateRequests) last(t *testing.T) *gateRequest {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.list) == 0 {
		t.Fatal("the fake gate received no requests")
	}
	return l.list[len(l.list)-1]
}

// newInferenceGateUpstream starts a fake provider whose handler is the gate.
//
// It deliberately does not use env.NewUpstream: that helper drains r.Body into
// its own record before invoking the handler, which would leave the gate
// nothing to inspect. Here the gate owns the body read, so it validates exactly
// the bytes that arrived on the wire.
func newInferenceGateUpstream(t *testing.T, env *Env, gate *inferenceGate) (*Upstream, *gateRequests) {
	t.Helper()
	var log gateRequests
	up := &Upstream{}
	up.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.mu.Lock()
		log.list = append(log.list, &gateRequest{path: r.URL.Path, header: r.Clone(r.Context()).Header, body: body})
		log.mu.Unlock()
		gate.respond(w, r, body)
	}))
	t.Cleanup(up.Close)
	_ = env
	return up, &log
}

func gateToolNames(t *testing.T, body []byte) []string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	raw, _ := payload["tools"].([]any)
	names := make([]string, 0, len(raw))
	for _, tool := range raw {
		if m, ok := tool.(map[string]any); ok {
			names = append(names, gateToolName(m))
		}
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// claudeCodeToolset is the harness shape: capitalised built-in tools that the
// paid lane would rename and the free lane must replace with the exact CLI
// fingerprint.
func claudeCodeToolset() []map[string]any {
	return []map[string]any{
		{"type": "function", "function": map[string]any{"name": "Bash", "parameters": map[string]any{"type": "object"}}},
		{"type": "function", "function": map[string]any{"name": "Read", "parameters": map[string]any{"type": "object"}}},
		{"type": "function", "function": map[string]any{"name": "Edit", "parameters": map[string]any{"type": "object"}}},
	}
}

func bigPickleBody(tools []map[string]any) map[string]any {
	// The client names the id with its provider prefix, as a real caller does.
	body := ChatBody("ocz/big-pickle", true)
	if tools != nil {
		body["tools"] = tools
	}
	return body
}

// TestBigPickle_IsFreeTier pins the classification the rest of this file rests
// on. big-pickle has no "-free" suffix, so if it ever stopped being treated as
// free the lane would move to /zen/v1 and these simulations would silently stop
// covering what they claim to.
func TestBigPickle_IsFreeTier(t *testing.T) {
	if !pricing.IsFreeModel("big-pickle") {
		t.Error("pricing must bill big-pickle at zero")
	}
	// Only the providers that actually offer the id classify it. big-pickle is
	// catalogued on opencode-zen alone, so it is rightly not a free model of
	// the opencode or opencode-go lanes — the tier is a property of the id, the
	// lane a property of the catalog.
	if !providers.IsOpencodeFreeTierModel("opencode-zen", "big-pickle") {
		t.Error("opencode-zen must classify big-pickle as free-tier")
	}
	for _, provider := range []string{"opencode", "opencode-go"} {
		if providers.IsOpencodeFreeTierModel(provider, "big-pickle") {
			t.Errorf("%s does not offer big-pickle, so it must not classify it", provider)
		}
	}
}

// TestBigPickle_GateAnswers200 is the headline case: a keyed opencode-zen
// connection asking for big-pickle must be forwarded to the keyless inference
// gate, shaped, and answered 200 — not sent to /zen/v1 where the free id 403s.
func TestBigPickle_GateAnswers200(t *testing.T) {
	env := newEnv(t)
	gate := &inferenceGate{}
	up, reqs := newInferenceGateUpstream(t, env, gate)
	addZenConnection(t, env, up)

	res := env.Post(t, "/v1/chat/completions", bigPickleBody(claudeCodeToolset()))
	if res.Status != http.StatusOK {
		t.Fatalf("gate answered %d, want 200; refusals: %v; body: %s",
			res.Status, gate.refusals, truncate(res.Body))
	}
	if len(gate.refusals) != 0 {
		t.Errorf("gate recorded a refusal although the client got 200: %v", gate.refusals)
	}

	sent := reqs.last(t)
	if sent.path != "/inference/v1/chat/completions" {
		t.Errorf("upstream path = %q, want /inference/v1/chat/completions", sent.path)
	}
	if got := sent.header.Get("Authorization"); got != "Bearer public" {
		t.Errorf("Authorization = %q, want the keyless public credential", got)
	}
	names := gateToolNames(t, sent.body)
	if !containsName(names, "read") || !containsName(names, "shell") {
		t.Errorf(`free-lane body must declare the exact "read" and "shell" tools, got %v`, names)
	}
	if !strings.Contains(string(res.Body), "big-pickle-200") {
		t.Errorf("client must see the upstream text, got %s", truncate(res.Body))
	}
}

// A tool-less request must still be shaped into a body the gate accepts.
func TestBigPickle_ToolLessAnswers200(t *testing.T) {
	env := newEnv(t)
	gate := &inferenceGate{}
	up, reqs := newInferenceGateUpstream(t, env, gate)
	addZenConnection(t, env, up)

	res := env.Post(t, "/v1/chat/completions", bigPickleBody(nil))
	if res.Status != http.StatusOK {
		t.Fatalf("gate answered %d, want 200; refusals: %v; body: %s",
			res.Status, gate.refusals, truncate(res.Body))
	}
	names := gateToolNames(t, reqs.last(t).body)
	if !containsName(names, "read") {
		t.Errorf(`shaped body must declare "read", got %v`, names)
	}
}

// The negative control: the same gate fed the raw client body. This proves the
// 200s above are earned by shaping and lane choice, not by a lenient fake.
func TestBigPickle_RawBodyIsRefused(t *testing.T) {
	raw, err := json.Marshal(bigPickleBody(claudeCodeToolset()))
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}

	gate := &inferenceGate{}
	req, err := http.NewRequest(http.MethodPost, "http://gate.invalid/inference/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	// A plausible but non-compliant client: v1 UA, no session mirrors.
	req.Header.Set("User-Agent", "opencode/1.18.31")
	if got := gate.status(req, raw); got != http.StatusForbidden {
		t.Errorf("raw client request got %d, want 403", got)
	}
	if len(gate.refusals) == 0 {
		t.Error("the gate should have recorded why it refused")
	}

	// The same body with compliant headers still fails on the tool contract.
	gate = &inferenceGate{}
	req.Header.Set("User-Agent", "opencode/latest/2.0.29/cli")
	session := "ses_000000000000AAAAAAAAAAAAAA"
	for _, h := range []string{"x-opencode-session", "x-opencode-session-id", "x-session-affinity", "x-session-id"} {
		req.Header.Set(h, session)
	}
	req.Header.Set("Authorization", "Bearer public")
	if got := gate.status(req, raw); got != http.StatusForbidden {
		t.Errorf("raw capitalised body got %d, want 403", got)
	}
	if len(gate.refusals) == 0 {
		t.Error("the gate should have refused the unshaped tool set")
	}
}
