package translator

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/log"
)

const (
	maxMemorySignatures = 2000
	memoryTTL           = time.Hour

	// persistentTTL is how long an entry survives in SQLite. It is longer than
	// memoryTTL on purpose: memory is the hot path and can afford to forget,
	// while the disk tier exists to answer after a restart, so it has to
	// outlive the session that wrote it. The discovery doc's L2 contract is
	// 7 days.
	persistentTTL = 7 * 24 * time.Hour

	// maxPersistentSignatures mirrors the L2 capacity ceiling. Pruning only by
	// expiry let the table grow for as long as rows kept arriving, which on a
	// long-lived gateway with a tool-heavy client is unbounded.
	maxPersistentSignatures = 10000

	// writeQueueDepth absorbs a burst of tool calls without blocking the
	// request that produced them (discovery doc §3.2).
	writeQueueDepth = 1024

	// trimEveryWrites is how many rows the writer inserts between two trims.
	// The hourly ticker alone left the table unmanaged for its whole interval:
	// a burst of 22 000 tool calls measured 27 190 rows (3.7 MB) and stayed
	// there until the next tick. Trimming on the writer bounds the overshoot
	// at one batch instead of one hour.
	trimEveryWrites = 1000
)

// asyncWriteTask is one row for the background writer. A nil key is a barrier:
// it writes nothing and closes done once every task queued before it has
// landed, which is how a test observes the writer without a sleep.
type asyncWriteTask struct {
	key       string
	signature string
	family    string
	expiresAt time.Time
	done      chan struct{}
}

type signatureEntry struct {
	signature string
	family    string
	expiresAt time.Time
}

// SignatureFamily returns the normalized model family ("claude", "gemini", or original)
// so signatures are not replayed to incompatible model families (upstream parity with bc3be0cb).
func SignatureFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	if strings.Contains(m, "claude") {
		return "claude"
	}
	if strings.Contains(m, "gemini") {
		return "gemini"
	}
	return m
}

func isFamilyCompatible(entryFamily, targetFamily string) bool {
	return entryFamily == "" || targetFamily == "" || entryFamily == targetFamily
}

// l1Expiry is the in-memory deadline for a row read from disk: the memory TTL,
// which is the tier's own contract, capped by when the row itself expires so a
// shorter-lived row is never outlived by its copy in memory.
func l1Expiry(now, rowExpiry time.Time) time.Time {
	memExpiry := now.Add(memoryTTL)
	if memExpiry.After(rowExpiry) {
		return rowExpiry
	}
	return memExpiry
}

type thoughtSignatureStore struct {
	mu      sync.RWMutex
	entries map[string]signatureEntry
	order   []string // FIFO / LRU approximation

	// persist is the optional backing store. Nil keeps the historical
	// process-local behaviour; the gateway attaches the SQLite repo at boot.
	// All persistence calls are fail-open: a dead database must never turn a
	// translation into an error.
	persist thoughtSignaturePersister
	// pruneStop is closed to stop the expiry-pruning goroutine.
	pruneStop chan struct{}

	// writeQueue is the bounded async write path. Persisting inline put a
	// SQLite round-trip on the request that produced each signature, so a
	// burst of tool calls serialised on the database's write lock in the
	// middle of a live turn.
	writeQueue chan asyncWriteTask
	// writerStop ends the background writer; nil when it never started.
	writerStop chan struct{}
	// writerDone closes when the writer has drained and exited, so swapping
	// the backing store waits for the old queue instead of racing it (§3.4).
	writerDone chan struct{}
	// dropped counts writes refused because the queue was full. Fail-open:
	// L1 already holds the entry, so the live session is unaffected and only
	// restart recovery loses it.
	dropped atomic.Uint64
}

var globalThoughtSigStore = &thoughtSignatureStore{
	entries: make(map[string]signatureEntry),
	order:   make([]string, 0, maxMemorySignatures),
}

// thoughtSignaturePersister is the persistence seam. *db.Repo satisfies it; a
// nil value (or a nil interface) means "memory only".
type thoughtSignaturePersister interface {
	SaveThoughtSignature(ctx context.Context, key, signature, family string, expiresAt time.Time) error
	GetThoughtSignature(ctx context.Context, key string) (*db.ThoughtSignatureRow, error)
	LoadThoughtSignatures(ctx context.Context, limit int) ([]db.ThoughtSignatureRow, error)
	PruneThoughtSignatures(ctx context.Context, now time.Time) (int64, error)
	TrimThoughtSignatures(ctx context.Context, keep int) (int64, error)
}

// SetThoughtSignaturePersistence attaches a backing store and hydrates the
// in-memory map from it, so a Get after restart answers without touching disk.
// Passing nil detaches persistence and clears memory (tests simulate a restart
// by calling this). Safe to call more than once; the previous pruner stops.
//
// Fail-open: a hydrate or prune error is logged and ignored — the gateway keeps
// serving from memory exactly as it did before persistence existed.
func SetThoughtSignaturePersistence(p thoughtSignaturePersister) {
	store := globalThoughtSigStore

	store.mu.Lock()
	if store.pruneStop != nil {
		close(store.pruneStop)
		store.pruneStop = nil
	}
	if store.writerStop != nil {
		close(store.writerStop)
		store.writerStop = nil
	}
	prevDone := store.writerDone
	store.writerDone = nil
	store.writeQueue = nil
	store.persist = p
	clear(store.entries)
	store.order = store.order[:0]
	store.mu.Unlock()

	// Wait for the previous writer's queue to drain before the new one starts:
	// otherwise a task still sitting in the old queue would be written by the
	// old persister after the swap, and a test that closes the database would
	// see a write land on a closed handle.
	if prevDone != nil {
		<-prevDone
	}

	if p == nil {
		return
	}

	// A hydrate failure is not a reason to give up on the pruner. persist is
	// already attached, so writes still land in SQLite; returning here used to
	// leave those rows with no pruner for the life of the process, and the
	// table grew without bound after a single transient read error.
	if rows, err := p.LoadThoughtSignatures(context.Background(), maxMemorySignatures); err != nil {
		log.Warn("translator", "thought signature hydration skipped", "error", err)
	} else {
		now := time.Now()
		store.mu.Lock()
		for _, row := range rows {
			if !now.Before(row.ExpiresAt) {
				continue
			}
			// The row carries the seven-day disk expiry; memory keeps its own
			// one-hour clock. Copying the disk value upward retired the memory
			// TTL entirely, so an entry evicted from L1 could never age out.
			store.entries[row.Key] = signatureEntry{
				signature: row.Signature,
				family:    row.Family,
				expiresAt: l1Expiry(now, row.ExpiresAt),
			}
			store.order = append(store.order, row.Key)
		}
		store.mu.Unlock()
	}

	stop := make(chan struct{})
	queue := make(chan asyncWriteTask, writeQueueDepth)
	wstop := make(chan struct{})
	wdone := make(chan struct{})
	store.mu.Lock()
	store.pruneStop = stop
	store.writeQueue = queue
	store.writerStop = wstop
	store.writerDone = wdone
	store.mu.Unlock()
	go store.pruneLoop(p, stop)
	go func() {
		defer close(wdone)
		store.writeLoop(p, queue, wstop)
	}()
}

// writeLoop is the single background writer. One goroutine owns every insert,
// so two tool calls in flight never contend for SQLite's write lock on the
// request path (discovery doc §3.2).
func (s *thoughtSignatureStore) writeLoop(p thoughtSignaturePersister, queue <-chan asyncWriteTask, stop <-chan struct{}) {
	writes := 0
	for {
		select {
		case task := <-queue:
			if s.runWrite(p, task) {
				writes++
			}
			if writes >= trimEveryWrites {
				writes = 0
				s.trimPersisted(p)
			}
		case <-stop:
			// Graceful shutdown: whatever is already queued still lands, so a
			// restart does not lose signatures that were accepted (§3.4).
			for {
				select {
				case task := <-queue:
					s.runWrite(p, task)
				default:
					return
				}
			}
		}
	}
}

// trimPersisted enforces the row ceiling from the writer, so the table stops at
// its budget during a burst rather than at the next hourly tick.
func (s *thoughtSignatureStore) trimPersisted(p thoughtSignaturePersister) {
	if _, err := p.TrimThoughtSignatures(context.Background(), maxPersistentSignatures); err != nil {
		log.Warn("translator", "thought signature trim skipped", "error", err)
	}
}

// runWrite saves one row and reports whether a row was actually attempted (a
// barrier writes nothing, so it must not count toward the next trim).
func (s *thoughtSignatureStore) runWrite(p thoughtSignaturePersister, task asyncWriteTask) bool {
	if task.done != nil {
		close(task.done)
		return false
	}
	if err := p.SaveThoughtSignature(context.Background(), task.key, task.signature, task.family, task.expiresAt); err != nil {
		log.Warn("translator", "thought signature persist skipped", "error", err)
	}
	return true
}

// pruneLoop deletes expired rows on the same cadence as the memory TTL.
func (s *thoughtSignatureStore) pruneLoop(p thoughtSignaturePersister, stop <-chan struct{}) {
	ticker := time.NewTicker(memoryTTL)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			if _, err := p.PruneThoughtSignatures(context.Background(), now); err != nil {
				log.Warn("translator", "thought signature prune skipped", "error", err)
			}
			// Expiry alone is not a bound: a live gateway keeps writing. Trim
			// to the L2 capacity so the table stops at the size the contract
			// names instead of at whatever disk fills to.
			if _, err := p.TrimThoughtSignatures(context.Background(), maxPersistentSignatures); err != nil {
				log.Warn("translator", "thought signature trim skipped", "error", err)
			}
		}
	}
}

// persistWrite is gone: writes now travel through enqueueSignatureWrites and
// the single background writer.

// enqueueSignatureWrites queues one row per key. Never blocks: a full queue
// drops the row (§3.3). L1 already holds the entry, so the live session keeps
// working and only restart recovery loses that signature.
func (s *thoughtSignatureStore) enqueueSignatureWrites(queue chan asyncWriteTask, keys []string, signature, family string, expiresAt time.Time) {
	for _, k := range keys {
		task := asyncWriteTask{key: k, signature: signature, family: family, expiresAt: expiresAt}
		select {
		case queue <- task:
		default:
			s.dropped.Add(1)
		}
	}
}

// drainSignatureWrites blocks until every write queued before this call has
// been attempted. Tests use it instead of sleeping; production never needs it.
func (s *thoughtSignatureStore) drainSignatureWrites() {
	s.mu.Lock()
	queue := s.writeQueue
	s.mu.Unlock()
	if queue == nil {
		return
	}
	done := make(chan struct{})
	select {
	case queue <- asyncWriteTask{done: done}:
		<-done
	default:
		// A full queue means the writer is already behind; a barrier that
		// cannot be queued cannot promise ordering, and blocking here would
		// defeat the fail-open contract it exists to test.
	}
}

func (s *thoughtSignatureStore) pruneExpiredLocked(now time.Time) {
	for k, v := range s.entries {
		if now.After(v.expiresAt) {
			delete(s.entries, k)
		}
	}

	// Bound max capacity
	if len(s.entries) > maxMemorySignatures {
		newOrder := make([]string, 0, len(s.entries))
		for _, k := range s.order {
			if _, ok := s.entries[k]; ok {
				newOrder = append(newOrder, k)
			}
		}
		for len(newOrder) > maxMemorySignatures {
			oldest := newOrder[0]
			newOrder = newOrder[1:]
			delete(s.entries, oldest)
		}
		s.order = newOrder
	}
}

// StoreGeminiThoughtSignature stores a thought signature for a tool_call_id with optional session namespace and model family.
func StoreGeminiThoughtSignature(toolCallID, signature, sessionID string, model ...string) {
	if toolCallID == "" || signature == "" {
		return
	}

	var family string
	if len(model) > 0 && model[0] != "" {
		family = SignatureFamily(model[0])
	}
	now := time.Now()
	// Two clocks, deliberately: the in-memory entry is the hot path and keeps
	// the short TTL, while the row that outlives the process is written with
	// the disk TTL. Using one value for both made "survives a restart" mean
	// "survives a restart within the hour", which is not what persisting is
	// for.
	memExp := now.Add(memoryTTL)
	diskExp := now.Add(persistentTTL)

	// Clean toolCallID if it contains __ts__ suffix
	cleanID := toolCallID
	if idx := strings.LastIndex(toolCallID, "__ts__"); idx != -1 {
		cleanID = toolCallID[:idx]
	}

	globalThoughtSigStore.mu.Lock()

	globalThoughtSigStore.pruneExpiredLocked(now)

	keys := []string{toolCallID}
	if cleanID != toolCallID {
		keys = append(keys, cleanID)
	}

	if sessionID != "" {
		keys = append(keys, sessionID+":"+toolCallID)
		if cleanID != toolCallID {
			keys = append(keys, sessionID+":"+cleanID)
		}
	}

	for _, k := range keys {
		if _, exists := globalThoughtSigStore.entries[k]; !exists {
			globalThoughtSigStore.order = append(globalThoughtSigStore.order, k)
		}
		globalThoughtSigStore.entries[k] = signatureEntry{
			signature: signature,
			family:    family,
			expiresAt: memExp,
		}
	}
	persister := globalThoughtSigStore.persist
	queue := globalThoughtSigStore.writeQueue
	globalThoughtSigStore.mu.Unlock()

	if persister == nil || queue == nil {
		return
	}
	globalThoughtSigStore.enqueueSignatureWrites(queue, keys, signature, family, diskExp)
}

// GetGeminiThoughtSignature retrieves a thought signature by tool_call_id,
// scoped to the conversation that produced it and matched against the model
// family.
//
// A non-empty sessionID makes the lookup session-only: the bare toolCallID key
// is shared by every conversation on the account, so falling back to it is how
// one conversation replayed another's signature and the provider answered
// 400 INVALID_ARGUMENT "Corrupted thought signature". A request with no session
// (a client that sends no session header) keeps the bare-key lookup, which is
// also the key such a request writes.
func GetGeminiThoughtSignature(toolCallID, sessionID string, model ...string) string {
	if toolCallID == "" {
		return ""
	}

	var targetFamily string
	if len(model) > 0 && model[0] != "" {
		targetFamily = SignatureFamily(model[0])
	}
	cleanID := toolCallID
	if idx := strings.LastIndex(toolCallID, "__ts__"); idx != -1 {
		cleanID = toolCallID[:idx]
	}

	candidates := signatureLookupKeys(toolCallID, cleanID, sessionID)
	now := time.Now()

	globalThoughtSigStore.mu.RLock()
	for _, k := range candidates {
		entry, ok := globalThoughtSigStore.entries[k]
		if !ok || !now.Before(entry.expiresAt) || !isFamilyCompatible(entry.family, targetFamily) {
			continue
		}
		globalThoughtSigStore.mu.RUnlock()
		return entry.signature
	}
	persister := globalThoughtSigStore.persist
	globalThoughtSigStore.mu.RUnlock()

	if persister == nil {
		return ""
	}

	// Memory miss with persistence attached: the row may have been evicted from
	// the hot map (2000-entry cap) or written by a previous process whose boot
	// hydrate ran before this key existed. Read through and repopulate.
	for _, k := range candidates {
		row, err := persister.GetThoughtSignature(context.Background(), k)
		if err != nil {
			log.Warn("translator", "thought signature lookup skipped", "error", err)
			return ""
		}
		if row == nil || !now.Before(row.ExpiresAt) || !isFamilyCompatible(row.Family, targetFamily) {
			continue
		}

		globalThoughtSigStore.mu.Lock()
		if _, exists := globalThoughtSigStore.entries[k]; !exists {
			globalThoughtSigStore.order = append(globalThoughtSigStore.order, k)
		}
		globalThoughtSigStore.entries[k] = signatureEntry{
			signature: row.Signature,
			family:    row.Family,
			expiresAt: l1Expiry(time.Now(), row.ExpiresAt),
		}
		// A read-through repopulates the same bounded map a write does, so it
		// prunes the same way: without this, 2 500 lookups left 2 500 entries
		// against a 2 000-entry cap, because the map was only trimmed when a
		// write happened to come along. Gated on the cap so the common miss
		// does not pay a whole-map scan.
		if len(globalThoughtSigStore.entries) > maxMemorySignatures {
			globalThoughtSigStore.pruneExpiredLocked(time.Now())
		}
		globalThoughtSigStore.mu.Unlock()
		return row.Signature
	}
	return ""
}

// signatureLookupKeys lists the store keys a lookup may read, most specific
// first. A session-scoped request may only read its own namespace; a request
// with no session reads the bare keys, which is what it writes.
func signatureLookupKeys(toolCallID, cleanID, sessionID string) []string {
	if sessionID == "" {
		keys := []string{toolCallID}
		if cleanID != toolCallID {
			keys = append(keys, cleanID)
		}
		return keys
	}
	keys := []string{sessionID + ":" + toolCallID}
	if cleanID != toolCallID {
		keys = append(keys, sessionID+":"+cleanID)
	}
	return keys
}

// ClearGeminiThoughtSignatures resets the store (useful for tests).
func ClearGeminiThoughtSignatures() {
	globalThoughtSigStore.mu.Lock()
	defer globalThoughtSigStore.mu.Unlock()
	clear(globalThoughtSigStore.entries)
	globalThoughtSigStore.order = globalThoughtSigStore.order[:0]
}
