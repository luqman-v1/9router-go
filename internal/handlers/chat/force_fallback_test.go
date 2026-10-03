package chat

import (
	"database/sql"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// seedForceFallback writes the dashboard toggle the operator flipped in
// Settings -> Default Routing Strategy.
func seedForceFallback(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.Exec(
		`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`,
		`{"forceFallback":true}`); err != nil {
		t.Fatalf("seed forceFallback: %v", err)
	}
}

func TestForceMinCooldownConnection_PicksSoonestToReset(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	inTenMinutes := time.Now().UTC().Add(10 * time.Minute)
	inTwoMinutes := time.Now().UTC().Add(2 * time.Minute)
	// Priority order puts the LATER reset first, so a selector that merely
	// takes the head of the list fails this: the forced account must be the one
	// that frees up first, like upstream's forcedCandidates sort.
	insertDeepseekConn(t, database, "conn-later", 1, cooldownData(t, &inTenMinutes))
	insertDeepseekConn(t, database, "conn-sooner", 2, cooldownData(t, &inTwoMinutes))

	handler := NewChatHandler(db.NewRepo(database))
	conns, err := handler.Repo.GetProviderConnections("deepseek", true)
	if err != nil {
		t.Fatalf("GetProviderConnections: %v", err)
	}

	if _, _, ok := handler.forceMinCooldownConnection("deepseek", conns, map[string]bool{}, "deepseek-chat", nil); ok {
		t.Fatal("force fallback is off by default and must not force a cooling account")
	}

	settings := &db.SettingsData{ForceFallback: true}
	conn, until, ok := handler.forceMinCooldownConnection("deepseek", conns, map[string]bool{}, "deepseek-chat", settings)
	if !ok {
		t.Fatal("force fallback enabled: expected a forced account")
	}
	if conn.ID != "conn-sooner" {
		t.Errorf("forced %q, want conn-sooner (the account that frees up first)", conn.ID)
	}
	if delta := until.Sub(inTwoMinutes); delta > time.Second || delta < -time.Second {
		t.Errorf("reported reset %s, want ~%s", until.UTC(), inTwoMinutes.UTC())
	}
}

// A client-excluded account must stay out even when it would reset first:
// upstream filters the excluded connection out of the forced candidates so a
// retry loop cannot land on the account that just failed.
func TestForceMinCooldownConnection_SkipsExcluded(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	inTwoMinutes := time.Now().UTC().Add(2 * time.Minute)
	inTenMinutes := time.Now().UTC().Add(10 * time.Minute)
	insertDeepseekConn(t, database, "conn-excluded", 1, cooldownData(t, &inTwoMinutes))
	insertDeepseekConn(t, database, "conn-later", 2, cooldownData(t, &inTenMinutes))

	handler := NewChatHandler(db.NewRepo(database))
	conns, err := handler.Repo.GetProviderConnections("deepseek", true)
	if err != nil {
		t.Fatalf("GetProviderConnections: %v", err)
	}
	settings := &db.SettingsData{ForceFallback: true}

	conn, _, ok := handler.forceMinCooldownConnection("deepseek", conns,
		map[string]bool{"conn-excluded": true}, "deepseek-chat", settings)
	if !ok {
		t.Fatal("expected the remaining cooling account to be forced")
	}
	if conn.ID != "conn-later" {
		t.Errorf("forced %q, want conn-later", conn.ID)
	}

	// Every candidate excluded: forcing would re-serve the account that just
	// failed, so the caller must keep failing instead.
	if _, _, ok := handler.forceMinCooldownConnection("deepseek", conns,
		map[string]bool{"conn-excluded": true, "conn-later": true}, "deepseek-chat", settings); ok {
		t.Fatal("forced an account even though every candidate was excluded")
	}
}

// With the toggle on, a request whose accounts are all cooling must be handed
// the account that frees up first instead of failing the client turn.
func TestGetBestConnection_ForceFallbackServesCoolingAccount(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	inTenMinutes := time.Now().UTC().Add(10 * time.Minute)
	inTwoMinutes := time.Now().UTC().Add(2 * time.Minute)
	insertDeepseekConn(t, database, "conn-later", 1, cooldownData(t, &inTenMinutes))
	insertDeepseekConn(t, database, "conn-sooner", 2, cooldownData(t, &inTwoMinutes))
	seedForceFallback(t, database)

	handler := NewChatHandler(db.NewRepo(database))
	conn, connData, err := handler.getBestConnection("deepseek", "", nil, "deepseek-chat")
	if err != nil {
		t.Fatalf("getBestConnection with force fallback on: %v", err)
	}
	if conn.ID != "conn-sooner" {
		t.Errorf("selected %q, want conn-sooner", conn.ID)
	}
	if connData == nil {
		t.Fatal("a forced account must still carry its connection data")
	}
}

// The environment override alone must enable the behaviour, so a deployment
// survives a provider outage before anyone opens the dashboard.
func TestGetBestConnection_ForceFallbackEnvOverride(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	t.Setenv(forceFallbackEnv, "true")

	dropSeededConn(t, database)
	inTwoMinutes := time.Now().UTC().Add(2 * time.Minute)
	insertDeepseekConn(t, database, "conn-cooling", 1, cooldownData(t, &inTwoMinutes))

	handler := NewChatHandler(db.NewRepo(database))
	conn, _, err := handler.getBestConnection("deepseek", "", nil, "deepseek-chat")
	if err != nil {
		t.Fatalf("getBestConnection with %s=true: %v", forceFallbackEnv, err)
	}
	if conn.ID != "conn-cooling" {
		t.Errorf("selected %q, want conn-cooling", conn.ID)
	}
}

// With the toggle off the selector keeps failing, so a deployment that never
// opted in behaves exactly as before.
func TestGetBestConnection_ForceFallbackOffKeepsFailing(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	inTwoMinutes := time.Now().UTC().Add(2 * time.Minute)
	insertDeepseekConn(t, database, "conn-cooling", 1, cooldownData(t, &inTwoMinutes))

	handler := NewChatHandler(db.NewRepo(database))
	_, _, err := handler.getBestConnection("deepseek", "", nil, "deepseek-chat")
	if err == nil {
		t.Fatal("expected the all-in-cooldown failure when force fallback is off")
	}
	if !containsAll(err.Error(), "cooldown", inTwoMinutes.UTC().Format(time.RFC3339)) {
		t.Errorf("error should still name the earliest reset, got: %v", err)
	}
}

// A model every account is model-locked on is not a cooldown problem, so force
// fallback must not quietly discard the locks and hammer a known-bad model.
func TestGetBestConnection_ForceFallbackIgnoresModelLocks(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	dropSeededConn(t, database)
	inTwoMinutes := time.Now().UTC().Add(2 * time.Minute)
	insertDeepseekConn(t, database, "conn-locked", 1, cooldownData(t, &inTwoMinutes))
	seedForceFallback(t, database)

	repo := db.NewRepo(database)
	if err := repo.LockConnectionModel("conn-locked", "deepseek-chat", 300, 1); err != nil {
		t.Fatalf("lock connection model: %v", err)
	}

	handler := NewChatHandler(repo)
	if _, _, err := handler.getBestConnection("deepseek", "", nil, "deepseek-chat"); err == nil {
		t.Fatal("a model-locked account must stay out even with force fallback on")
	}
}