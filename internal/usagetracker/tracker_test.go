package usagetracker

import (
	"testing"
	"time"
)

func TestTracker_Lifecycle(t *testing.T) {
	tracker := NewTracker()

	// Initially empty
	state := tracker.GetActiveState(nil)
	if len(state.ActiveRequests) != 0 {
		t.Errorf("expected 0 active requests, got %d", len(state.ActiveRequests))
	}

	// 1. Request starts
	tracker.TrackPending("claude-sonnet-4-6", "anthropic", "conn_123", true, false)
	state = tracker.GetActiveState(nil)
	if len(state.ActiveRequests) != 1 {
		t.Fatalf("expected 1 active request, got %d", len(state.ActiveRequests))
	}
	if state.ActiveRequests[0].Provider != "anthropic" {
		t.Errorf("expected provider anthropic, got %s", state.ActiveRequests[0].Provider)
	}

	// 2. Request finishes with success and push recent
	tracker.TrackPending("claude-sonnet-4-6", "anthropic", "conn_123", false, false)
	tracker.PushRecent(RecentRequest{
		Timestamp:        time.Now().UTC().Format(time.RFC3339),
		Model:            "claude-sonnet-4-6",
		Provider:         "anthropic",
		PromptTokens:     100,
		CompletionTokens: 50,
		Status:           "success",
	}, nil)

	state = tracker.GetActiveState(nil)
	if len(state.ActiveRequests) != 0 {
		t.Errorf("expected 0 active requests after completion, got %d", len(state.ActiveRequests))
	}
	if len(state.RecentRequests) != 1 {
		t.Errorf("expected 1 recent request, got %d", len(state.RecentRequests))
	}
}

func TestTracker_Subscription(t *testing.T) {
	tracker := NewTracker()
	ch, unsub := tracker.Subscribe()
	defer unsub()

	tracker.TrackPending("gpt-4o", "openai", "conn_abc", true, false)

	select {
	case payload := <-ch:
		if len(payload) == 0 {
			t.Error("received empty payload")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("timed out waiting for subscription broadcast")
	}
}
