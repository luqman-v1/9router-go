package headroom

import (
	"sync"
	"testing"
	"time"
)

func msg(role, content string) map[string]any {
	return map[string]any{"role": role, "content": content}
}

func TestMemoryStoreLookupRoundTrip(t *testing.T) {
	m := NewMemoryStore(10, 10, time.Hour)
	prefix := []any{msg("user", "hello"), msg("assistant", "hi")}
	result := []any{msg("user", "hello"), msg("assistant", "hi")}

	m.Store("sess-1", prefix, result, 12)

	got, ok := m.Lookup("sess-1", prefix)
	if !ok {
		t.Fatal("Lookup miss after Store")
	}
	if got.PrefixHash != PrefixHash(prefix) {
		t.Errorf("hash mismatch: got %q want %q", got.PrefixHash, PrefixHash(prefix))
	}
	if got.TokensSaved != 12 {
		t.Errorf("TokensSaved = %d, want 12", got.TokensSaved)
	}
	if len(got.CompressedMessages) != len(result) {
		t.Errorf("CompressedMessages length = %d, want %d", len(got.CompressedMessages), len(result))
	}
}

func TestMemoryPrefixMatch(t *testing.T) {
	m := NewMemoryStore(10, 10, time.Hour)
	prefix := []any{msg("user", "hello"), msg("assistant", "hi")}
	m.Store("sess-1", prefix, prefix, 7)

	// Query has the stored two messages plus one more; the stored prefix
	// must still match.
	longer := []any{msg("user", "hello"), msg("assistant", "hi"), msg("user", "more")}
	got, ok := m.Lookup("sess-1", longer)
	if !ok {
		t.Fatal("prefix match miss for longer query")
	}
	if got.Length != 2 {
		t.Errorf("matched entry Length = %d, want 2", got.Length)
	}
}

func TestMemoryPrefixNonMatch(t *testing.T) {
	m := NewMemoryStore(10, 10, time.Hour)
	m.Store("sess-1", []any{msg("user", "hello")}, []any{msg("user", "hello")}, 3)

	if _, ok := m.Lookup("sess-1", []any{msg("user", "different")}); ok {
		t.Fatal("Lookup matched a different prefix")
	}
	if _, ok := m.Lookup("sess-1", []any{}); ok {
		t.Fatal("Lookup matched an empty query")
	}
	if _, ok := m.Lookup("unknown-sess", []any{msg("user", "hello")}); ok {
		t.Fatal("Lookup matched an unknown session")
	}
}

func TestMemoryEvictExpiredTTL(t *testing.T) {
	m := NewMemoryStore(10, 10, 2*time.Millisecond)
	m.Store("sess-1", []any{msg("user", "a")}, []any{msg("user", "a")}, 1)
	m.Store("sess-2", []any{msg("user", "b")}, []any{msg("user", "b")}, 1)

	time.Sleep(5 * time.Millisecond)

	if removed := m.EvictExpired(); removed != 2 {
		t.Errorf("EvictExpired removed %d, want 2", removed)
	}
	if _, ok := m.Lookup("sess-1", []any{msg("user", "a")}); ok {
		t.Fatal("Lookup hit an expired session after EvictExpired")
	}
}

func TestMemoryEvictExpiredLazy(t *testing.T) {
	m := NewMemoryStore(10, 10, 2*time.Millisecond)
	prefix := []any{msg("user", "a")}
	m.Store("sess-1", prefix, prefix, 1)

	time.Sleep(5 * time.Millisecond)

	// Accesses the expired session drop it lazily without an explicit sweep.
	if _, ok := m.Lookup("sess-1", prefix); ok {
		t.Fatal("Lookup hit a lazily-expired session")
	}
	if removed := m.EvictExpired(); removed != 0 {
		t.Errorf("EvictExpired removed %d after lazy eviction, want 0", removed)
	}
}

func TestMemoryBoundedSessionEviction(t *testing.T) {
	m := NewMemoryStore(2, 10, time.Hour)

	m.Store("sess-1", []any{msg("user", "a")}, []any{msg("user", "a")}, 1)
	m.Store("sess-2", []any{msg("user", "b")}, []any{msg("user", "b")}, 1)

	// Adding a third session evicts the least recently used (sess-1).
	m.Store("sess-3", []any{msg("user", "c")}, []any{msg("user", "c")}, 1)

	if _, ok := m.Lookup("sess-1", []any{msg("user", "a")}); ok {
		t.Fatal("sess-1 survived session-cap eviction")
	}
	lookup := map[string]string{"sess-2": "b", "sess-3": "c"}
	for id, content := range lookup {
		if _, ok := m.Lookup(id, []any{msg("user", content)}); !ok {
			t.Errorf("%s was evicted, want kept", id)
		}
	}
}

func TestMemoryBoundedEntriesPerSession(t *testing.T) {
	const maxPerSession = 3
	m := NewMemoryStore(10, maxPerSession, time.Hour)

	for i := range 5 {
		p := []any{msg("user", "m")}
		// Each store adds a distinct prefix by appending a unique message.
		p = append(p, msg("assistant", string(rune('0'+i))))
		m.Store("sess-1", p, p, i)
	}

	if len(m.sessions["sess-1"].entries) > maxPerSession {
		t.Errorf("session holds %d entries, want <= %d", len(m.sessions["sess-1"].entries), maxPerSession)
	}
}

func TestMemoryConcurrentAccess(t *testing.T) {
	m := NewMemoryStore(50, 20, time.Hour)

	var wg sync.WaitGroup
	const workers = 16
	const opsPerWorker = 50
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			sess := string(rune('a' + w))
			for i := range opsPerWorker {
				prefix := []any{msg("user", sess), msg("assistant", string(rune('0'+i%10)))}
				m.Store(sess, prefix, prefix, i)
				m.Lookup(sess, prefix)
				m.EvictExpired()
			}
		}(w)
	}
	wg.Wait()

	// After the storm, a stored session must still be readable.
	for w := range workers {
		sess := string(rune('a' + w))
		prefix := []any{msg("user", sess), msg("assistant", "0")}
		if _, ok := m.Lookup(sess, prefix); !ok {
			t.Errorf("session %q lost under concurrency", sess)
		}
	}
}

func TestPrefixHashDeterminism(t *testing.T) {
	messages := []any{
		msg("user", "what is the meaning of life, the universe, and everything?"),
		msg("assistant", "42"),
		msg("tool", "42"),
	}
	a := PrefixHash(messages)
	b := PrefixHash(messages)
	if a != b {
		t.Errorf("PrefixHash not deterministic: %q != %q", a, b)
	}
	if a == "" {
		t.Fatal("PrefixHash returned empty string")
	}

	// Different inputs must produce different hashes.
	if a == PrefixHash([]any{msg("user", "what is the meaning of life?")}) {
		t.Error("different inputs produced the same hash")
	}
}

func TestMemoryStoreDefaults(t *testing.T) {
	m := NewMemoryStore(0, 0, 0)
	if m.maxSessions != defaultMaxSessions {
		t.Errorf("maxSessions = %d, want %d", m.maxSessions, defaultMaxSessions)
	}
	if m.maxPerSession != defaultMaxPerSession {
		t.Errorf("maxPerSession = %d, want %d", m.maxPerSession, defaultMaxPerSession)
	}
	if m.ttl != defaultTTL {
		t.Errorf("ttl = %v, want %v", m.ttl, defaultTTL)
	}
}
