package executor

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/providers"
	"9router/proxy/internal/translator"
)

// encoding/json/v2 rejects a repeated member name and invalid UTF-8, so a
// chunk carrying one is skipped by parseChatChunk and its content delta never
// reaches the client. v1 accepted both; the option set restores that.
func TestParseChatChunk_LenientAboutProviderChunks(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "repeated member name",
			payload: `{"id":"c1","id":"c2","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
			want:    "hi",
		},
		{
			name:    "invalid utf-8 in the delta",
			payload: "{\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"caf\xc3\xa9 \xff\"}}]}",
			want:    "caf\u00e9 \ufffd",
		},
		{
			name:    "unpaired surrogate escape",
			payload: `{"id":"c1","choices":[{"index":0,"delta":{"content":"ok \ud83d"}}]}`,
			want:    "ok \ufffd",
		},
		{
			name:    "member key spelled with different casing",
			payload: `{"Id":"c1","Choices":[{"Index":0,"Delta":{"Content":"hi"}}]}`,
			want:    "hi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunk := parseChatChunk([]byte(tt.payload))
			if chunk == nil {
				t.Fatalf("parseChatChunk(%s) = nil, want a chunk: the provider sent an answer", tt.payload)
			}
			if len(chunk.Choices) != 1 {
				t.Fatalf("parseChatChunk(%s) produced %d choices, want 1", tt.payload, len(chunk.Choices))
			}
			if got := chunk.Choices[0].Delta.Content; got != tt.want {
				t.Errorf("delta content = %q, want %q", got, tt.want)
			}
		})
	}
}

const chatStreamWithAnswer = "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"}}]}\n\n" +
	"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}\n\n" +
	"data: [DONE]\n\n"

// TestStreamChatToResponses_ReplaysChatStream pins the wire contract a
// /v1/responses client depends on: text arrives as output_text deltas and the
// stream is closed by response.completed, never by a bare connection drop.
func TestStreamChatToResponses_ReplaysChatStream(t *testing.T) {
	rec := httptest.NewRecorder()
	var ttft int64
	ctx := translator.WithRequestedModel(context.Background(), "gpt-x")

	if err := StreamChatToResponses(ctx, rec, strings.NewReader(chatStreamWithAnswer), time.Time{}, &ttft, nil); err != nil {
		t.Fatalf("StreamChatToResponses: %v", err)
	}

	body := rec.Body.String()
	for _, want := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.completed",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in bridge output:\n%s", want, body)
		}
	}
	if strings.Contains(body, "chat.completion.chunk") {
		t.Errorf("Chat Completions frames leaked to a Responses client:\n%s", body)
	}
	if !strings.Contains(body, `"text":"Hello"`) {
		t.Errorf("expected the completed item to carry the joined answer, got:\n%s", body)
	}
}

// TestStreamChatToResponses_TruncatedStreamStillCompletes guards the invariant
// the terminal event exists for: an upstream that dies mid-answer must still
// produce response.completed, or the client waits forever.
func TestStreamChatToResponses_TruncatedStreamStillCompletes(t *testing.T) {
	truncated := "data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"
	rec := httptest.NewRecorder()

	if err := StreamChatToResponses(context.Background(), rec, strings.NewReader(truncated), time.Time{}, nil, nil); err != nil {
		t.Fatalf("StreamChatToResponses: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "event: response.completed") {
		t.Errorf("truncated stream produced no terminal event:\n%s", rec.Body.String())
	}
}

// TestUpstreamSpeaksResponses covers both shapes that count as a native
// Responses endpoint. Getting either wrong translates a request that must stay
// untouched, which is how previous_response_id and store silently disappear.
func TestUpstreamSpeaksResponses(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		cfg      *providers.ProviderConfig
		want     bool
	}{
		{"codex serves the responses endpoint", "codex", "gpt-5.1", &providers.ProviderConfig{BaseURL: "https://chatgpt.com/backend-api/codex/responses"}, true},
		{"grok-cli serves the responses endpoint", "grok-cli", "grok-4.5", &providers.ProviderConfig{BaseURL: "https://cli-chat-proxy.grok.com/v1/responses"}, true},
		{"perplexity-agent serves the responses endpoint", "perplexity-agent", "sonar", &providers.ProviderConfig{BaseURL: "https://api.perplexity.ai/v1/responses"}, true},
		{"openai is a chat endpoint", "openai", "gpt-5.1", &providers.ProviderConfig{BaseURL: "https://api.openai.com/v1/chat/completions"}, false},
		{"muse-spark leaves the chat endpoint for opencode", "opencode", "muse-spark-1.3-contributor", &providers.ProviderConfig{BaseURL: "https://opencode.ai/zen/v1/chat/completions"}, true},
		{"gpt-5.6-luna leaves the chat endpoint for opencode-go", "opencode-go", "gpt-5.6-luna", &providers.ProviderConfig{BaseURL: "https://opencode.ai/zen/go/v1/chat/completions"}, true},
		{"gpt-5.4 answers the gpt family from responses upstream", "opencode", "gpt-5.4", &providers.ProviderConfig{BaseURL: "https://opencode.ai/zen/v1/chat/completions"}, true},
		{"a model name alone never implies responses", "openai", "muse-spark-1.3-contributor", &providers.ProviderConfig{BaseURL: "https://api.openai.com/v1/chat/completions"}, false},
		{"missing config cannot be native", "codex", "gpt-5.1", nil, false},
		{"muse spark is responses-native on zen", "opencode-zen", "muse-spark-1.3", &providers.ProviderConfig{BaseURL: "https://opencode.ai/zen/v1/chat/completions"}, true},
		{"gpt family is responses-native on zen", "opencode-zen", "gpt-5.4", &providers.ProviderConfig{BaseURL: "https://opencode.ai/zen/v1/chat/completions"}, true},
		{"zen chat-lane models are translated in, not relayed", "opencode-zen", "deepseek-v4-pro", &providers.ProviderConfig{BaseURL: "https://opencode.ai/zen/v1/chat/completions"}, false},
		{"zen messages-lane models are translated in, not relayed", "opencode-zen", "claude-sonnet-4-6", &providers.ProviderConfig{BaseURL: "https://opencode.ai/zen/v1/chat/completions"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UpstreamSpeaksResponses(tt.provider, tt.model, tt.cfg); got != tt.want {
				t.Errorf("UpstreamSpeaksResponses(%q, %q) = %v, want %v", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}

// TestHandleClaudeMessagesStream_ResponsesClient covers the two-hop case: a
// Claude Messages upstream answered for a /v1/responses client. The Claude
// events become OpenAI chunks and those become Responses events.
func TestHandleClaudeMessagesStream_ResponsesClient(t *testing.T) {
	upstream := strings.Join([]string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-x\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, "")

	rec := httptest.NewRecorder()
	ctx := translator.WithResponsesBridge(translator.WithClientFormat(context.Background(), translator.ClientFormatResponses))
	req := &Request{Ctx: ctx, IsStream: true}

	if err := handleClaudeMessagesStream(rec, req, strings.NewReader(upstream)); err != nil {
		t.Fatalf("handleClaudeMessagesStream: %v", err)
	}
	out := rec.Body.String()
	if strings.Contains(out, "chat.completion.chunk") {
		t.Errorf("Chat frames reached a Responses client:\n%s", out)
	}
	if !strings.Contains(out, "event: response.completed") {
		t.Errorf("two-hop stream never emitted response.completed:\n%s", out)
	}
}

// A /v1/messages client on a Claude upstream still needs fitted tool names
// restored. The TranslateResp branch is a passthrough — no OpenAI translation
// happens — but the request was fitted before it left, so skipping the
// decloaker there forwarded the 64-char name and left the client unable to
// dispatch the call.
func TestHandleClaudeMessages_TranslateRespRestoresToolNames(t *testing.T) {
	const longName = "mcp__server_name__action_detail_something_very_long_indeed_action_12345"
	const shortName = "mcp__server_name__action_detail_something_very_long_indeed_act_1"
toolMap := map[string]string{shortName: longName}

	t.Run("stream passthrough", func(t *testing.T) {
		upstream := strings.Join([]string{
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"" + shortName + "\"}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}, "")

		rec := httptest.NewRecorder()
		req := &Request{Ctx: context.Background(), IsStream: true, TranslateResp: true, ToolNameMap: toolMap}

		if err := handleClaudeMessagesStream(rec, req, strings.NewReader(upstream)); err != nil {
			t.Fatalf("handleClaudeMessagesStream: %v", err)
		}
		out := rec.Body.String()
		if strings.Contains(out, shortName) {
			t.Errorf("the fitted name reached the client undecloaked:\n%s", out)
		}
		if !strings.Contains(out, longName) {
			t.Errorf("expected the caller's name %q restored:\n%s", longName, out)
		}
	})

	t.Run("non-stream passthrough", func(t *testing.T) {
		body := `{"content":[{"type":"tool_use","id":"toolu_1","name":"` + shortName + `","input":{}}]}`
		rec := httptest.NewRecorder()
		req := &Request{Ctx: context.Background(), TranslateResp: true, ToolNameMap: toolMap}

		if err := handleClaudeMessagesNonStream(rec, req, strings.NewReader(body)); err != nil {
			t.Fatalf("handleClaudeMessagesNonStream: %v", err)
		}
		out := rec.Body.String()
		if strings.Contains(out, shortName) {
			t.Errorf("the fitted name reached the client undecloaked:\n%s", out)
		}
		if !strings.Contains(out, longName) {
			t.Errorf("expected the caller's name %q restored:\n%s", longName, out)
		}
	})
}
