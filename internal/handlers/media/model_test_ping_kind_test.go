package media

import (
	"context"

	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"9router/proxy/internal/handlers/chat"
)

// TestHandleTestModelLLMProbeKind covers the exact default kind the Add Custom
// Model dialog sends: web/src/components/connections/AddCustomModelModal.svelte
// posts {"model":"<alias>/<id>","kind":"llm"} for its Chat / LLM type. The model
// uses a generic test provider here so the assertion isolates kind dispatch
// instead of Codex-specific response framing. Upstream accepts "llm" and probes
// it on the chat lane (master commits 850eb573 and e414975d); this port's
// validation must agree.
func TestHandleTestModelLLMProbeKind(t *testing.T) {
	chatCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "chat") {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"unexpected probe lane: ` + r.URL.Path + `"}}`))
			return
		}
		chatCalls++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}]}`))
	}))
	defer srv.Close()

	database, cleanup := setupMediaTestDB(t)
	defer cleanup()

	priority := 1
	if err := database.CreateProviderConnectionFull(context.Background(), 
		"conn-llm-kind", "testprov", "sk-test", "testconn", &priority,
		`{"apiKey":"sk-test","baseUrl":"`+srv.URL+`"}`,
	); err != nil {
		t.Fatal(err)
	}

	handler := NewMediaHandler(database, nil, chat.NewChatHandler(database))

	tests := []struct {
		name        string
		body        string
		wantStatus  int
		wantOK      bool
		wantErrPart string
	}{
		{
			name:       "dashboard default llm kind",
			body:       `{"model":"testprov/gpt-6.2-sol","kind":"llm"}`,
			wantStatus: http.StatusOK,
			wantOK:     true,
		},
		{
			name:       "empty kind keeps working",
			body:       `{"model":"testprov/gpt-6.2-sol"}`,
			wantStatus: http.StatusOK,
			wantOK:     true,
		},
		{
			name:        "unknown kind is still rejected",
			body:        `{"model":"testprov/gpt-6.2-sol","kind":"bogus"}`,
			wantStatus:  http.StatusBadRequest,
			wantErrPart: "unsupported model kind: bogus",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/models/test", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			handler.HandleTestModel(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var result testModelResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatalf("unmarshal response: %v; body = %s", err, rec.Body.String())
			}
			if result.OK != tt.wantOK {
				t.Errorf("OK = %v, want %v; body = %s", result.OK, tt.wantOK, rec.Body.String())
			}
			if tt.wantErrPart != "" && !strings.Contains(result.Error, tt.wantErrPart) {
				t.Errorf("Error = %q, want it to contain %q", result.Error, tt.wantErrPart)
			}
		})
	}

	if chatCalls != 2 {
		t.Errorf("chat probe calls = %d, want 2 (llm and empty kind)", chatCalls)
	}
}

func TestIsProbeKindAcceptsUpstreamKinds(t *testing.T) {
	tests := []struct {
		kind string
		want bool
	}{
		{kind: "", want: true},
		{kind: "llm", want: true},
		{kind: "embedding", want: true},
		{kind: "image", want: true},
		{kind: "tts", want: true},
		{kind: "stt", want: true},
		{kind: "video", want: true},
		{kind: "systemone", want: true},
		{kind: "bogus", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			if got := isProbeKind(tt.kind); got != tt.want {
				t.Errorf("isProbeKind(%q) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}
