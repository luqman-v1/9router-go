package db

import (
	"context"
	"reflect"
	"testing"
)

// TestGetRequestDetailsPagedWindowsByTimestamp pins the ?period floor the Details
// tab now sends: the page and its total must both come from inside the window.
// Reporting the table-wide count next to a filtered page is the "zero that means
// broken" failure this reader was already fixed for once, in the other
// direction: here the number would be too large instead of too small, and the
// pager would offer pages that the window can never fill.
func TestGetRequestDetailsPagedWindowsByTimestamp(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE requestDetails (
		id TEXT PRIMARY KEY,
		timestamp TEXT NOT NULL,
		provider TEXT,
		model TEXT,
		connectionId TEXT,
		status TEXT,
		data TEXT
	);`); err != nil {
		t.Fatalf("create requestDetails: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO requestDetails (id, timestamp, provider, model, status, data) VALUES
		('req-old', '2026-03-01T00:00:00Z', 'openai', 'gpt-5.5', 'success', '{"latency":{"total":10},"tokens":{"completion_tokens":1}}'),
		('req-mid', '2026-03-02T00:00:00Z', 'openai', 'gpt-5.5', 'success', '{"latency":{"total":20},"tokens":{"completion_tokens":2}}'),
		('req-new', '2026-03-03T00:00:00Z', 'openai', 'gpt-5.5', 'success', '{"latency":{"total":30},"tokens":{"completion_tokens":3}}');`); err != nil {
		t.Fatalf("seed requestDetails: %v", err)
	}

	tests := []struct {
		name      string
		since     string
		limit     int
		wantIDs   []string
		wantTotal int
	}{
		{
			name:      "no floor reads the whole table",
			since:     "",
			limit:     50,
			wantIDs:   []string{"req-new", "req-mid", "req-old"},
			wantTotal: 3,
		},
		{
			name:      "the floor keeps rows at or after it",
			since:     "2026-03-02T00:00:00Z",
			limit:     50,
			wantIDs:   []string{"req-new", "req-mid"},
			wantTotal: 2,
		},
		{
			name:      "the total describes the window, not the page",
			since:     "2026-03-02T00:00:00Z",
			limit:     1,
			wantIDs:   []string{"req-new"},
			wantTotal: 2,
		},
		{
			name:      "a floor past every row empties the page and the total",
			since:     "2026-04-01T00:00:00Z",
			limit:     50,
			wantIDs:   []string{},
			wantTotal: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, total, err := NewRepo(database).GetRequestDetailsPaged(context.Background(), tt.limit, 0, tt.since)
			if err != nil {
				t.Fatalf("GetRequestDetailsPaged(limit=%d, since=%q) error = %v", tt.limit, tt.since, err)
			}
			if total != tt.wantTotal {
				t.Errorf("total = %d, want %d", total, tt.wantTotal)
			}
			gotIDs := make([]string, 0, len(rows))
			for _, row := range rows {
				gotIDs = append(gotIDs, row.ID)
			}
			if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Errorf("ids = %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}
