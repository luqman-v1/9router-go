package chat

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
)

// The mask is wired to the write gate, not to the request: a row that is
// stored carries the mask, and when observability says no row is written there
// is nothing to mask. These pin both sides of that wiring on both paths.

// Assembled from two literals so the file never carries a whole credential
// shape: push protection reads a test fixture exactly as a live key.
const pastedKey = "sk-" + "ant-api03-1234567890abcdefghijklmnop"

func pastedKeyRequestBody() []byte {
	return []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"Authorization: ` + pastedKey + `"}]}`)
}

func TestSuccessDetailMasksPastedCredential(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	handler := NewChatHandler(db.NewRepo(database))

	handler.LogUsage(context.Background(),
		&UsageLogInfo{Provider: "openai", Model: "gpt-test", ConnectionID: "conn-1", Endpoint: "/v1/chat/completions"},
		&translator.OpenAIUsage{PromptTokens: 7, CompletionTokens: 3},
		42, pastedKeyRequestBody(), nil,
	)

	var data string
	if err := database.QueryRow(`SELECT data FROM requestDetails WHERE status = 'success'`).Scan(&data); err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if strings.Contains(data, pastedKey) {
		t.Errorf("success requestDetails stored the pasted credential in the clear: %s", data)
	}
	if !strings.Contains(data, "[REDACTED]") {
		t.Errorf("success requestDetails carries no mask where the credential was: %s", data)
	}
}

func TestFailureDetailMasksPastedCredential(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	handler := NewChatHandler(db.NewRepo(database))

	handler.LogFailure(context.Background(),
		&UsageLogInfo{Provider: "openai", Model: "gpt-test", ConnectionID: "conn-1", Endpoint: "/v1/chat/completions"},
		&translator.OpenAIUsage{PromptTokens: 7},
		&upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":{"message":"boom"}}`)},
		42, pastedKeyRequestBody(), nil,
	)

	var data string
	if err := database.QueryRow(`SELECT data FROM requestDetails WHERE status = 'error'`).Scan(&data); err != nil {
		t.Fatalf("read detail: %v", err)
	}
	if strings.Contains(data, pastedKey) {
		t.Errorf("failure requestDetails stored the pasted credential in the clear: %s", data)
	}
	if !strings.Contains(data, "[REDACTED]") {
		t.Errorf("failure requestDetails carries no mask where the credential was: %s", data)
	}
}

func TestSuccessDetailSkippedUnderErrorsOnly(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if err := repo.UpdateSettingsRaw(map[string]any{"observabilityMode": "errors_only"}); err != nil {
		t.Fatalf("set observabilityMode: %v", err)
	}
	handler := NewChatHandler(repo)

	handler.LogUsage(context.Background(),
		&UsageLogInfo{Provider: "openai", Model: "gpt-test", ConnectionID: "conn-1", Endpoint: "/v1/chat/completions"},
		&translator.OpenAIUsage{PromptTokens: 7, CompletionTokens: 3},
		42, pastedKeyRequestBody(), nil,
	)

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM requestDetails`).Scan(&count); err != nil {
		t.Fatalf("count details: %v", err)
	}
	if count != 0 {
		t.Errorf("errors_only success wrote %d detail rows, want 0", count)
	}
}

func TestFailureDetailSkippedWhenObservabilityOff(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	if err := repo.UpdateSettingsRaw(map[string]any{"observabilityMode": "off"}); err != nil {
		t.Fatalf("set observabilityMode: %v", err)
	}
	handler := NewChatHandler(repo)

	handler.LogFailure(context.Background(),
		&UsageLogInfo{Provider: "openai", Model: "gpt-test", ConnectionID: "conn-1", Endpoint: "/v1/chat/completions"},
		&translator.OpenAIUsage{PromptTokens: 7},
		&upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":{"message":"boom"}}`)},
		42, pastedKeyRequestBody(), nil,
	)

	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM requestDetails`).Scan(&count); err != nil {
		t.Fatalf("count details: %v", err)
	}
	if count != 0 {
		t.Errorf("observability off wrote %d detail rows, want 0", count)
	}
}
