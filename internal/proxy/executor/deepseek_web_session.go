package executor

import (
	"strings"
	"sync"
	"time"
)

// Session persistence: one upstream chat session per client conversation.
//
// /api/v0/chat/completion takes a single prompt string and a
// `parent_message_id`, and the site keeps the conversation on its side. A
// gateway that opens a fresh session and re-sends the whole transcript on every
// turn reproduces nothing a browser does: every message in the upstream session
// then embeds every previous turn, which grows quadratically and is the signal
// the sibling implementation (snake-aabb-wtf/deepseek-web2api-free) names as the
// main cause of account-level mutes.
//
// So a conversation is pinned to one upstream session and each turn sends only
// the part of the prompt the previous turn did not already send. Three
// conditions have to hold, and each failure falls back to the plain behaviour
// rather than guessing:
//   - The client must name its conversation. A client session id is the
//     strongest name; without one, the conversation's opening message stands
//     in for it, so a client that never sends `x-session-id` still pins.
//     Two distinct conversations can open with the same message, so the
//     fallback key is weaker, and the entry's idle TTL plus the tail check
//     bound the harm: a stale chain ages out, and a divergent prompt opens a
//     fresh session.
//   - The turn must reuse a session no other turn is holding. Two turns in one
//     conversation are a fork of the parent chain, so the second one gets its
//     own session and does not touch the pinned one.
//   - The new prompt must extend what was already sent. A diverging prompt (a
//     client that rewrote its history, or a rolling window that dropped the
//     oldest turns) cannot be expressed as a tail, so the session is discarded
//     and a fresh one carries the whole prompt.

// deepSeekSessionEntry is one pinned conversation.
type deepSeekSessionEntry struct {
	sessionID string

	// sentPrompt is the full prompt handed to the upstream last turn. The next
	// turn's tail is the part of its own prompt that follows this.
	sentPrompt string

	// parentMessageID is the upstream's own id for the answer it produced, and
	// the parent the next turn threads on.
	parentMessageID int64

	// busy is set while a turn holds the session. A second turn in the same
	// conversation then starts its own session instead of forking this chain.
	busy bool

	// lastUsed is when the entry was last claimed or committed. Entries idle
	// past deepSeekSessionIdleTTL are dropped, so a fallback key that two
	// conversations happen to share stops leaking context once one of them
	// goes quiet.
	lastUsed time.Time
}

// deepSeekSessionStore keeps the pinned sessions, keyed by conversation.
type deepSeekSessionStore struct {
	mu    sync.Mutex
	items map[string]*deepSeekSessionEntry
}

// maxDeepSeekSessions bounds the store. One entry per live client
// conversation, so only a pathological client count reaches it.
const maxDeepSeekSessions = 512

// deepSeekSessionIdleTTL is how long a pinned conversation survives without
// activity. A conversation that goes quiet is done; keeping its chain would
// only let a later conversation with the same fallback key inherit it.
const deepSeekSessionIdleTTL = 30 * time.Minute

var deepSeekSessions = &deepSeekSessionStore{items: make(map[string]*deepSeekSessionEntry)}

// deepSeekSessionKey identifies one conversation.
//
// The client's streaming mode is not part of the key: the upstream request
// always asks for a stream (the site's own client has no other mode), so a
// streamed and a non-streamed turn of one conversation are the same
// conversation upstream, and both drain an event stream. The split this key
// used to carry existed only because the request's own `stream` flag once
// followed the client's, and a session created for one flag answered the
// other with an empty body.
//
// A client that names its conversation uses its id; one that does not falls
// back to the fingerprint of the conversation's opening message, so the
// anti-mute pinning holds for clients that never send a session header.
func deepSeekSessionKey(userToken, clientSessionID, firstMessageFingerprint string) string {
	if userToken == "" {
		return ""
	}
	if clientSessionID != "" {
		return userToken + "|" + clientSessionID
	}
	if firstMessageFingerprint != "" {
		return userToken + "|anon|" + firstMessageFingerprint
	}
	return ""
}

// deepSeekSessionClaim is the outcome of asking for a conversation's session.
type deepSeekSessionClaim struct {
	// reuse is true when the caller must thread on the stored session and send
	// only Tail of the prompt.
	reuse bool

	sessionID string

	// parentMessageID is nil when the upstream never named the previous
	// answer's id, which is what the endpoint expects for a new thread.
	parentMessageID *int64

	// tail is the prompt to send: the part the previous turn did not send, or
	// the whole prompt when the session is fresh.
	tail string

	// pin reports whether this turn may write the conversation's entry. A turn
	// that found the session busy holds its own, and writing the entry would
	// overwrite the chain the turn in flight is still building.
	pin bool
}

// claim takes the conversation's session for one turn.
//
// It never blocks. A conversation whose session is already in use is served by
// a fresh, unpersisted session for that turn: waiting would serialize a client
// that legitimately fired two turns, and sharing would fork the parent chain.
func (s *deepSeekSessionStore) claim(key, fullPrompt string) deepSeekSessionClaim {
	if key == "" {
		return deepSeekSessionClaim{tail: fullPrompt}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.items[key]
	if ok && entry.busy {
		// Another turn of this conversation is mid-flight. This turn gets its
		// own session and must not touch the pinned entry: writing it would
		// overwrite the chain the other turn is still building. The busy check
		// comes before the idle TTL for the same reason — an entry a turn is
		// still building must survive however long that turn runs, or the
		// committing turn would pin a chain over this one's session.
		return deepSeekSessionClaim{tail: fullPrompt}
	}
	if ok && time.Since(entry.lastUsed) > deepSeekSessionIdleTTL {
		// The conversation went quiet and the chain is stale: a fallback key
		// may now belong to a different conversation, so it starts fresh
		// instead of inheriting the old thread.
		delete(s.items, key)
		ok = false
	}
	if !ok {
		return deepSeekSessionClaim{tail: fullPrompt, pin: true}
	}

	tail, reusable := deepSeekPromptTail(entry.sentPrompt, fullPrompt, entry.parentMessageID > 0)
	if !reusable {
		// A prompt the upstream cannot be given as a tail, or a chain whose
		// parent the upstream never named: the old session is not a base for
		// this turn, so it is replaced by whatever this turn establishes.
		delete(s.items, key)
		return deepSeekSessionClaim{tail: fullPrompt, pin: true}
	}

	entry.busy = true
	entry.lastUsed = time.Now()
	parent := entry.parentMessageID
	return deepSeekSessionClaim{
		reuse:           true,
		sessionID:       entry.sessionID,
		parentMessageID: &parent,
		tail:            tail,
		pin:             true,
	}
}

// commit records what the turn actually sent, so the next turn knows its tail.
//
// A turn that was not allowed to pin leaves the store alone, whatever it
// achieved: a concurrent turn of the same conversation owns the chain this one
// would otherwise replace.
//
// A turn the upstream never named an answer for leaves no entry: without the
// message id there is no parent to thread, and a stored entry would only promise
// a chain that cannot be resumed.
func (s *deepSeekSessionStore) commit(claim deepSeekSessionClaim, key, sessionID, fullPrompt string, responseMessageID *int64) {
	if key == "" || !claim.pin {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if sessionID == "" || responseMessageID == nil {
		delete(s.items, key)
		return
	}
	if _, exists := s.items[key]; !exists && len(s.items) >= maxDeepSeekSessions {
		s.evictOldestLocked()
	}
	s.items[key] = &deepSeekSessionEntry{
		sessionID:       sessionID,
		sentPrompt:      fullPrompt,
		parentMessageID: *responseMessageID,
		lastUsed:        time.Now(),
	}
}

// forget drops a conversation's entry, but never one another turn holds.
//
// sessionID names the session the caller was using: an entry carrying a
// different one belongs to a concurrent turn of the same conversation, and
// clearing it would strand that turn's chain for no reason.
func (s *deepSeekSessionStore) forget(key, sessionID string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.items[key]; ok && sessionID != "" && entry.sessionID != sessionID {
		return
	}
	delete(s.items, key)
}

// holds reports whether the conversation is still pinned to this session, which
// is how the caller knows whether the upstream copy may be closed.
func (s *deepSeekSessionStore) holds(key, sessionID string) bool {
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[key]
	return ok && entry.sessionID == sessionID
}

// evictOldestLocked drops the entry idle longest, so the store keeps the
// conversations that are still going. The oldest is the smallest lastUsed;
// an entry without one predates the field and is evicted first.
func (s *deepSeekSessionStore) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, entry := range s.items {
		if oldestKey == "" || entry.lastUsed.Before(oldest) {
			oldestKey = key
			oldest = entry.lastUsed
		}
	}
	if oldestKey != "" {
		delete(s.items, oldestKey)
	}
}

// reset clears the store, for tests that drive real turns against one another.
func (s *deepSeekSessionStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make(map[string]*deepSeekSessionEntry)
}

// deepSeekPromptTail returns the part of fullPrompt that sentPrompt does not
// already contain, and whether the upstream can be given only that part.
//
// Three shapes matter:
//
//	sentPrompt is a prefix     → the tail is what follows it (the normal case:
//	                             the client appended its new turn)
//	the prompts are identical  → the client is retrying the same turn, so the
//	                             whole prompt is re-sent rather than an empty
//	                             string the upstream would reject
//	anything else              → the history diverged, and the caller must start
//	                             a new session
func deepSeekPromptTail(sentPrompt, fullPrompt string, haveParent bool) (string, bool) {
	if !haveParent {
		return fullPrompt, false
	}
	if sentPrompt == fullPrompt {
		return fullPrompt, true
	}
	if !strings.HasPrefix(fullPrompt, sentPrompt) {
		return fullPrompt, false
	}
	tail := strings.TrimLeft(fullPrompt[len(sentPrompt):], "\n")
	if tail == "" {
		return fullPrompt, true
	}
	return tail, true
}
