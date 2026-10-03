package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// The rotation stamp has to be strictly increasing even when the wall clock does
// not advance, because on the Windows host this was diagnosed on time.Now only
// moved every ~815µs — several picks inside that window all formatted to the
// same string, and equal stamps are a tie the selector cannot break.
func TestStampConnection_FrozenClockStillOrdersStrictly(t *testing.T) {
	frozen := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	original := rotationClock
	rotationClock = func() time.Time { return frozen }
	t.Cleanup(func() { rotationClock = original })

	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	for i, name := range []string{"conn-a", "conn-b", "conn-c"} {
		if _, err := database.Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, data, createdAt, updatedAt)
			 VALUES (?, 'rr-frozen', 'apikey', ?, '{}', '2026-10-03T00:00:00Z', '2026-10-03T00:00:00Z')`,
			name, name,
		); err != nil {
			t.Fatalf("seed %s (priority %d): %v", name, i+1, err)
		}
	}

	previous := ""
	for i, name := range []string{"conn-a", "conn-b", "conn-c", "conn-a", "conn-b", "conn-c"} {
		stamp, err := repo.TouchConnectionRotation(name, 1)
		if err != nil {
			t.Fatalf("touch %s: %v", name, err)
		}
		if stamp <= previous {
			t.Fatalf("pick %d (%s): stamp %q did not advance past %q", i+1, name, stamp, previous)
		}
		previous = stamp
	}

	// The stored values must be exactly what the caller was handed, or the
	// in-memory row the selector reads would disagree with the row on disk.
	rows, err := database.Query(`SELECT id, lastUsedAt FROM providerConnections WHERE provider = 'rr-frozen' ORDER BY id ASC`)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer func() { _ = rows.Close() }()

	stored := map[string]string{}
	for rows.Next() {
		var id, lastUsedAt string
		if err := rows.Scan(&id, &lastUsedAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		stored[id] = lastUsedAt
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	for id, lastUsedAt := range stored {
		if lastUsedAt == "" {
			t.Errorf("%s: lastUsedAt was not persisted", id)
		}
	}
	if stored["conn-a"] >= stored["conn-c"] {
		t.Errorf("conn-a stamp %q must sort before conn-c stamp %q", stored["conn-a"], stored["conn-c"])
	}
	if stored["conn-b"] >= stored["conn-c"] {
		t.Errorf("conn-b stamp %q must sort before conn-c stamp %q", stored["conn-b"], stored["conn-c"])
	}
	if !sameFixedWidth(stored["conn-a"]) {
		t.Errorf("conn-a stamp %q is not fixed-width", stored["conn-a"])
	}
}

func openAt(t *testing.T, path string) (*sql.DB, func()) {
	t.Helper()
	database, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if err := EnsureCoreSchema(database); err != nil {
		_ = database.Close()
		t.Fatalf("core schema: %v", err)
	}
	return database, func() { _ = database.Close() }
}

func TestStampConnection_SurvivesRestartAndClockJump(t *testing.T) {
	original := rotationClock
	t.Cleanup(func() { rotationClock = original })

	path := filepath.Join(t.TempDir(), "rotation.sqlite")
	database, cleanup := openAt(t, path)
	defer cleanup()
	repo := NewRepo(database)
	for _, name := range []string{"conn-a", "conn-b"} {
		if _, err := database.Exec(
			`INSERT INTO providerConnections (id, provider, authType, name, data, createdAt, updatedAt)
			 VALUES (?, 'rr-restart', 'apikey', ?, '{}', '2026-10-03T00:00:00Z', '2026-10-03T00:00:00Z')`,
			name, name,
		); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	first, err := repo.TouchConnectionRotation("conn-a", 1)
	if err != nil {
		t.Fatalf("first touch: %v", err)
	}
	// Reopen: the stamp is read back from the row, not from process memory.
	if err := database.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	rotated, err := OpenDatabase(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = rotated.Close() }()

	// Clock jumps backwards (NTP correction, VM restore, hand-set clock).
	backwards := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rotationClock = func() time.Time { return backwards }

	second, err := NewRepo(rotated).TouchConnectionRotation("conn-b", 1)
	if err != nil {
		t.Fatalf("second touch after restart: %v", err)
	}
	if second <= first {
		t.Errorf("stamp after restart = %q, must sort after %q even with the clock rewound", second, first)
	}
}

func TestStampConnection_OlderSecondPrecisionRowStillOrders(t *testing.T) {
	frozen := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	original := rotationClock
	rotationClock = func() time.Time { return frozen }
	t.Cleanup(func() { rotationClock = original })

	database, cleanup := setupTestDB(t)
	defer cleanup()
	repo := NewRepo(database)

	// A row left behind by an older build: plain second-precision RFC3339.
	if _, err := database.Exec(
		`INSERT INTO providerConnections (id, provider, authType, name, data, lastUsedAt, consecutiveUseCount, createdAt, updatedAt)
		 VALUES ('conn-legacy', 'rr-legacy', 'apikey', 'legacy', '{}', '2026-10-03T09:00:00Z', 1, '2026-10-03T00:00:00Z', '2026-10-03T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// Clock is behind the stored stamp: the new stamp must still win.
	stamp, err := repo.TouchConnectionRotation("conn-new", 1)
	if err != nil {
		t.Fatalf("touch: %v", err)
	}
	if stamp <= "2026-10-03T09:00:00Z" {
		t.Errorf("stamp %q must sort after the legacy second-precision stamp", stamp)
	}
	if !sameFixedWidth(stamp) {
		t.Errorf("stamp %q is not fixed width", stamp)
	}
}

// The ordering rule has to hold for every combination of clock position and
// stored-stamp format, because the selector's tie-break is the string compare
// and nothing downstream can repair a stamp that sorted the wrong way.
func TestNextRotationStamp_AlwaysSortsAfterPrevious(t *testing.T) {
	base := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		clock    time.Time
		previous string
		// ordered is false for a stored value with no parseable timestamp:
		// there is no prior instant to overtake, so the stamp only has to be
		// well formed and the clock is used as-is.
		ordered bool
	}{
		{"first stamp of a fresh pool", base, "", true},
		{"clock already past the stamp", base.Add(time.Hour), "2026-10-03T09:00:00.000000000Z", true},
		{"clock frozen exactly on the stamp", base, "2026-10-03T09:00:00.000000000Z", true},
		{"clock frozen inside the stored second", base.Add(400 * time.Millisecond), "2026-10-03T09:00:00.500000000Z", true},
		{"clock rewound to an earlier second", base.Add(-time.Hour), "2026-10-03T09:00:00.000000000Z", true},
		{"legacy second-precision stamp, clock behind", base, "2026-10-03T09:00:00Z", true},
		{"legacy second-precision stamp, clock ahead", base.Add(time.Hour), "2026-10-03T09:00:00Z", true},
		{"unparseable stored value", base, "not-a-timestamp", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := rotationClock
			rotationClock = func() time.Time { return tt.clock }
			t.Cleanup(func() { rotationClock = original })

			stamp := nextRotationStamp(tt.previous)
			if tt.ordered && stamp <= tt.previous {
				t.Errorf("stamp %q does not sort after previous %q", stamp, tt.previous)
			}
			if !sameFixedWidth(stamp) {
				t.Errorf("stamp %q is not fixed width", stamp)
			}

			// The successor of a successor must keep advancing, which is what
			// a frozen clock relies on for every pick after the first.
			next := nextRotationStamp(stamp)
			if next <= stamp {
				t.Errorf("successor %q does not sort after %q", next, stamp)
			}
		})
	}
}

func sameFixedWidth(stamp string) bool {
	parsed, err := time.Parse(rotationTimestampFormat, stamp)
	if err != nil {
		return false
	}
	return parsed.UTC().Format(rotationTimestampFormat) == stamp
}