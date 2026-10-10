package executor

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/proxy"
)

// bedrockWire is one captured request to the fake Bedrock upstream.
type bedrockWire struct {
	path        string
	authz       string
	amzDate     string
	accept      string
	contentType string
	token       string
	body        string
}

// newBedrockUpstream starts a fake Bedrock runtime and records what the gateway sent.
func newBedrockUpstream(t *testing.T, reply func(w http.ResponseWriter)) (*httptest.Server, *bedrockWire) {
	t.Helper()
	wire := &bedrockWire{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		wire.path = r.URL.Path
		wire.authz = r.Header.Get("Authorization")
		wire.amzDate = r.Header.Get("x-amz-date")
		wire.accept = r.Header.Get("Accept")
		wire.contentType = r.Header.Get("Content-Type")
		wire.token = r.Header.Get("x-amz-security-token")
		wire.body = string(raw)
		reply(w)
	}))
	t.Cleanup(srv.Close)
	return srv, wire
}

// bedrockChunks builds an EventStream body from raw Anthropic / Chat Completions events.
func bedrockChunks(events ...string) []byte {
	var body []byte
	for _, event := range events {
		encoded := base64.StdEncoding.EncodeToString([]byte(event))
		payload, _ := json.Marshal(map[string]string{"bytes": encoded})
		body = append(body, providers.EncodeEventFrame(map[string]string{
			":event-type":   providers.BedrockChunkEvent,
			":message-type": "event",
		}, payload)...)
	}
	return body
}

func bedrockStatic(psd map[string]any, region string) map[string]any {
	if psd == nil {
		psd = map[string]any{}
	}
	psd["accessKeyId"] = "AKIAIOSFODNN7EXAMPLE"
	if region != "" {
		psd["region"] = region
	}
	return psd
}

// bedrockCfg points a connection at the fake upstream the way a custom node would, so
// the test never touches the real Bedrock runtime.
func bedrockCfg(srv *httptest.Server, format string) *providers.ProviderConfig {
	return &providers.ProviderConfig{BaseURL: srv.URL, Format: format}
}

// A full Anthropic stream must reach an OpenAI client as Chat Completions chunks, with
// the model id escaped once in the path and the signature present. The Bedrock framing is
// unwrapped into ordinary Claude SSE first, then translated by the existing Claude path —
// so what this pins is that the unwrap loses nothing and the terminal event is tracked.
func TestForwardBedrock_ClaudeWireStream(t *testing.T) {
	srv, wire := newBedrockUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		w.Write(bedrockChunks(
			`{"type":"message_start","message":{"id":"msg_1","model":"us.anthropic.claude-sonnet-5"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
			`{"type":"message_stop"}`,
		))
	})

	cfg := bedrockCfg(srv, "claude")
	rec := httptest.NewRecorder()
	body := []byte(`{"model":"br/us.anthropic.claude-sonnet-5","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	err := ForwardBedrock(rec, &Request{
		Ctx: t.Context(), Client: srv.Client(), Config: cfg,
		APIKey: "wJalrXUtnFEMI-secret", Body: body, IsStream: true,
		// The Anthropic entry is the Claude wire: the handler sets this because the
		// upstream speaks Messages while the client asked /v1/chat/completions.
		UpstreamClaude: true,
		ConnData:       bedrockStatic(nil, "us-east-1"),
	})
	if err != nil {
		t.Fatalf("ForwardBedrock() error = %v", err)
	}

	if got, want := rec.Header().Get("Content-Type"), "text/event-stream"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	out := rec.Body.String()
	payloads := ssePayloads(t, out)
	if len(payloads) == 0 {
		t.Fatalf("stream carried no data frames:\n%s", out)
	}
	var text strings.Builder
	sawStop := false
	for _, payload := range payloads {
		if payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("decode SSE payload %q: %v", payload, err)
		}
		for _, choice := range chunk.Choices {
			text.WriteString(choice.Delta.Content)
			sawStop = sawStop || choice.FinishReason != nil
		}
	}
	if got := text.String(); got != "Hello" {
		t.Errorf("streamed text = %q, want %q — a Bedrock chunk was lost or mistranslated", got, "Hello")
	}
	if !sawStop {
		t.Errorf("stream never reported a finish_reason, so a client waits forever:\n%s", out)
	}
	if last := payloads[len(payloads)-1]; last != "[DONE]" {
		t.Errorf("last SSE frame = %q, want the [DONE] sentinel", last)
	}
	// A well-formed stream ended on message_stop, so no truncation error is appended.
	if strings.Contains(out, "api_error") {
		t.Errorf("SSE body reports an error on a well-formed stream:\n%s", out)
	}

	// The model id carries a ":0" suffix on most Bedrock models; it must be escaped
	// exactly once in the path the transport dials.
	if !strings.Contains(wire.path, "/model/us.anthropic.claude-sonnet-5/invoke-with-response-stream") {
		t.Errorf("upstream path = %q, want the streaming invoke path", wire.path)
	}
	if wire.accept != "application/vnd.amazon.eventstream" {
		t.Errorf("Accept = %q, want the Bedrock event-stream media type", wire.accept)
	}
	if !strings.HasPrefix(wire.authz, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/") {
		t.Errorf("Authorization = %q, want a SigV4 credential scope for the connection's key id", wire.authz)
	}
	if wire.amzDate == "" {
		t.Error("request carries no x-amz-date, so it cannot have been signed")
	}
	if strings.Contains(wire.body, `"model"`) {
		t.Errorf("request body still carries model, which Bedrock rejects: %s", wire.body)
	}
	if !strings.Contains(wire.body, `"anthropic_version":"`+providers.BedrockAnthropicVersion+`"`) {
		t.Errorf("request body missing the Bedrock anthropic_version pin: %s", wire.body)
	}
}

func TestForwardBedrock_SignsAndSendsTheSessionToken(t *testing.T) {
	srv, wire := newBedrockUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		w.Write(bedrockChunks(`{"type":"message_stop"}`))
	})

	psd := bedrockStatic(nil, "eu-west-1")
	psd["sessionToken"] = "FwoGZXIvYXdzEExample"
	rec := httptest.NewRecorder()
	err := ForwardBedrock(rec, &Request{
		Ctx: t.Context(), Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL, Format: "claude"},
		APIKey: "secret", Body: []byte(`{"model":"br/us.anthropic.claude-opus-5"}`),
		IsStream: true, ConnData: psd,
	})
	if err != nil {
		t.Fatalf("ForwardBedrock() error = %v", err)
	}
	if wire.token != "FwoGZXIvYXdzEExample" {
		t.Errorf("x-amz-security-token = %q, want the connection's session token", wire.token)
	}
	// A token that is sent but not signed is the classic cause of SignatureDoesNotMatch
	// on temporary credentials.
	if !strings.Contains(wire.authz, "x-amz-security-token") {
		t.Errorf("Authorization = %q, want the session token in SignedHeaders", wire.authz)
	}
}

// xAI Grok speaks Chat Completions on Bedrock, so its chunks are bare `data:` lines with
// no `event:` name and completion comes from finish_reason rather than a terminal event.
func TestForwardBedrock_OpenAIWireStream(t *testing.T) {
	srv, _ := newBedrockUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		w.Write(bedrockChunks(
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		))
	})

	rec := httptest.NewRecorder()
	err := ForwardBedrock(rec, &Request{
		Ctx: t.Context(), Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL},
		APIKey: "secret", Body: []byte(`{"model":"brx/us.xai.grok-4.6","messages":[]}`),
		IsStream: true, ConnData: bedrockStatic(nil, "us-west-2"),
	})
	if err != nil {
		t.Fatalf("ForwardBedrock() error = %v", err)
	}

	out := rec.Body.String()
	if !strings.Contains(out, `"content":"Hi"`) {
		t.Errorf("SSE body missing the Grok delta:\n%s", out)
	}
	// An `event:` line there would be junk the OpenAI parsers have to skip.
	if strings.Contains(out, "event: ") {
		t.Errorf("OpenAI-wire SSE must not carry event names:\n%s", out)
	}
	if strings.Contains(out, "api_error") {
		t.Errorf("SSE body reports an error after a finish_reason:\n%s", out)
	}
}

// The non-streaming endpoint returns one complete Anthropic body with no framing to
// unwrap, so it passes straight through.
func TestForwardBedrock_NonStreamPassesThrough(t *testing.T) {
	srv, wire := newBedrockUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_1","type":"message","content":[{"type":"text","text":"hi there"}]}`))
	})

	rec := httptest.NewRecorder()
	err := ForwardBedrock(rec, &Request{
		Ctx: t.Context(), Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL, Format: "claude"},
		APIKey: "secret", Body: []byte(`{"model":"br/us.anthropic.claude-sonnet-5","messages":[]}`),
		ConnData: bedrockStatic(nil, "us-east-1"),
	})
	if err != nil {
		t.Fatalf("ForwardBedrock() error = %v", err)
	}
	if !strings.HasSuffix(wire.path, "/invoke") {
		t.Errorf("upstream path = %q, want the non-streaming invoke path", wire.path)
	}
	if !strings.Contains(rec.Body.String(), `"text":"hi there"`) {
		t.Errorf("response body = %q, want the upstream's message body", rec.Body.String())
	}
}

// A model from the other family would return chunks this executor cannot read, and
// discovering that after the call is billed is worse than an upfront refusal.
func TestForwardBedrock_RejectsTheWrongModelFamily(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		cfg      *providers.ProviderConfig
		model    string
	}{
		{
			name:     "xai model on the anthropic entry",
			provider: "bedrock",
			cfg:      &providers.ProviderConfig{Format: "claude"},
			model:    "br/us.xai.grok-4.6",
		},
		{
			name:     "anthropic model on the xai entry",
			provider: "bedrock-xai",
			cfg:      &providers.ProviderConfig{},
			model:    "brx/us.anthropic.claude-sonnet-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, wire := newBedrockUpstream(t, func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusOK)
			})

			rec := httptest.NewRecorder()
			err := ForwardBedrock(rec, &Request{
				Ctx: t.Context(), Client: srv.Client(), Config: tt.cfg,
				APIKey: "secret", Body: []byte(`{"model":"` + tt.model + `"}`), IsStream: true,
				ConnData: bedrockStatic(nil, "us-east-1"),
			})
			if err == nil {
				t.Fatalf("ForwardBedrock() succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), "is not supported by the") {
				t.Errorf("error = %v, want it to name the expected model family", err)
			}
			if wire.path != "" {
				t.Errorf("upstream was called (%q); the model must be refused before the call is billed", wire.path)
			}
		})
	}
}

// Bedrock reports throttling as an in-band frame, not an HTTP status, so an exception
// frame has to become a terminal SSE error rather than a clean end of stream.
func TestForwardBedrock_InBandExceptionBecomesAStreamError(t *testing.T) {
	srv, _ := newBedrockUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		payload, _ := json.Marshal(map[string]string{"message": "Too many requests"})
		w.Write(providers.EncodeEventFrame(map[string]string{
			":event-type":     "exception",
			":message-type":   "exception",
			":exception-type": "ThrottlingException",
		}, payload))
	})

	rec := httptest.NewRecorder()
	// No UpstreamClaude: the stream Bedrock emits is fed through the standard OpenAI
	// path, whose in-band error check turns a mid-stream error object into a failed turn
	// instead of a clean stop.
	err := ForwardBedrock(rec, &Request{
		Ctx: t.Context(), Client: srv.Client(),
		Config: bedrockCfg(srv, "claude"),
		APIKey: "secret", Body: []byte(`{"model":"br/us.anthropic.claude-sonnet-5"}`),
		IsStream: true, ConnData: bedrockStatic(nil, "us-east-1"),
	})
	if err == nil {
		t.Fatal("ForwardBedrock() succeeded, want the in-band throttling to fail the turn")
	}
	// The router retries a 502, which is what lets the next account take over.
	var upstreamErr *proxy.UpstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("error = %v, want a 502 UpstreamError the fallback layer can retry on", err)
	}
	if !strings.Contains(string(upstreamErr.Body), "ThrottlingException") ||
		!strings.Contains(string(upstreamErr.Body), "Too many requests") {
		t.Errorf("error body = %q, want the upstream's exception type and message", upstreamErr.Body)
	}
}

// An upstream that hangs up cleanly mid-answer looks like a complete response unless the
// terminal event is tracked.
func TestForwardBedrock_TruncatedStreamIsReported(t *testing.T) {
	srv, _ := newBedrockUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		w.Write(bedrockChunks(`{"type":"content_block_delta","delta":{"text":"half"}}`))
	})

	rec := httptest.NewRecorder()
	err := ForwardBedrock(rec, &Request{
		Ctx: t.Context(), Client: srv.Client(),
		Config: bedrockCfg(srv, "claude"),
		APIKey: "secret", Body: []byte(`{"model":"br/us.anthropic.claude-sonnet-5"}`),
		IsStream: true, ConnData: bedrockStatic(nil, "us-east-1"),
	})
	// Presenting a truncated answer as finished is the failure this check exists for,
	// so the turn fails rather than closing with [DONE].
	var upstreamErr *proxy.UpstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("error = %v, want a 502 UpstreamError instead of a silently finished stream", err)
	}
	if !strings.Contains(string(upstreamErr.Body), "ended before any choice reported a finish_reason") {
		t.Errorf("error body = %q, want the truncation reason", upstreamErr.Body)
	}
	if out := rec.Body.String(); !strings.Contains(out, "half") {
		t.Errorf("SSE body dropped the chunk that did arrive: %q", out)
	}
}

// A region is interpolated into the Bedrock hostname, so an unvalidated one must be
// refused before a signed request is sent to it.
func TestForwardBedrock_RejectsAnUnvalidatedRegion(t *testing.T) {
	srv, wire := newBedrockUpstream(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	err := ForwardBedrock(rec, &Request{
		Ctx: t.Context(), Client: srv.Client(),
		Config: &providers.ProviderConfig{BaseURL: srv.URL, Format: "claude"},
		APIKey: "secret", Body: []byte(`{"model":"br/us.anthropic.claude-sonnet-5"}`),
		IsStream: true, ConnData: bedrockStatic(nil, "evil.com/x"),
	})
	if err == nil {
		t.Fatal("ForwardBedrock() succeeded, want a region rejection")
	}
	if !strings.Contains(err.Error(), "invalid region") {
		t.Errorf("error = %v, want a region validation failure", err)
	}
	if wire.path != "" {
		t.Error("a request was sent despite the invalid region")
	}
}

// ssePayloads extracts the payload of every "data:" frame in an SSE body.
func ssePayloads(t *testing.T, body string) []string {
	t.Helper()
	var payloads []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payloads = append(payloads, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	return payloads
}

func TestBedrockProviderID(t *testing.T) {
	tests := []struct {
		name string
		cfg  *providers.ProviderConfig
		want string
	}{
		{name: "claude wire", cfg: &providers.ProviderConfig{Format: "claude"}, want: "bedrock"},
		{name: "openai wire", cfg: &providers.ProviderConfig{}, want: "bedrock-xai"},
		{name: "nil config", cfg: nil, want: "bedrock-xai"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BedrockProviderID(tt.cfg); got != tt.want {
				t.Errorf("BedrockProviderID() = %q, want %q", got, tt.want)
			}
		})
	}
}
