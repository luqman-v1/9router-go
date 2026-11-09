package chat

import (
	"testing"
	"time"
)

func TestSessionAffinityStore_BindGetEvict(t *testing.T) {
	store := NewSessionAffinityStore(100, 1*time.Hour)

	// Miss on non-existent
	if conn, ok := store.Get("codex", "gpt-5.6", "sess-1"); ok || conn != "" {
		t.Fatalf("expected miss, got %q", conn)
	}

	// Bind and Get
	store.Bind("codex", "gpt-5.6", "sess-1", "conn-a")
	if conn, ok := store.Get("codex", "gpt-5.6", "sess-1"); !ok || conn != "conn-a" {
		t.Fatalf("expected conn-a, got %q (ok=%v)", conn, ok)
	}

	// Multi-model separation within same session (e.g., fast model vs main model)
	store.Bind("codex", "gpt-5-mini", "sess-1", "conn-b")
	if conn, ok := store.Get("codex", "gpt-5-mini", "sess-1"); !ok || conn != "conn-b" {
		t.Fatalf("expected conn-b for mini, got %q", conn)
	}
	if conn, ok := store.Get("codex", "gpt-5.6", "sess-1"); !ok || conn != "conn-a" {
		t.Fatalf("expected conn-a for 5.6, got %q", conn)
	}

	// Evict
	store.Evict("codex", "gpt-5.6", "sess-1")
	if conn, ok := store.Get("codex", "gpt-5.6", "sess-1"); ok || conn != "" {
		t.Fatalf("expected empty after evict, got %q", conn)
	}
}

func TestSessionAffinityStore_EvictConnection(t *testing.T) {
	store := NewSessionAffinityStore(100, 1*time.Hour)
	store.Bind("codex", "gpt-5.6", "sess-1", "conn-dead")
	store.Bind("codex", "gpt-5.6", "sess-2", "conn-dead")
	store.Bind("codex", "gpt-5.6", "sess-3", "conn-alive")

	store.EvictConnection("conn-dead")
	if _, ok := store.Get("codex", "gpt-5.6", "sess-1"); ok {
		t.Error("sess-1 should be evicted")
	}
	if _, ok := store.Get("codex", "gpt-5.6", "sess-2"); ok {
		t.Error("sess-2 should be evicted")
	}
	if conn, ok := store.Get("codex", "gpt-5.6", "sess-3"); !ok || conn != "conn-alive" {
		t.Errorf("sess-3 should survive, got %q", conn)
	}
}

func TestSessionAffinityStore_TTLExpirationAndCapacity(t *testing.T) {
	store := NewSessionAffinityStore(2, 50*time.Millisecond)
	store.Bind("codex", "m1", "s1", "c1")
	// slight tick to differentiate timestamps
	time.Sleep(5 * time.Millisecond)
	store.Bind("codex", "m1", "s2", "c2")

	// Capacity eviction: adding 3rd entry evicts oldest (s1)
	time.Sleep(5 * time.Millisecond)
	store.Bind("codex", "m1", "s3", "c3")
	if _, ok := store.Get("codex", "m1", "s1"); ok {
		t.Error("s1 should have been evicted when exceeding capacity 2")
	}
	if conn, ok := store.Get("codex", "m1", "s2"); !ok || conn != "c2" {
		t.Errorf("s2 should still exist, got %q (ok=%v)", conn, ok)
	}

	// TTL expiration
	time.Sleep(60 * time.Millisecond)
	if _, ok := store.Get("codex", "m1", "s3"); ok {
		t.Error("s3 should expire after TTL")
	}
}

func TestNormalizeModelFamily(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"gemini-2.5-pro", "pro"},
		{"gemini-2.5-flash", "flash"},
		{"gemini-3-pro-preview", "pro"},
		{"google/gemini-1.5-flash-latest", "flash"},
		{"claude-3-5-sonnet-20241022", "sonnet"},
		{"anthropic/claude-3-5-haiku", "haiku"},
		{"claude-3-opus", "opus"},
		{"gpt-4o-mini", "mini"},
		{"o3-mini", "mini"},
		{"gpt-4o", "gpt-4o"},
		{"deepseek-reasoner", "deepseek-reasoner"},
		{"", "default"},
	}

	for _, tc := range cases {
		got := normalizeModelFamily(tc.model)
		if got != tc.want {
			t.Errorf("normalizeModelFamily(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

func TestHashSessionID_BoundedKey(t *testing.T) {
	shortID := "sess-normal-123"
	if hashSessionID(shortID) != shortID {
		t.Errorf("short session id should be kept verbatim, got %q", hashSessionID(shortID))
	}

	// 1000-char session ID must be truncated/hashed to bounded length (64 hex chars)
	longID := "sess-"
	for len(longID) < 1000 {
		longID += "abcdefghijklmnopqrstuvwxyz0123456789"
	}
	hashed := hashSessionID(longID)
	if len(hashed) != 64 {
		t.Errorf("long session id hash length = %d, want 64", len(hashed))
	}

	// affinityKey with long session ID must remain bounded
	key := affinityKey("codex", "gpt-4o", longID)
	if len(key) > 120 {
		t.Errorf("affinityKey length = %d too long, want <= 120", len(key))
	}
}

func TestIsAffinityEligibleStrategy(t *testing.T) {
	if !isAffinityEligibleStrategy("round-robin") {
		t.Error("round-robin should be eligible")
	}
	if !isAffinityEligibleStrategy("sticky") {
		t.Error("sticky should be eligible")
	}
	if isAffinityEligibleStrategy("random") {
		t.Error("random should NOT be eligible (must preserve per-request randomness)")
	}
	if isAffinityEligibleStrategy("none") {
		t.Error("none should NOT be eligible")
	}
	if isAffinityEligibleStrategy("") {
		t.Error("empty strategy should NOT be eligible")
	}
}
