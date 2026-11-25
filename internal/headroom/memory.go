// Session-level memory store for the builtin engine.
//
// The cache records the compression decision made on a conversation prefix
// (the leading messages of a request) so that a subsequent request that reuses
// the same prefix can replay that decision instead of re-compressing the
// already-seen history. It is a runtime cache, not a durable store: it lives in
// memory, is bounded per session and in total, and evicts expired sessions
// lazily on access.
package headroom

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	json "encoding/json/v2"
)

// Default memory-store bounds, used when NewMemoryStore is given non-positive
// limits. The session cap is the same order of magnitude as the number of
// concurrent conversations a gateway typically holds; the per-session cap
// covers the length of a long agent run; the TTL matches the default session
// lifetime of the dashboard.
const (
	defaultMaxSessions   = 500
	defaultMaxPerSession = 50
	defaultTTL           = 24 * time.Hour
)

// MemoryEntry captures the compression decision made on one prefix. The
// message count is kept alongside the hash so a lookup can compare the stored
// prefix against the matching head of a longer request.
type MemoryEntry struct {
	// PrefixHash is the canonical SHA-256 of the prefix messages (role+
	// content).
	PrefixHash string
	// Length is the number of messages in the prefix. Entries in a session
	// are ordered by this value.
	Length int
	// CompressedMessages are the compressed form of the prefix messages.
	CompressedMessages []any
	// TokensSaved is the token saving reported for this prefix.
	TokensSaved int
	// CompressedAt is when the decision was recorded.
	CompressedAt time.Time
}

// sessionMemory is one session's cached decisions plus its access bookkeeping.
// lastUsed is what TTL eviction keys on (wall-clock idle time). seq is a
// monotonic access stamp that orders sessions for LRU eviction: wall-clock time
// has finite granularity, so several sessions touched within one clock tick
// would tie on lastUsed and let Go's random map iteration pick the eviction
// victim. seq breaks those ties deterministically.
type sessionMemory struct {
	entries  []MemoryEntry
	lastUsed time.Time
	seq      uint64
}

// MemoryStore caches compression decisions per session. It is safe for
// concurrent use.
type MemoryStore struct {
	mu            sync.RWMutex
	sessions      map[string]*sessionMemory
	maxSessions   int
	maxPerSession int
	ttl           time.Duration
	// accessSeq increments on every Store/Lookup and stamps the touched
	// session, giving evictLRUSessionLocked a total order even when many
	// sessions are touched in the same clock tick.
	accessSeq uint64
}

// NewMemoryStore returns a memory store with the given bounds. Non-positive
// limits fall back to the defaults, and a non-positive TTL falls back to the
// default.
func NewMemoryStore(maxSessions, maxPerSession int, ttl time.Duration) *MemoryStore {
	if maxSessions <= 0 {
		maxSessions = defaultMaxSessions
	}
	if maxPerSession <= 0 {
		maxPerSession = defaultMaxPerSession
	}
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &MemoryStore{
		sessions:      make(map[string]*sessionMemory),
		maxSessions:   maxSessions,
		maxPerSession: maxPerSession,
		ttl:           ttl,
	}
}

// PrefixHash computes a deterministic SHA-256 over the canonical serialization
// of messages, so the same input always yields the same hash across runs.
// Map keys are emitted in sorted order, because the underlying JSON encoder
// does not guarantee a stable map iteration order.
func PrefixHash(messages []any) string {
	sum := sha256.Sum256(canonicalJSON(messages))
	return hex.EncodeToString(sum[:])
}

// canonicalJSON returns a deterministic byte encoding of v. Object keys are
// emitted in sorted order and values are recursed, so equal values always
// encode identically regardless of map iteration order.
func canonicalJSON(v any) []byte {
	switch t := v.(type) {
	case nil:
		return []byte("null")
	case bool:
		if t {
			return []byte("true")
		}
		return []byte("false")
	case string:
		b, _ := json.Marshal(t)
		return b
	case float64:
		b, _ := json.Marshal(t)
		return b
	case float32:
		b, _ := json.Marshal(float64(t))
		return b
	case int:
		b, _ := json.Marshal(int64(t))
		return b
	case int64:
		b, _ := json.Marshal(t)
		return b
	case []any:
		out := make([]byte, 0, 16)
		out = append(out, '[')
		for i, el := range t {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, canonicalJSON(el)...)
		}
		out = append(out, ']')
		return out
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]byte, 0, 16)
		out = append(out, '{')
		for i, k := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, canonicalJSON(k)...)
			out = append(out, ':')
			out = append(out, canonicalJSON(t[k])...)
		}
		out = append(out, '}')
		return out
	default:
		// Numbers that did not land in the concrete cases above, and any
		// other plain value, marshal deterministically.
		b, _ := json.Marshal(t)
		return b
	}
}

// Store records the compression decision for a prefix. If the session already
// holds a decision for the same prefix, it is replaced; otherwise it is added,
// bounded by the per-session and total session caps.
func (m *MemoryStore) Store(sessionID string, prefix []any, result []any, tokensSaved int) {
	entry := MemoryEntry{
		PrefixHash:         PrefixHash(prefix),
		Length:             len(prefix),
		CompressedMessages: cloneMessages(result),
		TokensSaved:        tokensSaved,
		CompressedAt:       time.Now(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	sm := m.sessions[sessionID]
	if sm == nil {
		if len(m.sessions) >= m.maxSessions {
			m.evictLRUSessionLocked()
		}
		sm = &sessionMemory{}
		m.sessions[sessionID] = sm
	} else if m.expired(sm) {
		// The session outlived its TTL; drop it and start fresh.
		delete(m.sessions, sessionID)
		sm = &sessionMemory{}
		m.sessions[sessionID] = sm
	}
	m.touchLocked(sm)

	for i := range sm.entries {
		if sm.entries[i].PrefixHash == entry.PrefixHash {
			sm.entries[i] = entry
			return
		}
	}

	if len(sm.entries) >= m.maxPerSession {
		// Drop the entry at the front. Entries are kept sorted by prefix
		// length, and the front is the shortest, so this bounds memory
		// without touching the longest prefixes that are most likely to
		// still be reused.
		sm.entries = sm.entries[1:]
	}
	sm.entries = append(sm.entries, entry)
	// A stable sort keeps entries of equal length in insertion order, so the
	// eviction above always removes the oldest entry, not an arbitrary one.
	sort.SliceStable(sm.entries, func(i, j int) bool {
		return sm.entries[i].Length < sm.entries[j].Length
	})
}

// Lookup returns the cached decision whose prefix matches the head of the
// request messages. The stored prefix matches when the request's leading
// messages hash to the stored hash — so a query longer than the stored prefix
// still matches. The longest matching prefix wins. A miss returns nil, false;
// the caller then compresses from scratch (fail-open).
func (m *MemoryStore) Lookup(sessionID string, prefix []any) (*MemoryEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sm := m.sessions[sessionID]
	if sm == nil {
		return nil, false
	}
	if m.expired(sm) {
		delete(m.sessions, sessionID)
		return nil, false
	}
	m.touchLocked(sm)

	for i := len(sm.entries) - 1; i >= 0; i-- {
		e := sm.entries[i]
		if e.Length > len(prefix) {
			continue
		}
		if PrefixHash(prefix[:e.Length]) == e.PrefixHash {
			cp := e
			return &cp, true
		}
	}
	return nil, false
}

// EvictExpired removes sessions that have not been touched within the TTL and
// returns how many were removed.
func (m *MemoryStore) EvictExpired() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := 0
	for id, sm := range m.sessions {
		if m.expired(sm) {
			delete(m.sessions, id)
			removed++
		}
	}
	return removed
}

// expired reports whether a session has been idle longer than the TTL.
func (m *MemoryStore) expired(sm *sessionMemory) bool {
	return time.Since(sm.lastUsed) > m.ttl
}

// touchLocked stamps a session as just used. It must be called with m.mu held.
// It advances the monotonic access stamp and refreshes the wall-clock idle
// timer, so the session is both kept alive for the TTL and ordered after every
// session touched earlier for LRU eviction.
func (m *MemoryStore) touchLocked(sm *sessionMemory) {
	m.accessSeq++
	sm.seq = m.accessSeq
	sm.lastUsed = time.Now()
}

// evictLRUSessionLocked removes the least recently used session. It is called
// with the lock held when a new session would exceed maxSessions.
//
// Ordering keys on the monotonic access stamp seq, not on wall-clock lastUsed:
// sessions touched within the same clock tick share a lastUsed value, and
// lastUsed.Before would then be false for every pair, leaving the victim to
// Go's random map iteration order. seq is a total order, so the eviction is
// deterministic regardless of timing or map ordering.
func (m *MemoryStore) evictLRUSessionLocked() {
	var oldestID string
	var oldestSeq uint64
	first := true
	for id, sm := range m.sessions {
		if first || sm.seq < oldestSeq {
			oldestID = id
			oldestSeq = sm.seq
			first = false
		}
	}
	if !first {
		delete(m.sessions, oldestID)
	}
}

// cloneMessages copies the message slice so a stored entry does not alias the
// caller's slice, which the caller is free to reuse after Store returns.
func cloneMessages(messages []any) []any {
	if messages == nil {
		return nil
	}
	out := make([]any, len(messages))
	copy(out, messages)
	return out
}
