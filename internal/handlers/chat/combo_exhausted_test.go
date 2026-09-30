package chat

import (
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// exhaustedBody renders an upstream 429 the way providers actually send it.
// A non-empty resetDelay produces the Google RPC ErrorInfo shape that
// extractRetryAfter reads a concrete reset instant from; an empty one produces
// a rate limit with no timing information at all.
func exhaustedBody(message, resetDelay string) string {
	if resetDelay == "" {
		return fmt.Sprintf(`{"error":{"message":%q,"type":"rate_limit_error","code":429}}`, message)
	}
	return fmt.Sprintf(`{"error":{"message":%q,"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED","metadata":{"quotaResetDelay":%q}}]}}`,
		message, resetDelay)
}

// setupExhaustedCombo points two providers at mock upstreams that always 429,
// and registers a combo over both so no candidate can serve the request.
func setupExhaustedCombo(t *testing.T, firstBody, secondBody string) (*ChatHandler, func()) {
	t.Helper()
	database, cleanupDB := setupChatTestDB(t)

	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(firstBody))
	}))
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(secondBody))
	}))

	if _, err := database.Exec(`DELETE FROM providerConnections WHERE id IN ('conn-1', 'conn-2')`); err != nil {
		t.Fatalf("clear seeded connections: %v", err)
	}

	baseURLOf := map[string]string{"mock-ds": firstUpstream.URL, "mock-gq": secondUpstream.URL}
	providerOf := map[string]string{"mock-ds": "deepseek", "mock-gq": "groq"}
	nameOf := map[string]string{"mock-ds": "DeepSeek Mock", "mock-gq": "Groq Mock"}
	for _, id := range []string{"mock-ds", "mock-gq"} {
		data, _ := json.Marshal(map[string]any{"apiKey": "sk-mock-key", "baseUrl": baseURLOf[id]})
		if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
			(?, ?, 'apikey', ?, 0, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
			id, providerOf[id], nameOf[id], string(data)); err != nil {
			t.Fatalf("insert %s connection: %v", id, err)
		}
	}

	comboModels, _ := json.Marshal([]string{"deepseek/deepseek-chat", "groq/qwen3-32b"})
	if _, err := database.Exec(`INSERT INTO combos (id, name, kind, models, createdAt, updatedAt) VALUES
		('combo-exhausted', 'combo-exhausted', 'fallback', ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`,
		string(comboModels)); err != nil {
		t.Fatalf("insert combo: %v", err)
	}

	return NewChatHandler(db.NewRepo(database)), func() {
		firstUpstream.Close()
		secondUpstream.Close()
		cleanupDB()
	}
}

func TestComboExhaustedCarriesResetTiming(t *testing.T) {
	tests := []struct {
		name        string
		endpoint    string
		firstDelay  string
		secondDelay string
		wantSecs    int // 0 means no candidate reported a cooldown at all
	}{
		{
			name:        "chat completions publishes the earliest candidate reset",
			endpoint:    "chat",
			firstDelay:  "150s",
			secondDelay: "900s",
			wantSecs:    150,
		},
		{
			name:        "messages publishes the earliest candidate reset",
			endpoint:    "messages",
			firstDelay:  "150s",
			secondDelay: "900s",
			wantSecs:    150,
		},
		{
			name:     "chat completions without cooldown info stays generic",
			endpoint: "chat",
			wantSecs: 0,
		},
		{
			name:     "messages without cooldown info stays generic",
			endpoint: "messages",
			wantSecs: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The later candidate cools down far further out, so a response
			// built from the wrong candidate is visibly wrong, not plausible.
			handler, cleanup := setupExhaustedCombo(t,
				exhaustedBody("quota exhausted on first", tt.firstDelay),
				exhaustedBody("quota exhausted on second", tt.secondDelay))
			defer cleanup()

			before := time.Now()
			var rec *httptest.ResponseRecorder
			if tt.endpoint == "chat" {
				req := httptest.NewRequest("POST", "/v1/chat/completions",
					strings.NewReader(`{"model":"combo-exhausted","messages":[{"role":"user","content":"hi"}],"stream":false}`))
				rec = httptest.NewRecorder()
				handler.HandleChatCompletions(rec, req)
			} else {
				req := httptest.NewRequest("POST", "/v1/messages",
					strings.NewReader(`{"model":"combo-exhausted","messages":[{"role":"user","content":"hi"}],"max_tokens":64,"stream":false}`))
				rec = httptest.NewRecorder()
				handler.HandleMessages(rec, req)
			}
			elapsed := time.Since(before)

			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429; body: %s", rec.Code, rec.Body.String())
			}

			var envelope struct {
				Error struct {
					Message    string `json:"message"`
					ResetAt    string `json:"reset_at"`
					RetryAfter *int   `json:"retry_after"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("response is not a JSON error envelope: %v; body: %s", err, rec.Body.String())
			}

			header := rec.Header().Get("Retry-After")
			if tt.wantSecs == 0 {
				if header != "" {
					t.Errorf("Retry-After = %q, want no header when no candidate reported a cooldown", header)
				}
				if envelope.Error.ResetAt != "" {
					t.Errorf("error.reset_at = %q, want absent when no candidate reported a cooldown", envelope.Error.ResetAt)
				}
				if envelope.Error.RetryAfter != nil {
					t.Errorf("error.retry_after = %d, want absent when no candidate reported a cooldown", *envelope.Error.RetryAfter)
				}
				if strings.Contains(envelope.Error.Message, "reset after") {
					t.Errorf("error.message = %q, want the upstream message with no reset suffix", envelope.Error.Message)
				}
				return
			}

			if header == "" {
				t.Fatalf("Retry-After header missing, want ~%ds; body: %s", tt.wantSecs, rec.Body.String())
			}
			headerSecs, err := strconv.Atoi(header)
			if err != nil {
				t.Fatalf("Retry-After = %q, want an integer number of seconds: %v", header, err)
			}
			if headerSecs < tt.wantSecs-3 || headerSecs > tt.wantSecs {
				t.Errorf("Retry-After = %ds, want ~%ds (earliest candidate, not the 900s one)", headerSecs, tt.wantSecs)
			}

			resetAt, err := time.Parse(time.RFC3339, envelope.Error.ResetAt)
			if err != nil {
				t.Fatalf("error.reset_at = %q, want an ISO-8601 timestamp: %v", envelope.Error.ResetAt, err)
			}
			want := time.Duration(tt.wantSecs) * time.Second
			if got := resetAt.Sub(before.Add(elapsed / 2)); got < want-4*time.Second || got > want+time.Second {
				t.Errorf("error.reset_at = %s, want ~%ds after the request (earliest candidate)", resetAt, tt.wantSecs)
			}
			if envelope.Error.RetryAfter == nil {
				t.Fatalf("error.retry_after missing, want %d; body: %s", headerSecs, rec.Body.String())
			}
			if *envelope.Error.RetryAfter != headerSecs {
				t.Errorf("error.retry_after = %d, want it to agree with the Retry-After header (%d)", *envelope.Error.RetryAfter, headerSecs)
			}
			// "2m" is the human form of the 150s the header just reported, so a
			// suffix that disagrees with retry_after is a failure, not a nit.
			if !strings.Contains(envelope.Error.Message, "reset after 2m") {
				t.Errorf("error.message = %q, want the human-readable reset suffix to match retry_after=%d", envelope.Error.Message, headerSecs)
			}
		})
	}
}

func TestWriteExhaustedComboError_ResetFieldBoundaries(t *testing.T) {
	const openAIMessage = `{"error":{"message":"quota exceeded","type":"rate_limit_error","code":429}}`

	tests := []struct {
		name          string
		retryAfter    string
		body          string
		wantHeader    string
		wantResetAt   bool
		wantRetryFor  bool
		wantBodyAsIs  bool
		wantMessageIs string
	}{
		{
			name:         "no cooldown info forwards the upstream error untouched",
			retryAfter:   "",
			body:         openAIMessage,
			wantBodyAsIs: true,
		},
		{
			name:         "non-JSON upstream body is forwarded even with a reset",
			retryAfter:   time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
			body:         "<html>gateway timeout</html>",
			wantHeader:   "120",
			wantBodyAsIs: true,
		},
		{
			name:         "upstream error without a message keeps its body and gains no fields",
			retryAfter:   time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
			body:         `{"error":{"type":"rate_limit_error"}}`,
			wantHeader:   "120",
			wantBodyAsIs: true,
		},
		{
			name:          "the human suffix names the same duration as the header",
			retryAfter:    time.Now().Add(150 * time.Second).UTC().Format(time.RFC3339),
			body:          openAIMessage,
			wantHeader:    "150",
			wantResetAt:   true,
			wantRetryFor:  true,
			wantMessageIs: "quota exceeded (reset after 2m 30s)",
		},
		{
			name:          "a reset in the past still reports a minimum of one second",
			retryAfter:    time.Now().Add(-90 * time.Second).UTC().Format(time.RFC3339),
			body:          openAIMessage,
			wantHeader:    "1",
			wantResetAt:   true,
			wantRetryFor:  true,
			wantMessageIs: "quota exceeded (reset after 0s)",
		},
		{
			name:          "an unparsable reset yields no reset_at rather than a bogus one",
			retryAfter:    "30",
			body:          openAIMessage,
			wantHeader:    "1",
			wantMessageIs: "quota exceeded ()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			cw := newCommittedResponseWriter(rec)
			writeExhaustedComboError(cw, &upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(tt.body)}, tt.retryAfter)

			if rec.Code != http.StatusTooManyRequests {
				t.Errorf("status = %d, want 429", rec.Code)
			}
			if got := rec.Header().Get("Retry-After"); got != tt.wantHeader {
				t.Errorf("Retry-After = %q, want %q", got, tt.wantHeader)
			}
			if tt.wantBodyAsIs {
				if got := rec.Body.String(); got != tt.body {
					t.Errorf("body = %q, want the upstream body verbatim (%q)", got, tt.body)
				}
				return
			}

			var envelope struct {
				Error struct {
					Message    string `json:"message"`
					ResetAt    string `json:"reset_at"`
					RetryAfter *int   `json:"retry_after"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("response is not a JSON error envelope: %v; body: %s", err, rec.Body.String())
			}
			if got := envelope.Error.Message; got != tt.wantMessageIs {
				t.Errorf("error.message = %q, want %q", got, tt.wantMessageIs)
			}
			if tt.wantResetAt {
				if _, err := time.Parse(time.RFC3339, envelope.Error.ResetAt); err != nil {
					t.Errorf("error.reset_at = %q, want an ISO-8601 timestamp: %v", envelope.Error.ResetAt, err)
				}
			} else if envelope.Error.ResetAt != "" {
				t.Errorf("error.reset_at = %q, want absent", envelope.Error.ResetAt)
			}
			if tt.wantRetryFor {
				if envelope.Error.RetryAfter == nil {
					t.Error("error.retry_after missing, want a value")
				} else if *envelope.Error.RetryAfter < 1 {
					t.Errorf("error.retry_after = %d, want at least 1 second", *envelope.Error.RetryAfter)
				}
			} else if envelope.Error.RetryAfter != nil {
				t.Errorf("error.retry_after = %d, want absent", *envelope.Error.RetryAfter)
			}
		})
	}
}
