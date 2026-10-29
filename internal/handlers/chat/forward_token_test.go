package chat

import (
	"cmp"
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"9router/proxy/internal/constants"
	"9router/proxy/internal/db"
)

// TestForwardRequest_SingleTokenFieldOnWire asserts that an outbound
// OpenAI Chat Completions request never carries both max_tokens and
// max_completion_tokens, whatever the inbound client sent.
//
// Root cause: renameMaxTokensBody only rewrites the body for the registry
// `openai` provider. A custom openai-compatible node takes the generic
// ForwardOpenAI path, which forwards the client's bytes verbatim — so a client
// that sends both spellings (legal on the wire; some clients do) put both on
// one streaming request, and an upstream that rejects the pair answered 400
// "max_tokens and max_completion_tokens cannot both be set".
//
// The normalizer keeps the client-sent field. When the client sent no field
// at all it adds none, so a provider default is never overridden.
func TestForwardRequest_SingleTokenFieldOnWire(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		endpoint   string
		wantHas    string
		wantValue  float64
		wantAbsent []string
	}{
		{
			name:       "client sent max_tokens only",
			body:       `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],"max_tokens":4096,"stream":true}`,
			wantHas:    "max_tokens",
			wantValue:  4096,
			wantAbsent: []string{"max_completion_tokens"},
		},
		{
			name:       "client sent max_completion_tokens only",
			body:       `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":8192,"stream":true}`,
			wantHas:    "max_completion_tokens",
			wantValue:  8192,
			wantAbsent: []string{"max_tokens"},
		},
		{
			name:       "client sent both, max_completion_tokens wins",
			body:       `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],"max_tokens":1024,"max_completion_tokens":2048,"stream":true}`,
			wantHas:    "max_completion_tokens",
			wantValue:  2048,
			wantAbsent: []string{"max_tokens"},
		},
		{
			name:       "client sent neither",
			body:       `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],"stream":true}`,
			wantAbsent: []string{"max_tokens", "max_completion_tokens"},
		},
		{
			// A /v1/responses client on a Chat-speaking custom node: the body
			// is converted to Chat Completions before the normalizer runs, and
			// that conversion keeps both fields as sent. Gating on the endpoint
			// alone left the pair on the wire for this lane.
			name:       "responses client on a chat-speaking node, client sent both",
			body:       `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}],"max_tokens":1024,"max_completion_tokens":2048,"stream":true}`,
			endpoint:   "/v1/responses",
			wantHas:    "max_completion_tokens",
			wantValue:  2048,
			wantAbsent: []string{"max_tokens"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedBody, _ = io.ReadAll(r.Body)
				w.Header().Set(constants.HeaderContentType, constants.ContentTypeJSON)
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			}))
			defer srv.Close()

			database, cleanup := setupChatTestDB(t)
			defer cleanup()
			seedConnDB(t, database, "openai-compatible-chat-x", "conn-tok", "sk-tok", srv.URL)

			repo := db.NewRepo(database)
			h := NewChatHandler(repo)

			rec := httptest.NewRecorder()
			params := forwardRequestParams{
				Ctx:               context.Background(),
				W:                 rec,
				Provider:          "openai-compatible-chat-x",
				Model:             "gpt-6-luna",
				ConnectionID:      "conn-tok",
				ConnData:          &ConnectionData{APIKey: "sk-tok", BaseURL: srv.URL},
				Body:              []byte(tt.body),
				IsStream:          true,
				TranslateResponse: false,
				Endpoint:          cmp.Or(tt.endpoint, "/v1/chat/completions"),
			}
			if err := h.tryForwardWithConnection(params); err != nil {
				t.Fatalf("tryForwardWithConnection: %v", err)
			}

			var got map[string]any
			if err := json.Unmarshal(capturedBody, &got); err != nil {
				t.Fatalf("unmarshal captured wire body: %v", err)
			}
			if tt.wantHas != "" {
				v, ok := got[tt.wantHas]
				if !ok {
					t.Fatalf("wire body missing %q: %s", tt.wantHas, capturedBody)
				}
				if n, _ := v.(float64); n != tt.wantValue {
					t.Errorf("%s = %v, want %v", tt.wantHas, v, tt.wantValue)
				}
			}
			for _, absent := range tt.wantAbsent {
				if _, ok := got[absent]; ok {
					t.Errorf("wire body must not carry %q: %s", absent, capturedBody)
				}
			}
		})
	}
}
