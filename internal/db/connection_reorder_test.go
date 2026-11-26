package db

import (
	"context"

	"slices"
	"testing"

	"github.com/samber/lo"
)

// seedOrderedConnections inserts connections in the given order. Each row gets
// priority i+1 unless the priorities map overrides it; a nil override value
// leaves the row NULL, which is how legacy/imported rows look.
func seedOrderedConnections(t *testing.T, repo *Repo, provider string, seedOrder []string, priorities map[string]*int) {
	t.Helper()
	for i, id := range seedOrder {
		if err := repo.CreateProviderConnection(context.Background(), id, provider, "apikey", id, `{"apiKey":"sk-`+id+`"}`); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		priority, ok := priorities[id]
		if !ok {
			priority = lo.ToPtr(i + 1)
		}
		if priority == nil {
			continue
		}
		if err := repo.SetConnectionPriority(id, *priority); err != nil {
			t.Fatalf("set priority %s: %v", id, err)
		}
	}
}

// rankedPool reads the pool back and asserts it carries a distinct contiguous
// 1..N sequence — the invariant that makes a tied pair recoverable.
func rankedPool(t *testing.T, repo *Repo, provider string) ([]string, map[string]int) {
	t.Helper()
	conns, err := repo.GetProviderConnections(provider, false)
	if err != nil {
		t.Fatalf("read pool: %v", err)
	}
	order := make([]string, 0, len(conns))
	priorities := make(map[string]int, len(conns))
	for _, c := range conns {
		if c.Priority == nil {
			t.Fatalf("connection %s was left with a NULL priority after reorder", c.ID)
		}
		order = append(order, c.ID)
		priorities[c.ID] = *c.Priority
	}
	for i, id := range order {
		if priorities[id] != i+1 {
			t.Errorf("connection %s priority = %d, want %d", id, priorities[id], i+1)
		}
	}
	return order, priorities
}

func TestReorderProviderConnections_Conditions(t *testing.T) {
	tests := []struct {
		name       string
		seedOrder  []string
		priorities map[string]*int
		moveID     string
		direction  int
		wantOrder  []string
	}{
		{
			name:      "moves the second row above the first",
			seedOrder: []string{"a", "b", "c"},
			moveID:    "b",
			direction: -1,
			wantOrder: []string{"b", "a", "c"},
		},
		{
			name:      "moves the first row below the second",
			seedOrder: []string{"a", "b", "c"},
			moveID:    "a",
			direction: 1,
			wantOrder: []string{"b", "a", "c"},
		},
		{
			name:      "a move past the last row leaves the order alone",
			seedOrder: []string{"a", "b", "c"},
			moveID:    "c",
			direction: 1,
			wantOrder: []string{"a", "b", "c"},
		},
		{
			name:      "a move past the first row leaves the order alone",
			seedOrder: []string{"a", "b", "c"},
			moveID:    "a",
			direction: -1,
			wantOrder: []string{"a", "b", "c"},
		},
		{
			name:      "renumbers a sparse sequence without gaps",
			seedOrder: []string{"a", "b", "c"},
			priorities: map[string]*int{
				"a": lo.ToPtr(10), "b": lo.ToPtr(20), "c": lo.ToPtr(90),
			},
			moveID:    "c",
			direction: -1,
			wantOrder: []string{"a", "c", "b"},
		},
		{
			name:      "repairs a tie left behind by a failed two-request swap",
			seedOrder: []string{"a", "b", "c"},
			// a and b both rank 1: the state a half-applied client swap used
			// to leave behind, and the reason the pair could never be
			// reordered again through the dashboard.
			priorities: map[string]*int{"a": lo.ToPtr(1), "b": lo.ToPtr(1), "c": lo.ToPtr(3)},
			moveID:     "a",
			direction:  1,
			wantOrder:  []string{"b", "a", "c"},
		},
		{
			name:       "ranks a NULL-priority row alongside the rest",
			seedOrder:  []string{"a", "b", "c"},
			priorities: map[string]*int{"b": nil},
			moveID:     "c",
			direction:  -1,
			// Seed order on disk is a(1), c(3), b(NULL) — NULL sorts last,
			// so moving c up puts it first, not second.
			wantOrder: []string{"c", "a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, cleanup := setupTestDB(t)
			defer cleanup()
			repo := NewRepo(database)
			seedOrderedConnections(t, repo, "reorder-test", tt.seedOrder, tt.priorities)

			before, err := repo.GetProviderConnections("reorder-test", false)
			if err != nil {
				t.Fatalf("read pool before reorder: %v", err)
			}

			if err := repo.ReorderProviderConnections("reorder-test", tt.moveID, tt.direction); err != nil {
				t.Fatalf("ReorderProviderConnections() error = %v", err)
			}

			order, _ := rankedPool(t, repo, "reorder-test")
			if len(order) != len(before) {
				t.Fatalf("reorder changed the pool size: got %d rows, want %d", len(order), len(before))
			}
			if !slices.Equal(order, tt.wantOrder) {
				t.Errorf("pool order = %v, want %v", order, tt.wantOrder)
			}
		})
	}
}

func TestReorderProviderConnections_RejectsForeignConnection(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	seedOrderedConnections(t, repo, "provider-a", []string{"a", "b"}, nil)

	if err := repo.ReorderProviderConnections("provider-a", "does-not-exist", -1); err == nil {
		t.Fatal("expected an error when reordering a connection outside the provider pool, got nil")
	}
}

func TestReorderProviderConnections_LeavesOtherProvidersAlone(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	seedOrderedConnections(t, repo, "provider-a", []string{"a1", "a2"}, nil)
	seedOrderedConnections(t, repo, "provider-b", []string{"b1", "b2"}, nil)

	if err := repo.ReorderProviderConnections("provider-a", "a2", -1); err != nil {
		t.Fatalf("ReorderProviderConnections() error = %v", err)
	}

	order, _ := rankedPool(t, repo, "provider-b")
	if !slices.Equal(order, []string{"b1", "b2"}) {
		t.Errorf("provider-b order = %v, want [b1 b2] untouched", order)
	}
}

func TestUpdateProviderConnection_PreservesNullPriority(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	seedOrderedConnections(t, repo, "null-priority", []string{"a", "b"}, map[string]*int{"a": nil})

	// A full-row update that carries no explicit priority must not promote the
	// NULL row to rank 0, which would sort it ahead of every other account.
	if err := repo.UpdateProviderConnection("a", "renamed", nil, true, `{"apiKey":"sk-a"}`); err != nil {
		t.Fatalf("UpdateProviderConnection() error = %v", err)
	}

	got, err := repo.GetProviderConnectionByID("a")
	if err != nil {
		t.Fatalf("GetProviderConnectionByID() error = %v", err)
	}
	if got.Priority != nil {
		t.Fatalf("priority = %d, want NULL to be preserved", *got.Priority)
	}
	if got.Name == nil || *got.Name != "renamed" {
		t.Errorf("name = %v, want the rename to have been applied", got.Name)
	}
}

func TestUpdateProviderConnection_WritesExplicitPriority(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	seedOrderedConnections(t, repo, "explicit-priority", []string{"a", "b"}, nil)

	if err := repo.UpdateProviderConnection("a", "renamed", lo.ToPtr(9), true, `{"apiKey":"sk-a"}`); err != nil {
		t.Fatalf("UpdateProviderConnection() error = %v", err)
	}

	got, err := repo.GetProviderConnectionByID("a")
	if err != nil {
		t.Fatalf("GetProviderConnectionByID() error = %v", err)
	}
	if got.Priority == nil || *got.Priority != 9 {
		t.Fatalf("priority = %v, want 9", got.Priority)
	}
}

func TestUpdateConnectionData_LeavesOtherColumnsAlone(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)
	seedOrderedConnections(t, repo, "data-only", []string{"a"}, nil)

	// A background writer refreshing a token must not revert a reorder or a
	// disable the user just performed.
	if err := repo.SetConnectionStatus("a", false); err != nil {
		t.Fatalf("SetConnectionStatus() error = %v", err)
	}
	if err := repo.SetConnectionPriority("a", 3); err != nil {
		t.Fatalf("SetConnectionPriority() error = %v", err)
	}
	if err := repo.UpdateConnectionData(context.Background(), "a", `{"apiKey":"sk-new"}`); err != nil {
		t.Fatalf("UpdateConnectionData(context.Background(), ) error = %v", err)
	}

	got, err := repo.GetProviderConnectionByID("a")
	if err != nil {
		t.Fatalf("GetProviderConnectionByID() error = %v", err)
	}
	if got.IsActive != 0 {
		t.Errorf("isActive = %d, want 0 (data-only write must not re-enable)", got.IsActive)
	}
	if got.Priority == nil || *got.Priority != 3 {
		t.Errorf("priority = %v, want 3 (data-only write must not revert a reorder)", got.Priority)
	}
	if got.Data != `{"apiKey":"sk-new"}` {
		t.Errorf("data = %q, want the refreshed payload", got.Data)
	}
}
