package chat

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"
)

func TestComboFallback_RecordsAttemptTrail(t *testing.T) {
	rec := NewFallbackAttemptRecorder()
	ctx := WithFallbackAttemptRecorder(context.Background(), rec)

	RecordAttemptFromContext(ctx, "conn-1", "Account 1", "opencode-zen", "deepseek-v4.1-flash", 429, errors.New("rate limited"), 120)
	RecordAttemptFromContext(ctx, "conn-2", "Account 2", "opencode-zen", "deepseek-v4.1-flash", 200, nil, 650)

	snapshot := rec.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(snapshot))
	}

	if snapshot[0].ConnectionID != "conn-1" || snapshot[0].StatusCode != 429 || snapshot[0].LatencyMs != 120 {
		t.Errorf("unexpected attempt 0: %+v", snapshot[0])
	}
	if snapshot[1].ConnectionID != "conn-2" || snapshot[1].StatusCode != 200 || snapshot[1].LatencyMs != 650 {
		t.Errorf("unexpected attempt 1: %+v", snapshot[1])
	}

	data, err := json.Marshal(snapshot[0])
	if err != nil {
		t.Fatalf("failed to marshal attempt: %v", err)
	}
	raw := string(data)
	if !strings.Contains(raw, `"connectionId":"conn-1"`) {
		t.Errorf("expected connectionId in json, got %s", raw)
	}
	if !strings.Contains(raw, `"statusCode":429`) {
		t.Errorf("expected statusCode in json, got %s", raw)
	}
	if !strings.Contains(raw, `"latencyMs":120`) {
		t.Errorf("expected latencyMs in json, got %s", raw)
	}
}
