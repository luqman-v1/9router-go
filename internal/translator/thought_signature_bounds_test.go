package translator

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	json "encoding/json/v2"
)

// The tests here pin the three bounds a review measured against the store: the
// row ceiling only existed on the hourly ticker, the memory tier was unbounded
// on the read-through path, and both read-through and hydrate copied the
// seven-day disk expiry into a tier whose contract is one hour.

// TestThoughtSignatureStore_BoundsRowsAtWriteTime pins the row ceiling where
// the rows are written. A budget of 10 000 enforced only by an hourly ticker
// let a burst reach 27 190 rows (3.7 MB) and stay there until the next tick,
// which is the interval the table is least protected in — the burst itself.
func TestThoughtSignatureStore_BoundsRowsAtWriteTime(t *testing.T) {
	repo := openTestSignatureRepo(t)

	// Seed the ceiling directly so the test measures the write path and not
	// how fast SQLite can absorb a synthesized burst. Each call below then
	// writes two keys (session-scoped and bare), so the table is pushed past
	// the ceiling on the first few hundred calls.
	expires := time.Now().Add(persistentTTL).UTC().Format(time.RFC3339)
	stamp := time.Now().UTC().Format(time.RFC3339)
	tx, err := repo.RawDB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	for i := 0; i < maxPersistentSignatures; i++ {
		if _, err := tx.ExecContext(context.Background(),
			`INSERT INTO thoughtSignatures(key, signature, family, expiresAt, updatedAt) VALUES(?, ?, ?, ?, ?)`,
			"seed_"+strconv.Itoa(i), "SIG_SEED", "gemini", expires, stamp,
		); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	p := &flakyPersister{inner: repo}
	t.Cleanup(func() { SetThoughtSignaturePersistence(nil) })
	SetThoughtSignaturePersistence(p)

	// Written in batches with a drain between them, which is what a tool-heavy
	// client actually produces: the writer keeps up, so no row is refused
	// because the queue was full. Dropping every write that a 1024-slot queue
	// cannot absorb is the documented fail-open path (§3.3), not a missing
	// bound, and a single un-drained dump measures that path instead of this
	// one — it never reaches the trim threshold at all.
	const calls = trimEveryWrites * 3
	const batch = 100
	for i := 0; i < calls; i += batch {
		for j := i; j < i+batch && j < calls; j++ {
			StoreGeminiThoughtSignature("call_bound_"+strconv.Itoa(j), "SIG_BOUND", "sess_bound", "gemini-2.5-pro")
		}
		globalThoughtSigStore.drainSignatureWrites()
	}

	var rows int
	if err := repo.RawDB().QueryRow(`SELECT COUNT(1) FROM thoughtSignatures`).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	limit := maxPersistentSignatures + trimEveryWrites
	if rows > limit {
		t.Fatalf("%d rows after %d calls on a full table: the write path does not enforce the %d-row ceiling (allowed overshoot %d)",
			rows, calls, maxPersistentSignatures, trimEveryWrites)
	}

	p.pruneMu.Lock()
	trims := len(p.trims)
	p.pruneMu.Unlock()
	if trims == 0 {
		t.Fatal("no trim ran while rows were being written; the ceiling is still left to the hourly ticker")
	}
}

// TestThoughtSignatureStore_L1BoundedOnReadThrough pins that a database read
// repopulates the hot map under the same ceiling as a write. Before this, 2 500
// read-through lookups left 2 500 entries against a 2 000 cap.
func TestThoughtSignatureStore_L1BoundedOnReadThrough(t *testing.T) {
	repo := openTestSignatureRepo(t)

	// A failing hydrate leaves L1 empty, so every lookup below has to go to
	// disk — the read-through path, in isolation.
	p := &flakyPersister{inner: repo, failLoad: errors.New("skip hydrate")}
	t.Cleanup(func() { SetThoughtSignaturePersistence(nil) })
	SetThoughtSignaturePersistence(p)

	ctx := context.Background()
	const keys = maxMemorySignatures + 500
	exp := time.Now().Add(persistentTTL)
	for i := 0; i < keys; i++ {
		k := "call_rt_" + strconv.Itoa(i)
		if err := repo.SaveThoughtSignature(ctx, k, "SIG_RT_"+strconv.Itoa(i), "gemini", exp); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	for i := 0; i < keys; i++ {
		k := "call_rt_" + strconv.Itoa(i)
		if got := GetGeminiThoughtSignature(k, "", "gemini-2.5-pro"); got != "SIG_RT_"+strconv.Itoa(i) {
			t.Fatalf("read-through for %s returned %q, want SIG_RT_%d", k, got, i)
		}
	}

	globalThoughtSigStore.mu.RLock()
	entries, order := len(globalThoughtSigStore.entries), len(globalThoughtSigStore.order)
	globalThoughtSigStore.mu.RUnlock()

	if entries > maxMemorySignatures {
		t.Errorf("L1 holds %d entries after %d read-through lookups, over the %d cap", entries, keys, maxMemorySignatures)
	}
	if order > entries {
		t.Errorf("order holds %d keys for %d entries: pruned keys leaked into the FIFO slice", order, entries)
	}
}

// TestThoughtSignatureStore_L1UsesMemoryTTL pins the two clocks in the
// direction the disk tier does not need: an entry brought into memory keeps the
// one-hour memory TTL, whatever the row on disk expires at. Copying the
// seven-day disk expiry upward retired the memory TTL entirely.
func TestThoughtSignatureStore_L1UsesMemoryTTL(t *testing.T) {
	repo := openTestSignatureRepo(t)

	t.Run("hydrate", func(t *testing.T) {
		const key = "call_ttl_hydrate"
		if err := repo.SaveThoughtSignature(context.Background(), key, "SIG_TTL", "gemini", time.Now().Add(persistentTTL)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		SetThoughtSignaturePersistence(nil)
		SetThoughtSignaturePersistence(repo)
		t.Cleanup(func() { SetThoughtSignaturePersistence(nil) })

		globalThoughtSigStore.mu.RLock()
		entry, ok := globalThoughtSigStore.entries[key]
		globalThoughtSigStore.mu.RUnlock()
		if !ok {
			t.Fatalf("hydrate did not bring %q into L1", key)
		}
		if remaining := time.Until(entry.expiresAt); remaining > memoryTTL+time.Minute {
			t.Errorf("hydrated entry expires in %v, want at most the %v memory TTL", remaining, memoryTTL)
		}
	})

	t.Run("read-through", func(t *testing.T) {
		const key = "call_ttl_readthrough"
		if err := repo.SaveThoughtSignature(context.Background(), key, "SIG_TTL_RT", "gemini", time.Now().Add(persistentTTL)); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// Failing hydrate keeps L1 empty so the lookup below reads through.
		p := &flakyPersister{inner: repo, failLoad: errors.New("skip hydrate")}
		SetThoughtSignaturePersistence(nil)
		SetThoughtSignaturePersistence(p)
		t.Cleanup(func() { SetThoughtSignaturePersistence(nil) })

		if got := GetGeminiThoughtSignature(key, "", "gemini-2.5-pro"); got != "SIG_TTL_RT" {
			t.Fatalf("read-through returned %q, want SIG_TTL_RT", got)
		}
		globalThoughtSigStore.mu.RLock()
		entry, ok := globalThoughtSigStore.entries[key]
		globalThoughtSigStore.mu.RUnlock()
		if !ok {
			t.Fatalf("read-through did not repopulate %q", key)
		}
		if remaining := time.Until(entry.expiresAt); remaining > memoryTTL+time.Minute {
			t.Errorf("read-through entry expires in %v, want at most the %v memory TTL", remaining, memoryTTL)
		}
	})
}

// TestRepo_TrimThoughtSignaturesStableWithinSameSecond pins a tiebreak on the
// trim order. updatedAt is second-resolution, so a burst trims against several
// rows that share a timestamp; with no tiebreak the order is whatever SQLite's
// scan produced, and the trim can delete the newest rows and keep the oldest.
func TestRepo_TrimThoughtSignaturesStableWithinSameSecond(t *testing.T) {
	repo := openTestSignatureRepo(t)

	stamp := time.Now().UTC().Format(time.RFC3339)
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	keys := []string{"same1", "same2", "same3", "same4", "same5"}
	for _, k := range keys {
		if _, err := repo.RawDB().ExecContext(context.Background(),
			`INSERT INTO thoughtSignatures(key, signature, family, expiresAt, updatedAt) VALUES(?, ?, ?, ?, ?)`,
			k, "SIG_"+k, "gemini", expires, stamp,
		); err != nil {
			t.Fatalf("insert %s: %v", k, err)
		}
	}

	if _, err := repo.TrimThoughtSignatures(context.Background(), 3); err != nil {
		t.Fatalf("trim: %v", err)
	}
	rows, err := repo.LoadThoughtSignatures(context.Background(), 100)
	if err != nil {
		t.Fatalf("load after trim: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows after the trim, got %d", len(rows))
	}
	for _, row := range rows {
		if row.Key == "same1" || row.Key == "same2" {
			t.Errorf("trim kept %q and dropped a row written after it: rows sharing a timestamp must fall back to insertion order", row.Key)
		}
	}
}

// TestGeminiThoughtSignature_CrossConversationIsolation is the reason the
// signature is namespaced at all. Two conversations on one account can mint the
// same tool_call id, and a signature is only valid for the call it was issued
// for — replaying the other conversation's signature is answered with
// 400 INVALID_ARGUMENT "Corrupted thought signature". The store used to record
// under a per-response id and read the bare key, so the second conversation saw
// the first one's signature.
func TestGeminiThoughtSignature_CrossConversationIsolation(t *testing.T) {
	ClearGeminiThoughtSignatures()

	const model = "gemini-2.5-pro"
	streamCall := func(session, sig string) string {
		state := &GeminiStreamState{SessionID: session, Model: model}
		chunk := `{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + sig +
			`","functionCall":{"name":"read_file","args":{"a":1},"id":"call_shared"}}]},"finishReason":"STOP"}]}`
		ids := streamedToolCallIDs(t, chunk, state)
		if len(ids) != 1 {
			t.Fatalf("want one tool_call id, got %v", ids)
		}
		// The client that echoes the id back may drop the __ts__ suffix, which
		// is the case the store has to answer.
		return geminiCleanToolCallID(ids[0])
	}

	callID := streamCall("sess_A", "SIG_A")
	if other := streamCall("sess_B", "SIG_B"); other != callID {
		t.Fatalf("the two conversations should have minted the same id, got %q and %q", callID, other)
	}

	if got := replayedSignature(t, callID, "sess_A"); got != "SIG_A" {
		t.Errorf("conversation A replayed %q, want SIG_A", got)
	}
	if got := replayedSignature(t, callID, "sess_B"); got != "SIG_B" {
		t.Errorf("conversation B replayed %q, want SIG_B", got)
	}
	// A conversation that never saw this call may still get the unsigned
	// fallback, which is a constant the translator applies to a first call —
	// what it must never get is a signature minted for another conversation.
	if got := replayedSignature(t, callID, "sess_C"); got == "SIG_A" || got == "SIG_B" {
		t.Errorf("a third conversation replayed %q, a signature minted for another conversation", got)
	}
}

// replayedSignature runs the same translation the gateway does for one request
// and returns the thought signature attached to the replayed function call.
func replayedSignature(t *testing.T, toolCallID, sessionID string) string {
	t.Helper()
	body := `{"model":"gemini-test","messages":[{"role":"assistant","content":"","tool_calls":[
		{"id":"` + toolCallID + `","type":"function","function":{"name":"read_file","arguments":"{}"}}
	]}]}`
	out, err := TranslateOpenAIToGeminiWithSession([]byte(body), sessionID)
	if err != nil {
		t.Fatalf("TranslateOpenAIToGeminiWithSession: %v", err)
	}
	var req GeminiRequest
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("unmarshal translated request: %v", err)
	}
	for _, content := range req.Contents {
		for _, part := range content.Parts {
			if part.FunctionCall != nil {
				return part.ThoughtSignature
			}
		}
	}
	t.Fatalf("no functionCall part in the translated request: %s", out)
	return ""
}
