//go:build integration

package integration

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	json "encoding/json/v2"

	"9router/proxy/internal/providers"
)

// bedrockEventStream builds the AWS EventStream body Bedrock's
// invoke-with-response-stream returns: one frame per Anthropic event, each
// payload base64-encoded under "bytes".
func bedrockEventStream(t *testing.T, events ...string) []byte {
	t.Helper()
	var body []byte
	for _, event := range events {
		payload, err := json.Marshal(map[string]string{
			"bytes": base64.StdEncoding.EncodeToString([]byte(event)),
		})
		if err != nil {
			t.Fatalf("encode bedrock chunk payload: %v", err)
		}
		body = append(body, providers.EncodeEventFrame(map[string]string{
			":event-type":   providers.BedrockChunkEvent,
			":message-type": "event",
		}, payload)...)
	}
	return body
}

// addBedrockConnection stores a Bedrock connection pointed at the fake upstream.
// The gateway dials the connection's own baseUrl, which is what keeps this
// offline; it signs the request exactly as it would for the real runtime.
func addBedrockConnection(t *testing.T, env *Env, up *Upstream) {
	t.Helper()
	const id = "conn-bedrock"
	data := `{"apiKey":"wJalrXUtnFEMI-secret","baseUrl":"` + up.URL + `",` +
		`"providerSpecificData":{"accessKeyId":"AKIAIOSFODNN7EXAMPLE","region":"us-east-1"}}`
	if err := env.Repo.CreateProviderConnectionFull(id, "bedrock", "apikey",
		"Bedrock Integration", nil, data); err != nil {
		t.Fatalf("create bedrock connection: %v", err)
	}
	stored, err := env.Repo.GetProviderConnectionByID(id)
	if err != nil {
		t.Fatalf("read back bedrock connection: %v", err)
	}
	if stored == nil {
		t.Fatal("bedrock connection was not stored")
	}
	if !strings.Contains(stored.Data, up.URL) {
		t.Fatalf("bedrock connection stored %q, want the fake upstream URL — a request through it "+
			"would dial the real Bedrock runtime", truncate([]byte(stored.Data)))
	}
}

// TestBedrockStreamingUnwrapsEventStreamIntoSSE is the whole point of the port:
// a streaming Bedrock call arrives as binary AWS EventStream frames and must
// reach an OpenAI client as ordinary SSE, signed and pointed at the connection's
// own endpoint.
func TestBedrockStreamingUnwrapsEventStreamIntoSSE(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bedrockEventStream(t,
			`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":11,"output_tokens":1}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello from Bedrock"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
			`{"type":"message_stop"}`,
		))
	})
	addBedrockConnection(t, env, upstream)

	res := env.Post(t, "/v1/chat/completions",
		ChatBody("bedrock/us.anthropic.claude-sonnet-5", true))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if got := res.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	body := string(res.Body)
	if !strings.Contains(body, "Hello from Bedrock") {
		t.Errorf("stream body missing the decoded Anthropic text delta: %q", truncate(res.Body))
	}
	payloads := sseDataLines(t, res.Body)
	if len(payloads) == 0 {
		t.Fatal("stream carried no data frames")
	}
	if last := payloads[len(payloads)-1]; last != "[DONE]" {
		t.Errorf("last SSE frame = %q, want the [DONE] sentinel", last)
	}

	sent := upstream.Last(t)
	// SigV4 is the whole auth story here: without a real Authorization header the
	// request never leaves the gateway.
	if authz := sent.Header.Get("Authorization"); !strings.HasPrefix(authz, "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/") {
		t.Errorf("upstream Authorization = %q, want a SigV4 credential scope for the connection's key id", authz)
	}
	if sent.Header.Get("x-amz-date") == "" {
		t.Error("upstream request carries no x-amz-date, so it cannot have been signed")
	}
	// Bedrock rejects `model` in the body — it lives in the path — and requires the
	// version pin instead.
	if got := sent.Model(t); got != "" {
		t.Errorf("upstream body carries model %q, which Bedrock rejects", got)
	}
	var sentBody map[string]any
	if err := json.Unmarshal(sent.Body, &sentBody); err != nil {
		t.Fatalf("decode upstream request body: %v", err)
	}
	if got, _ := sentBody["anthropic_version"].(string); got != providers.BedrockAnthropicVersion {
		t.Errorf("upstream anthropic_version = %q, want %q", got, providers.BedrockAnthropicVersion)
	}
	if _, ok := sentBody["stream"]; ok {
		t.Error("upstream body still carries stream, which Bedrock rejects")
	}
	if !strings.Contains(sent.Path, "/model/us.anthropic.claude-sonnet-5/invoke-with-response-stream") {
		t.Errorf("upstream path = %q, want the streaming invoke path with the model id", sent.Path)
	}
}

// TestBedrockNonStreamingInvokePassesThrough pins the /invoke path: Bedrock has a
// real non-streaming endpoint returning one complete Anthropic body, and routing
// it through the EventStream decoder would hang waiting for frames that never come.
func TestBedrockNonStreamingInvokePassesThrough(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg_2","type":"message","role":"assistant",`+
			`"content":[{"type":"text","text":"non-streaming answer"}],"stop_reason":"end_turn",`+
			`"usage":{"input_tokens":5,"output_tokens":4}}`)
	})
	addBedrockConnection(t, env, upstream)

	res := env.Post(t, "/v1/chat/completions",
		ChatBody("bedrock/us.anthropic.claude-sonnet-5", false))
	if res.Status != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = %d, want 200 (body: %s)", res.Status, truncate(res.Body))
	}
	if !strings.Contains(string(res.Body), "non-streaming answer") {
		t.Errorf("response body missing the upstream text: %q", truncate(res.Body))
	}
	if !strings.HasSuffix(upstream.Last(t).Path, "/invoke") {
		t.Errorf("upstream path = %q, want the non-streaming invoke path", upstream.Last(t).Path)
	}
}

// TestBedrockRejectsTheWrongModelFamilyBeforeBilling pins that an xAI model on the
// Anthropic entry is refused before the call reaches AWS. Bedrock streams chunks
// this executor cannot read, and finding that out after the request is billed is
// the worse experience.
func TestBedrockRejectsTheWrongModelFamilyBeforeBilling(t *testing.T) {
	env := newEnv(t)
	upstream := env.NewUpstream(t, chatCompletionResponder())
	addBedrockConnection(t, env, upstream)

	res := env.Post(t, "/v1/chat/completions", ChatBody("bedrock/us.xai.grok-4.6", true))
	if res.Status == http.StatusOK {
		t.Fatalf("POST /v1/chat/completions = 200, want a rejection (body: %s)", truncate(res.Body))
	}
	if msg := res.ErrorMessage(t); !strings.Contains(msg, "is not supported by the") {
		t.Errorf("error message = %q, want it to name the expected model family", msg)
	}
	if n := upstream.Count(); n != 0 {
		t.Errorf("upstream received %d requests; the wrong-family model must be refused before the call is billed", n)
	}
}
