package chat

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

// A client that hung up is not an upstream failure. The request log has to
// agree with the response: the response says 499, so the row has to say 499
// too, not the 502 an aborted forward would otherwise be recorded as.
//
// The abort reaches the log as a transport error (*url.Error wrapping
// context.Canceled), not as the 499 upstreamError the response is built from,
// so the row used to record 502 while the client saw 499. The verdict is
// taken from the request context, and the log call site passed a nil context,
// so the check could not fire at all.
func TestLogFailureRecordsClientAbortAs499(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// A canceled request context is the client-abort signal: Go's HTTP
	// server cancels the request context when the client disconnects.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	handler.LogFailure(
		ctx,
		&UsageLogInfo{Provider: "openai", Model: "gpt-test", ConnectionID: "conn-1", Endpoint: "/v1/chat/completions"},
		&translator.OpenAIUsage{PromptTokens: 7},
		&upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":{"message":"upstream error: context canceled"}}`)},
		42,
		[]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
		nil,
	)

	var data string
	if err := database.QueryRow(`SELECT data FROM requestDetails WHERE status = 'error'`).Scan(&data); err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if !strings.Contains(data, `"status":499`) {
		t.Fatalf("client abort recorded as %s, want the 499 the response carries", data)
	}
	if strings.Contains(data, `"status":502`) {
		t.Fatalf("client abort recorded as 502: %s", data)
	}
}

// The flip side: an upstream failure on a live request is still an upstream
// failure. The abort verdict comes from the request context, so a request
// that was never canceled must keep its upstream status.
func TestLogFailureKeepsUpstreamStatusOnLiveRequest(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	handler.LogFailure(
		context.Background(),
		&UsageLogInfo{Provider: "openai", Model: "gpt-test", ConnectionID: "conn-1", Endpoint: "/v1/chat/completions"},
		&translator.OpenAIUsage{PromptTokens: 7},
		&upstreamError{StatusCode: http.StatusTooManyRequests, Body: []byte(`{"error":{"message":"rate limited"}}`)},
		42,
		[]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`),
		nil,
	)

	var data string
	if err := database.QueryRow(`SELECT data FROM requestDetails WHERE status = 'error'`).Scan(&data); err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if !strings.Contains(data, `"status":429`) {
		t.Fatalf("upstream failure recorded as %s, want the 429 the upstream returned", data)
	}
}
