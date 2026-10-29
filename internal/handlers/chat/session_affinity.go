package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// SessionAffinityStore maintains thread-safe mappings of client sessions to upstream
// connection accounts. This preserves prompt caching across multi-turn conversations
// while balancing multiple distinct sessions across available accounts.
type SessionAffinityStore struct {
	mu         sync.Mutex
	entries    map[string]*sessionBinding
	maxEntries int
	ttl        time.Duration
}

type sessionBinding struct {
	key          string
	provider     string
	modelFamily  string
	sessionID    string
	connectionID string
	lastSeenAt   time.Time
}

// NewSessionAffinityStore creates a session affinity store bounded by maxEntries and inactivity TTL.
func NewSessionAffinityStore(maxEntries int, ttl time.Duration) *SessionAffinityStore {
	if maxEntries <= 0 {
		maxEntries = 10000
	}
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	return &SessionAffinityStore{
		entries:    make(map[string]*sessionBinding),
		maxEntries: maxEntries,
		ttl:        ttl,
	}
}

func hasModelToken(m, token string) bool {
	for _, part := range strings.FieldsFunc(m, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/' || r == ':'
	}) {
		if part == token {
			return true
		}
	}
	return false
}

// normalizeModelFamily groups models into families so multi-model sessions (e.g., Sonnet vs Haiku)
// do not cause eviction thrashing when accounts have different model capabilities.
// Uses token-boundary matching so substrings like "gemini" do not falsely match "mini".
func normalizeModelFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.Index(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	if m == "" {
		return "default"
	}
	if hasModelToken(m, "haiku") {
		return "haiku"
	}
	if hasModelToken(m, "opus") {
		return "opus"
	}
	if hasModelToken(m, "sonnet") {
		return "sonnet"
	}
	if hasModelToken(m, "flash") {
		return "flash"
	}
	if hasModelToken(m, "pro") {
		return "pro"
	}
	if hasModelToken(m, "mini") {
		return "mini"
	}
	return m
}

// hashSessionID truncates and hashes client-supplied session identifiers to bound
// map key memory on constrained environments (such as 1-vCPU targets).
func hashSessionID(sessionID string) string {
	s := strings.TrimSpace(sessionID)
	if len(s) > 64 {
		h := sha256.Sum256([]byte(s))
		return hex.EncodeToString(h[:])
	}
	return s
}

// isAffinityEligibleStrategy reports whether a rotation strategy participates in session affinity.
// Session affinity is designed for multi-account load balancing ("round-robin" and "sticky").
// "random" is explicitly excluded so callers desiring per-request randomness do not get pinned.
func isAffinityEligibleStrategy(strategy string) bool {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "round-robin", "roundrobin", "sticky":
		return true
	default:
		return false
	}
}

func affinityKey(provider, model, sessionID string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + ":" + normalizeModelFamily(model) + ":" + hashSessionID(sessionID)
}

// Get returns the pinned connection ID for the given provider, model, and session ID if still active.
func (s *SessionAffinityStore) Get(provider, model, sessionID string) (string, bool) {
	if s == nil || sessionID == "" {
		return "", false
	}
	key := affinityKey(provider, model, sessionID)

	s.mu.Lock()
	defer s.mu.Unlock()

	b, ok := s.entries[key]
	if !ok {
		return "", false
	}
	now := time.Now()
	if now.Sub(b.lastSeenAt) > s.ttl {
		delete(s.entries, key)
		return "", false
	}
	b.lastSeenAt = now
	return b.connectionID, true
}

// Bind assigns a session to a connection ID, evicting the oldest entry if capacity is exceeded.
func (s *SessionAffinityStore) Bind(provider, model, sessionID, connectionID string) {
	if s == nil || sessionID == "" || connectionID == "" {
		return
	}
	key := affinityKey(provider, model, sessionID)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	// If entry already exists, update and touch
	if b, exists := s.entries[key]; exists {
		b.connectionID = connectionID
		b.lastSeenAt = now
		return
	}

	// Capacity enforcement: evict expired or oldest inactive entry
	if len(s.entries) >= s.maxEntries {
		var oldestKey string
		var oldestTime time.Time
		for k, v := range s.entries {
			if now.Sub(v.lastSeenAt) > s.ttl {
				oldestKey = k
				break
			}
			if oldestKey == "" || v.lastSeenAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.lastSeenAt
			}
		}
		if oldestKey != "" {
			delete(s.entries, oldestKey)
		}
	}

	s.entries[key] = &sessionBinding{
		key:          key,
		provider:     provider,
		modelFamily:  normalizeModelFamily(model),
		sessionID:    hashSessionID(sessionID),
		connectionID: connectionID,
		lastSeenAt:   now,
	}
}

// Evict removes the affinity binding for the specified session.
func (s *SessionAffinityStore) Evict(provider, model, sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	key := affinityKey(provider, model, sessionID)
	s.mu.Lock()
	delete(s.entries, key)
	s.mu.Unlock()
}

// EvictConnection removes all session bindings pointing to the given connection ID
// (called when an account encounters 429 rate limit or enters cooldown).
func (s *SessionAffinityStore) EvictConnection(connectionID string) {
	if s == nil || connectionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range s.entries {
		if v.connectionID == connectionID {
			delete(s.entries, k)
		}
	}
}
