package db

import (
	"testing"
)

func TestBlockedIPsCRUD(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(db)

	// List on empty
	list, err := repo.ListBlockedIPs()
	if err != nil {
		t.Fatalf("ListBlockedIPs on empty: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 entries, got %d", len(list))
	}

	// Check unblocked
	blocked, err := repo.IsIPBlocked("203.0.113.7")
	if err != nil {
		t.Fatalf("IsIPBlocked: %v", err)
	}
	if blocked {
		t.Error("expected 203.0.113.7 to be unblocked initially")
	}

	// Block IP
	entry, err := repo.BlockIP("203.0.113.7", "US", "spamming", "admin")
	if err != nil {
		t.Fatalf("BlockIP: %v", err)
	}
	if entry.IP != "203.0.113.7" || entry.Country != "US" || entry.Reason != "spamming" {
		t.Errorf("unexpected entry: %+v", entry)
	}

	// Verify blocked
	blocked, err = repo.IsIPBlocked("203.0.113.7")
	if err != nil {
		t.Fatalf("IsIPBlocked: %v", err)
	}
	if !blocked {
		t.Error("expected 203.0.113.7 to be blocked after BlockIP")
	}

	// List
	list, err = repo.ListBlockedIPs()
	if err != nil {
		t.Fatalf("ListBlockedIPs: %v", err)
	}
	if len(list) != 1 || list[0].IP != "203.0.113.7" {
		t.Errorf("unexpected list: %+v", list)
	}

	// Unblock IP
	if err := repo.UnblockIP("203.0.113.7"); err != nil {
		t.Fatalf("UnblockIP: %v", err)
	}

	// Verify unblocked
	blocked, err = repo.IsIPBlocked("203.0.113.7")
	if err != nil {
		t.Fatalf("IsIPBlocked: %v", err)
	}
	if blocked {
		t.Error("expected 203.0.113.7 to be unblocked after UnblockIP")
	}
}
