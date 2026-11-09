package chat

import (
	"context"
	"net/http"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/translator"
	"9router/proxy/internal/usagetracker"
)

func TestLogFailure_DifferentiatesCanceledFromError(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	repo := db.NewRepo(database)
	handler := NewChatHandler(repo)

	// Case 1: Client canceled (context.Canceled)
	ctxCancel, cancel := context.WithCancel(context.Background())
	cancel()
	infoCancel := &UsageLogInfo{Provider: "openai", Model: "gpt-4o", Endpoint: "/v1/chat/completions", ConnectionID: "c1"}
	handler.LogFailure(
		ctxCancel,
		infoCancel,
		&translator.OpenAIUsage{PromptTokens: 10},
		&upstreamError{StatusCode: http.StatusBadGateway, Body: []byte(`{"error":{"message":"context canceled"}}`)},
		100,
		[]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`),
		nil,
	)

	time.Sleep(5 * time.Millisecond)

	// Case 2: Upstream error (500)
	infoErr := &UsageLogInfo{Provider: "openai", Model: "gpt-4o", Endpoint: "/v1/chat/completions", ConnectionID: "c1"}
	handler.LogFailure(
		context.Background(),
		infoErr,
		&translator.OpenAIUsage{PromptTokens: 10},
		&upstreamError{StatusCode: http.StatusInternalServerError, Body: []byte(`{"error":{"message":"internal error"}}`)},
		250,
		[]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`),
		nil,
	)

	// Allow background writer to flush
	time.Sleep(100 * time.Millisecond)

	// Verify in tracker recent ring
	recent := usagetracker.GetTracker().GetRecent()
	if len(recent) < 2 {
		t.Fatalf("expected at least 2 recent requests in tracker ring, got %d", len(recent))
	}

	foundError := false
	foundCanceled := false
	for _, r := range recent {
		if r.Status == "error" {
			foundError = true
		}
		if r.Status == "canceled" {
			foundCanceled = true
		}
	}

	if !foundError {
		t.Errorf("expected error status recorded in recent requests, got: %+v", recent)
	}
	if !foundCanceled {
		t.Errorf("expected canceled status recorded in recent requests, got: %+v", recent)
	}

	// Verify failure recorded in requestDetails for diagnostics
	var errorDetailsCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM requestDetails WHERE status = 'error'`).Scan(&errorDetailsCount); err != nil {
		t.Fatalf("count error in requestDetails: %v", err)
	}
	if errorDetailsCount < 2 {
		t.Errorf("expected at least 2 diagnostic rows in requestDetails, got %d", errorDetailsCount)
	}

	// Verify usageHistory is untouched (billed spend ledger must not record failures)
	var usageHistoryCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM usageHistory`).Scan(&usageHistoryCount); err != nil {
		t.Fatalf("count usageHistory: %v", err)
	}
	if usageHistoryCount != 0 {
		t.Errorf("usageHistory rows = %d, want 0: failures must not be logged as billed spend", usageHistoryCount)
	}
}
