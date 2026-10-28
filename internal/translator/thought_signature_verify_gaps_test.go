package translator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"9router/proxy/internal/db"
)

// The tests here pin the three ways the first pass at G26 fell short of its own
// discovery doc: a hydrate failure stopped the pruner, the disk TTL was the
// one-hour memory TTL, and the table had no size ceiling.

// flakyPersister wraps a real repo and can be told to fail the boot hydrate.
// It also records whether a prune ever ran, which is the whole point of the
// first test.
type flakyPersister struct {
	inner     *db.Repo
	failLoad  error
	pruneMu   sync.Mutex
	pruneRuns int
	trims     []int
}

func (f *flakyPersister) SaveThoughtSignature(ctx context.Context, key, signature, family string, expiresAt time.Time) error {
	return f.inner.SaveThoughtSignature(ctx, key, signature, family, expiresAt)
}

func (f *flakyPersister) GetThoughtSignature(ctx context.Context, key string) (*db.ThoughtSignatureRow, error) {
	return f.inner.GetThoughtSignature(ctx, key)
}

func (f *flakyPersister) LoadThoughtSignatures(ctx context.Context, limit int) ([]db.ThoughtSignatureRow, error) {
	if f.failLoad != nil {
		return nil, f.failLoad
	}
	return f.inner.LoadThoughtSignatures(ctx, limit)
}

func (f *flakyPersister) PruneThoughtSignatures(ctx context.Context, now time.Time) (int64, error) {
	f.pruneMu.Lock()
	f.pruneRuns++
	f.pruneMu.Unlock()
	return f.inner.PruneThoughtSignatures(ctx, now)
}

func (f *flakyPersister) TrimThoughtSignatures(ctx context.Context, keep int) (int64, error) {
	f.pruneMu.Lock()
	f.trims = append(f.trims, keep)
	f.pruneMu.Unlock()
	return f.inner.TrimThoughtSignatures(ctx, keep)
}

// TestThoughtSignatureStore_HydrateFailureKeepsPruning pins that a transient
// read error at boot does not leave the disk tier unmanaged. Writes still land
// in SQLite after a failed hydrate — persist is attached before the read — so
// giving up on the pruner there let the table grow for the life of the process.
func TestThoughtSignatureStore_HydrateFailureKeepsPruning(t *testing.T) {
	repo := openTestSignatureRepo(t)
	defer SetThoughtSignaturePersistence(nil)

	p := &flakyPersister{inner: repo, failLoad: errors.New("database is locked")}
	SetThoughtSignaturePersistence(p)

	store := globalThoughtSigStore
	store.mu.Lock()
	prunerStarted := store.pruneStop != nil
	persistAttached := store.persist != nil
	store.mu.Unlock()

	if !prunerStarted {
		t.Error("a failed hydrate must not stop the pruner from starting")
	}
	if !persistAttached {
		t.Fatal("persistence was not attached, so writes would not land at all")
	}

	// A write after the failed hydrate must still reach the database. If it
	// does not, the pruner argument above is moot.
	StoreGeminiThoughtSignature("call_after_failed_hydrate", "SIG_LATE", "sess", "gemini-2.5-flash")
	globalThoughtSigStore.drainSignatureWrites()
	row, err := repo.GetThoughtSignature(context.Background(), "call_after_failed_hydrate")
	if err != nil {
		t.Fatalf("read back after failed hydrate: %v", err)
	}
	if row == nil {
		t.Fatal("a write after a failed hydrate never reached the database, so nothing needed pruning")
	}
}

// TestThoughtSignatureStore_DiskTTLOutlivesMemory pins the two clocks. The disk
// row has to outlive the session that wrote it; writing it with the one-hour
// memory TTL made "survives a restart" mean "survives a restart within the
// hour".
func TestThoughtSignatureStore_DiskTTLOutlivesMemory(t *testing.T) {
	repo := openTestSignatureRepo(t)
	defer SetThoughtSignaturePersistence(nil)
	SetThoughtSignaturePersistence(repo)

	const callID = "call_disk_ttl"
	StoreGeminiThoughtSignature(callID, "SIG_TTL", "sess", "gemini-2.5-flash")
	globalThoughtSigStore.drainSignatureWrites()

	row, err := repo.GetThoughtSignature(context.Background(), callID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row == nil {
		t.Fatal("the row was never persisted")
	}

	remaining := time.Until(row.ExpiresAt)
	if remaining <= memoryTTL {
		t.Errorf("disk row expires in %v, no longer than the %v memory TTL: a restart after an hour loses it", remaining, memoryTTL)
	}
	if remaining > persistentTTL+time.Minute {
		t.Errorf("disk row outlives the %v persistent TTL: %v", persistentTTL, remaining)
	}
}

// TestRepo_TrimThoughtSignaturesKeepsNewest pins the size ceiling. Pruning by
// expiry alone leaves the table unbounded on a gateway that keeps serving, and
// the newest rows are the ones a restart is most likely to be asked for.
func TestRepo_TrimThoughtSignaturesKeepsNewest(t *testing.T) {
	repo := openTestSignatureRepo(t)

	now := time.Now()
	for _, key := range []string{"old1", "old2", "new1", "new2", "new3"} {
		if err := repo.SaveThoughtSignature(context.Background(), key, "SIG_"+key, "gemini", now.Add(time.Hour)); err != nil {
			t.Fatalf("save %s: %v", key, err)
		}
		// updatedAt is second-resolution; nudge each write apart so the
		// ordering in TrimThoughtSignatures is defined.
		time.Sleep(1100 * time.Millisecond)
	}

	if _, err := repo.TrimThoughtSignatures(context.Background(), 3); err != nil {
		t.Fatalf("trim: %v", err)
	}
	remaining, err := repo.LoadThoughtSignatures(context.Background(), 100)
	if err != nil {
		t.Fatalf("load after trim: %v", err)
	}
	if len(remaining) != 3 {
		t.Fatalf("expected the ceiling to keep 3 rows, got %d", len(remaining))
	}
	for _, row := range remaining {
		if row.Key != "new1" && row.Key != "new2" && row.Key != "new3" {
			t.Errorf("trim kept %q and dropped a newer row", row.Key)
		}
	}
}
