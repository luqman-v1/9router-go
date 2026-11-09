package executor

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"9router/proxy/internal/proxy"
)

// Stealth mode is the set of behaviours that keeps a browser-login account from
// being muted. Each test here pins one of them against the observable contract:
// what the upstream was sent, and which session the turn was asked to continue.

// A conversation pinned to one upstream session sends only the new tail of its
// prompt, and threads on the message id the upstream named for the previous
// answer.
//
// Re-sending the whole transcript is the behaviour the sibling implementation
// names as the main cause of account mutes: every message in the upstream
// session then embeds every previous turn, which grows quadratically and no
// browser produces it.
func TestDeepSeekStealthReusesSessionAndSendsOnlyTheTail(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekTurnFrames(4242, "the answer"))
	server := httptest.NewServer(upstream)
	defer server.Close()

	first := deepSeekTestBody(t, "deepseek-chat",
		map[string]any{"role": "user", "content": "what is the capital of Indonesia?"})
	if err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-1", "conversation-1", first)); err != nil {
		t.Fatalf("first turn: %v", err)
	}

	// Second turn: the client sends the grown history, as a chat client does.
	second := deepSeekTestBody(t, "deepseek-chat",
		map[string]any{"role": "user", "content": "what is the capital of Indonesia?"},
		map[string]any{"role": "assistant", "content": "Jakarta"},
		map[string]any{"role": "user", "content": "and its population?"})
	if err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-1", "conversation-1", second)); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	// One session for the conversation, not one per turn.
	if upstream.createdSessions != 1 {
		t.Errorf("sessions created = %d, want 1: the conversation must stay pinned", upstream.createdSessions)
	}
	// The pinned session must not be closed either: closing it is exactly what
	// makes the next turn a fresh thread.
	if got := upstream.countCalls("/api/v0/chat_session/delete"); got != 0 {
		t.Errorf("session deletes = %d, want 0 while the conversation is pinned", got)
	}

	payload := upstream.body(t, deepSeekCompletionPath)
	prompt, _ := payload["prompt"].(string)
	if strings.Contains(prompt, "capital of Indonesia") {
		t.Errorf("the follow-up turn re-sent history the upstream already holds:\n%s", prompt)
	}
	if !strings.Contains(prompt, "population") {
		t.Errorf("the tail of the new turn is missing from the prompt:\n%s", prompt)
	}
	if got, _ := payload["parent_message_id"].(float64); int64(got) != 4242 {
		t.Errorf("parent_message_id = %v, want 4242 (the id the upstream named)", payload["parent_message_id"])
	}
	if got := payload["chat_session_id"]; got != "session-1" {
		t.Errorf("chat_session_id = %v, want the pinned session-1", got)
	}
}

// A client that rewrites its history cannot be given a tail, so the
// conversation starts a fresh session with the whole prompt rather than sending
// the upstream a continuation that does not extend what it holds.
func TestDeepSeekStealthDivergedHistoryStartsAFreshSession(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekTurnFrames(7, "ok"))
	server := httptest.NewServer(upstream)
	defer server.Close()

	first := deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "one"})
	if err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-1", "conversation-2", first)); err != nil {
		t.Fatalf("first turn: %v", err)
	}

	// A diverging history: the client dropped the earlier turn entirely.
	diverged := deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "something else"})
	if err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-1", "conversation-2", diverged)); err != nil {
		t.Fatalf("second turn: %v", err)
	}

	if upstream.createdSessions != 2 {
		t.Errorf("sessions created = %d, want 2: a diverged prompt cannot continue the old session", upstream.createdSessions)
	}
	payload := upstream.body(t, deepSeekCompletionPath)
	if payload["parent_message_id"] != nil {
		t.Errorf("parent_message_id = %v, want null for a fresh thread", payload["parent_message_id"])
	}
	// A pinned conversation renders as a role-tagged transcript, so the whole
	// diverged turn arrives in that form — the point is that it arrives whole.
	if prompt, _ := payload["prompt"].(string); prompt != "User: something else" {
		t.Errorf("prompt = %q, want the whole diverged prompt", prompt)
	}
}

// A pinned session that fails at the HTTP level is a stale session — the chat
// was deleted in the DeepSeek UI, or the upstream dropped it — and not an
// account verdict. The turn heals the way the sibling implementation does: the
// stale session is dropped and the completion is retried once on a fresh
// session carrying the whole prompt, and the client never hears about it.
func TestDeepSeekStealthStaleSessionHealsWithAFreshSession(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekTurnFrames(88, "the answer"))
	server := httptest.NewServer(upstream)
	defer server.Close()

	first := deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "first question"})
	if err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-heal", "conversation-heal", first)); err != nil {
		t.Fatalf("first turn: %v", err)
	}

	// The pinned session is now dead upstream: the next completion on it
	// answers HTTP 500. The turn must retry on a session it opened itself.
	upstream.mu.Lock()
	upstream.failCompletions = 1
	upstream.mu.Unlock()

	second := deepSeekTestBody(t, "deepseek-chat",
		map[string]any{"role": "user", "content": "first question"},
		map[string]any{"role": "assistant", "content": "first answer"},
		map[string]any{"role": "user", "content": "second question"})
	if err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-heal", "conversation-heal", second)); err != nil {
		t.Fatalf("healing turn: %v", err)
	}

	if got := upstream.createdSessions; got != 2 {
		t.Errorf("sessions created = %d, want 2: the healing turn must open a fresh session", got)
	}
	// One completion for the first turn, then the failed attempt plus the
	// healing retry for the second.
	if got := upstream.countCalls(deepSeekCompletionPath); got != 3 {
		t.Errorf("completion calls = %d, want 3: the stale session is retried once, not repeatedly", got)
	}

	// The retry carried the whole prompt on a fresh thread, not the tail the
	// dead chain would have dictated.
	payload := upstream.body(t, deepSeekCompletionPath)
	if got, _ := payload["chat_session_id"].(string); got != "session-2" {
		t.Errorf("chat_session_id = %v, want the fresh session-2", got)
	}
	if payload["parent_message_id"] != nil {
		t.Errorf("parent_message_id = %v, want null on the healing thread", payload["parent_message_id"])
	}
	if prompt, _ := payload["prompt"].(string); !strings.Contains(prompt, "second question") || !strings.Contains(prompt, "first question") {
		t.Errorf("the healing turn must re-send the whole prompt, got:\n%s", prompt)
	}

	// The healed session is what the conversation is pinned to now: the next
	// turn continues it instead of starting over.
	third := deepSeekTestBody(t, "deepseek-chat",
		map[string]any{"role": "user", "content": "first question"},
		map[string]any{"role": "assistant", "content": "first answer"},
		map[string]any{"role": "user", "content": "second question"},
		map[string]any{"role": "assistant", "content": "the answer"},
		map[string]any{"role": "user", "content": "third question"})
	if err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-heal", "conversation-heal", third)); err != nil {
		t.Fatalf("third turn: %v", err)
	}
	if got := upstream.createdSessions; got != 2 {
		t.Errorf("sessions created = %d, want 2: the healed session must stay pinned", got)
	}
	payload = upstream.body(t, deepSeekCompletionPath)
	if got, _ := payload["chat_session_id"].(string); got != "session-2" {
		t.Errorf("chat_session_id = %v, want the healed session-2", got)
	}
	if got, _ := payload["parent_message_id"].(float64); int64(got) != 88 {
		t.Errorf("parent_message_id = %v, want 88 (the id the upstream named on the healed thread)", payload["parent_message_id"])
	}
}

// An auth failure on a reused session is a rejected token, not a stale
// session: the access token was freshly exchanged and the upstream refused
// it, so a new session cannot help and healing would only add more calls to
// the account the site is refusing. The failure surfaces as before.
func TestDeepSeekStealthAuthFailureIsNotHealed(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekTurnFrames(77, "the answer"))
	server := httptest.NewServer(upstream)
	defer server.Close()

	first := deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "first question"})
	if err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-auth", "conversation-auth", first)); err != nil {
		t.Fatalf("first turn: %v", err)
	}

	upstream.mu.Lock()
	upstream.failCompletions = 1
	upstream.failStatus = http.StatusUnauthorized
	upstream.mu.Unlock()

	second := deepSeekTestBody(t, "deepseek-chat",
		map[string]any{"role": "user", "content": "first question"},
		map[string]any{"role": "assistant", "content": "first answer"},
		map[string]any{"role": "user", "content": "second question"})
	err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-auth", "conversation-auth", second))
	if err == nil {
		t.Fatal("a rejected token must surface as an error, not be healed")
	}
	if !isDeepSeekAuthError(err) {
		t.Fatalf("err = %v, want an auth failure the cooldown path can park", err)
	}
	if got := upstream.createdSessions; got != 1 {
		t.Errorf("sessions created = %d, want 1: a dead token must not be given a fresh session", got)
	}
	if got := upstream.countCalls(deepSeekCompletionPath); got != 2 {
		t.Errorf("completion calls = %d, want 2: the refused turn must not be retried", got)
	}
}

// Two turns of one conversation fired at once must not share a session: the
// second would fork the parent chain. The one that loses the race gets its own
func TestDeepSeekStealthConcurrentTurnDoesNotForkTheChain(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)

	key := deepSeekSessionKey("user-token-1", "conversation-3", "")
	deepSeekSessions.commit(deepSeekSessionClaim{pin: true}, key, "session-pinned", "turn one", new(int64(11)))

	// The first turn takes the session, which marks it busy for as long as the
	// turn runs.
	held := deepSeekSessions.claim(key, "turn one\n\nturn two")
	if !held.reuse {
		t.Fatalf("the first turn must take the pinned session, got %+v", held)
	}

	// A second turn of the same conversation now cannot share it, so it gets
	// its own session and leaves the pinned one alone.
	claim := deepSeekSessions.claim(key, "turn one\n\nturn two")
	if claim.reuse {
		t.Fatal("a second concurrent turn must not reuse the session in flight")
	}
	if claim.tail != "turn one\n\nturn two" {
		t.Errorf("tail = %q, want the whole prompt for a turn with its own session", claim.tail)
	}
	if claim.sessionID != "" {
		t.Errorf("sessionID = %q, want empty: this turn creates its own", claim.sessionID)
	}
}

// A session the upstream never named a message id for cannot be continued, and
// the store must not promise otherwise.
func TestDeepSeekStealthSessionWithoutMessageIDIsNotPinned(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	key := deepSeekSessionKey("user-token-1", "conversation-4", "")

	deepSeekSessions.commit(deepSeekSessionClaim{pin: true}, key, "session-1", "turn one", nil)

	if got := deepSeekSessions.claim(key, "turn one\n\nturn two"); got.reuse {
		t.Error("a session with no parent message id must not be continued")
	}
}

// A turn that did not pin must not write the pinned entry when it finishes.
//
// The store used to accept any turn's commit, so a concurrent second turn of
// one conversation replaced the entry the first turn still held — and the first
// turn's own follow-up then threaded on a session the second turn had created
// and the first had never seen.
func TestDeepSeekStealthConcurrentTurnDoesNotOverwriteThePinnedEntry(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)

	key := deepSeekSessionKey("user-token-1", "conversation-11", "")
	deepSeekSessions.commit(deepSeekSessionClaim{pin: true}, key, "session-pinned", "turn one", new(int64(11)))

	held := deepSeekSessions.claim(key, "turn one\n\nturn two")
	if !held.reuse {
		t.Fatalf("the first turn must take the pinned session, got %+v", held)
	}

	// The second turn was told it may not pin, and commits anyway.
	other := deepSeekSessions.claim(key, "turn one\n\nturn two")
	if other.pin {
		t.Fatal("a turn that found the session busy must not be allowed to pin")
	}
	deepSeekSessions.commit(other, key, "session-other", "turn one\n\nturn two", new(int64(99)))

	// The entry must still be the one the first turn holds.
	if !deepSeekSessions.holds(key, "session-pinned") {
		t.Error("a non-pinning turn overwrote the chain the in-flight turn is building")
	}

	// And abandoning the second turn must not clear it either.
	deepSeekSessions.forget(key, "session-other")
	if !deepSeekSessions.holds(key, "session-pinned") {
		t.Error("forgetting a concurrent turn's session cleared the pinned entry")
	}
}

// The client's streaming mode is not part of a conversation's identity: the
// upstream request always asks for a stream, so a streamed and a non-streamed
// turn of one conversation share the session the conversation is pinned to.
// The split used to exist because the request's own `stream` flag followed
// the client's, and a session created for one flag answered the other with
// an empty body; with the flag constant there is nothing left to split on.
func TestDeepSeekSessionKeyIgnoresTheTransportMode(t *testing.T) {
	if deepSeekSessionKey("token", "conversation", "") == "" {
		t.Error("a named conversation must have a session key")
	}
	if deepSeekSessionKey("token", "", "fingerprint-a") == "" {
		t.Error("a conversation with no client id but a fingerprint must be pinnable")
	}
	if deepSeekSessionKey("", "conversation", "") != "" {
		t.Error("a turn with no credential must not be pinnable")
	}
	if deepSeekSessionKey("token", "", "") != "" {
		t.Error("a turn with neither client id nor fingerprint must not be pinnable")
	}
	if a, b := deepSeekSessionKey("token", "conversation", ""), deepSeekSessionKey("token", "other", ""); a == b {
		t.Error("two named conversations must not share a session key")
	}
	if a, b := deepSeekSessionKey("token", "", "fp-a"), deepSeekSessionKey("token", "", "fp-b"); a == b {
		t.Error("two conversations with different openings must not share a session key")
	}
}

// A client that never sends a session id still pins, keyed by the account and
// the opening message: without the fallback every anonymous turn would replay
// the full transcript on a fresh session, the quadratic growth that triggers
// account-level mutes.
func TestDeepSeekStealthAnonymousConversationPinsOnTheOpeningMessage(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)

	key := deepSeekSessionKey("user-token-1", "", "fp-1")
	deepSeekSessions.commit(deepSeekSessionClaim{pin: true}, key, "session-anon", "turn one", new(int64(11)))

	claim := deepSeekSessions.claim(key, "turn one\n\nturn two")
	if !claim.reuse {
		t.Fatalf("an anonymous conversation must reuse its pinned session, got %+v", claim)
	}
	if claim.sessionID != "session-anon" {
		t.Errorf("sessionID = %q, want the pinned session", claim.sessionID)
	}
}

// A pinned conversation that goes quiet ages out, so a later conversation that
// happens to share a fallback key does not inherit its thread.
func TestDeepSeekStealthIdleSessionExpires(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)

	key := deepSeekSessionKey("user-token-1", "", "fp-1")
	deepSeekSessions.commit(deepSeekSessionClaim{pin: true}, key, "session-old", "turn one", new(int64(11)))

	deepSeekSessions.mu.Lock()
	entry := deepSeekSessions.items[key]
	entry.lastUsed = time.Now().Add(-deepSeekSessionIdleTTL - time.Minute)
	deepSeekSessions.mu.Unlock()

	claim := deepSeekSessions.claim(key, "turn one\n\nturn two")
	if claim.reuse {
		t.Fatal("an idle conversation must not reuse its stale session")
	}
	if !claim.pin {
		t.Fatal("an expired entry must be replaced by what this turn establishes")
	}
}

// A session a turn is mid-flight on is never dropped, however long that turn
// runs: the idle TTL is for conversations that went quiet, and evicting a busy
// entry would let the committing turn pin a chain over this one's session.
func TestDeepSeekStealthBusySessionSurvivesTheIdleTTL(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)

	key := deepSeekSessionKey("user-token-1", "conversation-ttl", "")
	deepSeekSessions.commit(deepSeekSessionClaim{pin: true}, key, "session-pinned", "turn one", new(int64(11)))

	deepSeekSessions.mu.Lock()
	entry := deepSeekSessions.items[key]
	entry.busy = true
	entry.lastUsed = time.Now().Add(-deepSeekSessionIdleTTL - time.Minute)
	deepSeekSessions.mu.Unlock()

	claim := deepSeekSessions.claim(key, "turn one\n\nturn two")
	if claim.reuse || claim.pin {
		t.Fatalf("a busy session must be left to its holding turn, got %+v", claim)
	}
	if !deepSeekSessions.holds(key, "session-pinned") {
		t.Fatal("the busy entry must survive the claim: its turn is still building it")
	}

	// The holding turn ends and settles: its commit refreshes the entry, and
	// the chain must still be resumable — which is what eviction here would
	// have destroyed.
	deepSeekSessions.commit(deepSeekSessionClaim{pin: true}, key, "session-pinned", "turn one\n\nturn two", new(int64(12)))
	if claim := deepSeekSessions.claim(key, "turn one\n\nturn two\n\nturn three"); !claim.reuse {
		t.Fatalf("the settled session must still be reusable, got %+v", claim)
	}
}

// The anonymous fallback key is the conversation's first USER message, never
// the system prompt: a client that leads every conversation with the same
// system message would otherwise put all of its conversations on one key,
// where each divergent turn evicts the other's chain and every turn falls
// back to a fresh session carrying the full transcript.
func TestDeepSeekFirstMessageFingerprintSkipsTheSystemPrompt(t *testing.T) {
	system := map[string]any{"role": "system", "content": "you are a coding agent"}
	body := func(messages ...map[string]any) []deepSeekMessage {
		t.Helper()
		var decoded struct {
			Messages []deepSeekMessage `json:"messages"`
		}
		raw, err := json.Marshal(map[string]any{"messages": messages})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return decoded.Messages
	}

	sameOpening := deepSeekFirstMessageFingerprint(body(system, map[string]any{"role": "user", "content": "hi"}))
	otherOpening := deepSeekFirstMessageFingerprint(body(system, map[string]any{"role": "user", "content": "write tests"}))
	if sameOpening == "" || otherOpening == "" {
		t.Fatal("a conversation with a user message must produce a fingerprint")
	}
	if sameOpening == otherOpening {
		t.Error("two conversations differing only after the system prompt must not share a fallback key")
	}
	if again := deepSeekFirstMessageFingerprint(body(system, map[string]any{"role": "user", "content": "hi"})); again != sameOpening {
		t.Error("identical openings must share the fallback key")
	}
	if got := deepSeekFirstMessageFingerprint(body(system, map[string]any{"role": "assistant", "content": "only an assistant turn"})); got != "" {
		t.Errorf("a conversation with no user message must not be pinnable, got %q", got)
	}
}

// The account-level mute arrives as HTTP 200 with a plain JSON body, and has to
// reach the router as a 429 carrying the wait the upstream announced: a 502 is
// retried against the same punished account, which is how a brief mute becomes
// a long one.
func TestDeepSeekStealthMutedAccountReportsTooManyRequests(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t)
	upstream.muteBody = deepSeekMuteBody()
	server := httptest.NewServer(upstream)
	defer server.Close()

	err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-1", "conversation-5",
		deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"})))
	if err == nil {
		t.Fatal("a muted account must not be reported as a served turn")
	}

	status, ok := deepSeekUpstreamStatus(err)
	if !ok || status != http.StatusTooManyRequests {
		t.Fatalf("error = %v (status %d), want 429 so the account is cooled down and rotated", err, status)
	}
	if !strings.Contains(err.Error(), "muted") {
		t.Errorf("error = %v, want it to say the account is muted", err)
	}
	if got := retryAfterOf(err); got == "" {
		t.Error("a mute with an announced end must carry Retry-After, or the account is retried immediately")
	}
}

// The burst-limit refusal arrives inside the stream as an error frame with no
// fragments at all. Read as a cut-off session it becomes a 502, which the
// client retries; read as what it is, it is a 429.
func TestDeepSeekStealthRateLimitHintIsTooManyRequests(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekRateLimitHintFrame())
	server := httptest.NewServer(upstream)
	defer server.Close()

	err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-1", "conversation-6",
		deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"})))
	if err == nil {
		t.Fatal("a rate-limited turn must not be reported as a served turn")
	}
	if status, ok := deepSeekUpstreamStatus(err); !ok || status != http.StatusTooManyRequests {
		t.Fatalf("error = %v (status %d), want 429", err, status)
	}
}

// A version refusal is not the account's fault, so it must not cool the account
// down — but it is not an answer either, so the turn still fails.
func TestDeepSeekStealthClientRefusalDoesNotCoolDownTheAccount(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekClientRejectedToastFrame())
	server := httptest.NewServer(upstream)
	defer server.Close()

	err := forwardTurn(t, deepSeekTurnRequest(server, "user-token-1", "conversation-7",
		deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"})))
	if err == nil {
		t.Fatal("a refused turn must not be reported as served")
	}
	if status, ok := deepSeekUpstreamStatus(err); !ok || status != http.StatusBadGateway {
		t.Fatalf("error = %v (status %d), want 502 rather than a cooldown on the account", err, status)
	}
}

// A streaming client must be told about the refusal in-band: the status is
// already on the wire, so all that is left is an error frame and a terminator.
func TestDeepSeekStealthStreamedRefusalReachesTheClient(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekRateLimitHintFrame())
	server := httptest.NewServer(upstream)
	defer server.Close()

	turn := deepSeekTurnRequest(server, "user-token-1", "conversation-8",
		deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"}))
	turn.IsStream = true
	rec := httptest.NewRecorder()
	if err := ForwardDeepSeekWeb(rec, turn); err == nil {
		t.Fatal("the turn must still report the refusal to the router")
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"error"`) {
		t.Errorf("the refusal is missing from the stream; body: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("an in-band refusal must still terminate the stream; body: %s", body)
	}
}

// Stealth off restores the plain path: the minimal header set, no attestation
// fetch, and a session per request. That is what makes the option a real
// comparison rather than a different flavour of the same behaviour.
func TestDeepSeekStealthOffUsesThePlainPath(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekTurnFrames(1, "ok"))
	server := httptest.NewServer(upstream)
	defer server.Close()

	turn := deepSeekTurnRequest(server, "user-token-1", "conversation-9",
		deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"}))
	turn.ConnData = map[string]any{"stealth": false}
	if err := forwardTurn(t, turn); err != nil {
		t.Fatalf("turn: %v", err)
	}

	headers := upstream.headers[deepSeekCompletionPath]
	if got := headers.Get("X-Hif-Leim"); got != "" {
		t.Errorf("X-Hif-Leim = %q, want none with stealth off", got)
	}
	if got := headers.Get("Sec-Fetch-Mode"); got != "" {
		t.Errorf("Sec-Fetch-Mode = %q, want none with stealth off", got)
	}
	if got := headers.Get("X-Client-Version"); got != deepSeekWebClientVersion {
		t.Errorf("X-Client-Version = %q, want %q", got, deepSeekWebClientVersion)
	}
	if got := upstream.countCalls("/api/v0/chat_session/delete"); got != 1 {
		t.Errorf("session deletes = %d, want 1: the plain path serves one session per turn", got)
	}
}

// Stealth off must also stop the turn from being pinned, even when the client
// names its conversation: otherwise the option would only change the headers.
func TestDeepSeekStealthOffDoesNotPinSessions(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekTurnFrames(1, "ok"))
	server := httptest.NewServer(upstream)
	defer server.Close()

	for range 2 {
		turn := deepSeekTurnRequest(server, "user-token-1", "conversation-10",
			deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"}))
		turn.ConnData = map[string]any{"stealth": false}
		if err := forwardTurn(t, turn); err != nil {
			t.Fatalf("turn: %v", err)
		}
	}

	if upstream.createdSessions != 2 {
		t.Errorf("sessions created = %d, want 2: with stealth off every turn opens its own", upstream.createdSessions)
	}
}

// The per-account gate serializes a burst instead of letting it through. Two
// turns of one account may run; the third waits.
func TestDeepSeekStealthGateCapsConcurrencyPerAccount(t *testing.T) {
	gate := &deepSeekGate{slots: map[string]chan struct{}{}}
	ctx := context.Background()

	first, err := gate.acquire(ctx, "account-a", 2, 0)
	if err != nil {
		t.Fatalf("first slot: %v", err)
	}
	second, err := gate.acquire(ctx, "account-a", 2, 0)
	if err != nil {
		t.Fatalf("second slot: %v", err)
	}

	// A third request for the same account cannot be served now. The wait is
	// zero here so the test does not sit out the production queue timeout.
	if _, err := gate.acquire(ctx, "account-a", 2, 0); err == nil {
		t.Fatal("a third concurrent request must not be admitted past the cap")
	}

	// A different account is unaffected: the cap is per account, not global.
	other, err := gate.acquire(ctx, "account-b", 2, 0)
	if err != nil {
		t.Fatalf("a second account must not be gated by the first: %v", err)
	}
	other()

	second()
	// A second release must not free the slot twice, or the cap would leak
	// capacity on every path that releases more than once.
	second()
	third, err := gate.acquire(ctx, "account-a", 2, 0)
	if err != nil {
		t.Fatalf("a released slot must be reusable: %v", err)
	}
	third()
	first()
}

// The gate must not leave a leaked slot when its context ends while waiting.
func TestDeepSeekStealthGateReleasesOnContextCancel(t *testing.T) {
	gate := &deepSeekGate{slots: map[string]chan struct{}{}}
	held, err := gate.acquire(context.Background(), "account-c", 1, 0)
	if err != nil {
		t.Fatalf("slot: %v", err)
	}
	defer held()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.acquire(ctx, "account-c", 1, 0); err == nil {
		t.Fatal("a cancelled wait must fail rather than block forever")
	}
}

// A cancelled wait must also free the slot it was waiting for once the holder
// releases it, so a burst does not permanently narrow the account.
func TestDeepSeekStealthGateSlotSurvivesAFailedWait(t *testing.T) {
	gate := &deepSeekGate{slots: map[string]chan struct{}{}}
	held, err := gate.acquire(context.Background(), "account-d", 1, 0)
	if err != nil {
		t.Fatalf("slot: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.acquire(ctx, "account-d", 1, 0); err == nil {
		t.Fatal("a cancelled wait must fail")
	}
	held()

	next, err := gate.acquire(context.Background(), "account-d", 1, 0)
	if err != nil {
		t.Fatalf("the slot must be usable again after the holder releases: %v", err)
	}
	next()
}

// The attestation refresh keeps the cached value when the host cannot be
// reached: a header the site issued is closer to a browser than no header, and
// a turn must never fail because an attestation host is down.
func TestDeepSeekAttestationFallsBackToTheCachedValue(t *testing.T) {
	cache := &deepSeekHifCache{values: map[string]deepSeekHifEntry{}}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dead.Close()

	// Seed the value the way a successful fetch would have stored it, with a
	// TTL that has already lapsed, so this fetch is the refresh path.
	cache.store("leim", "issued-value", -time.Second)

	if got := cache.value(context.Background(), dead.Client(), "leim", dead.URL, "https://chat.deepseek.com"); got != "issued-value" {
		t.Errorf("value = %q, want the cached value when the refresh fails", got)
	}
}

// A value fetched once is reused until its TTL passes: re-fetching it on every
// turn would be its own kind of fingerprint.
func TestDeepSeekAttestationCachesUntilItsTTLExpires(t *testing.T) {
	cache := &deepSeekHifCache{values: map[string]deepSeekHifEntry{}}

	var fetches int
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-hif-ttl", "600")
		_, _ = w.Write([]byte(`{"code":0,"data":{"biz_data":{"value":"issued-value"}}}`))
	}))
	defer live.Close()

	ctx := context.Background()
	client := live.Client()
	for range 3 {
		if got := cache.value(ctx, client, "leim", live.URL, "https://chat.deepseek.com"); got != "issued-value" {
			t.Fatalf("value = %q, want the fetched value", got)
		}
	}
	if fetches != 1 {
		t.Errorf("fetches = %d, want 1: the value must be cached for its announced TTL", fetches)
	}
}

// The announced TTL is what the cache honours, with the documented default
// standing in for an answer that names none.
func TestDeepSeekAttestationTTL(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "announced", header: "600", want: 10 * time.Minute},
		{name: "short", header: "30", want: 30 * time.Second},
		{name: "absent", header: "", want: deepSeekHifDefaultTTL},
		{name: "nonsense", header: "soon", want: deepSeekHifDefaultTTL},
		{name: "zero", header: "0", want: deepSeekHifDefaultTTL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deepSeekHifTTL(tt.header); got != tt.want {
				t.Errorf("deepSeekHifTTL(%q) = %s, want %s", tt.header, got, tt.want)
			}
		})
	}
}

// The stealth options are read from the connection, and every one of them
// defaults to the browser-parity value so a connection that configures nothing
// gets the stealth path.
func TestDeepSeekStealthOptions(t *testing.T) {
	tests := []struct {
		name           string
		connData       map[string]any
		wantEnabled    bool
		wantPersist    bool
		wantConcurrenc int
		wantJitterMs   int64
		wantGapMs      int64
	}{
		{name: "unconfigured defaults to stealth", wantEnabled: true, wantPersist: true, wantConcurrenc: deepSeekStealthDefaultConcurrency, wantJitterMs: 400, wantGapMs: deepSeekStealthDefaultCallGapMs},
		{name: "explicitly off", connData: map[string]any{"stealth": false}},
		{
			name:           "concurrency raised",
			connData:       map[string]any{"maxConcurrency": float64(8)},
			wantEnabled:    true,
			wantPersist:    true,
			wantConcurrenc: 8,
			wantJitterMs:   400,
			wantGapMs:      deepSeekStealthDefaultCallGapMs,
		},
		{
			name:           "concurrency uncapped",
			connData:       map[string]any{"maxConcurrency": float64(0)},
			wantEnabled:    true,
			wantPersist:    true,
			wantConcurrenc: 0,
			wantJitterMs:   400,
			wantGapMs:      deepSeekStealthDefaultCallGapMs,
		},
		{
			name:           "session persistence off",
			connData:       map[string]any{"persistSession": false},
			wantEnabled:    true,
			wantPersist:    false,
			wantConcurrenc: deepSeekStealthDefaultConcurrency,
			wantJitterMs:   400,
			wantGapMs:      deepSeekStealthDefaultCallGapMs,
		},
		{
			name:           "jitter off",
			connData:       map[string]any{"jitterMs": float64(0)},
			wantEnabled:    true,
			wantPersist:    true,
			wantConcurrenc: deepSeekStealthDefaultConcurrency,
			wantJitterMs:   0,
			wantGapMs:      deepSeekStealthDefaultCallGapMs,
		},
		{
			name:           "call gap off",
			connData:       map[string]any{"callGapMs": float64(0)},
			wantEnabled:    true,
			wantPersist:    true,
			wantConcurrenc: deepSeekStealthDefaultConcurrency,
			wantJitterMs:   400,
			wantGapMs:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deepSeekStealthFor(tt.connData)
			if got.enabled != tt.wantEnabled {
				t.Errorf("enabled = %t, want %t", got.enabled, tt.wantEnabled)
			}
			if !tt.wantEnabled {
				return
			}
			if got.persistSession != tt.wantPersist {
				t.Errorf("persistSession = %t, want %t", got.persistSession, tt.wantPersist)
			}
			if got.maxConcurrency != tt.wantConcurrenc {
				t.Errorf("maxConcurrency = %d, want %d", got.maxConcurrency, tt.wantConcurrenc)
			}
			if got.jitter.Milliseconds() != tt.wantJitterMs {
				t.Errorf("jitter = %dms, want %dms", got.jitter.Milliseconds(), tt.wantJitterMs)
			}
			if got.callGap.Milliseconds() != tt.wantGapMs {
				t.Errorf("callGap = %dms, want %dms", got.callGap.Milliseconds(), tt.wantGapMs)
			}
		})
	}
}

// The prompt tail rules are the whole basis for sending less than the client
// did, so each shape is pinned: an extended prompt yields its new part, an
// identical one is re-sent whole, and a diverged one cannot be a tail.
func TestDeepSeekPromptTail(t *testing.T) {
	tests := []struct {
		name       string
		sent       string
		full       string
		haveParent bool
		wantTail   string
		wantOK     bool
	}{
		{
			name:       "appended turn yields the new part",
			sent:       "User: one",
			full:       "User: one\n\nUser: two",
			haveParent: true,
			wantTail:   "User: two",
			wantOK:     true,
		},
		{
			name:       "identical prompt is re-sent whole",
			sent:       "User: one",
			full:       "User: one",
			haveParent: true,
			wantTail:   "User: one",
			wantOK:     true,
		},
		{
			name:       "diverged prompt has no tail",
			sent:       "User: one",
			full:       "User: something else",
			haveParent: true,
			wantTail:   "User: something else",
			wantOK:     false,
		},
		{
			name:       "no parent means no continuation",
			sent:       "User: one",
			full:       "User: one\n\nUser: two",
			haveParent: false,
			wantTail:   "User: one\n\nUser: two",
			wantOK:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tail, ok := deepSeekPromptTail(tt.sent, tt.full, tt.haveParent)
			if ok != tt.wantOK {
				t.Errorf("reusable = %t, want %t", ok, tt.wantOK)
			}
			if tail != tt.wantTail {
				t.Errorf("tail = %q, want %q", tail, tt.wantTail)
			}
		})
	}
}

// The mute envelope is read by either carrier, because the site spells the same
// refusal two ways, and a payload that only carries one of them must still be
// recognised.
func TestDeepSeekMuteEnvelopeShapes(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantKind   deepSeekEnforcementKind
		wantNil    bool
		wantStatus int
	}{
		{
			name:       "biz_code 5",
			body:       `{"code":0,"data":{"biz_code":5,"biz_msg":"user is muted"}}`,
			wantKind:   deepSeekMuted,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "is_muted flag alone",
			body:       `{"code":0,"data":{"biz_msg":"","biz_data":{"is_muted":1}}}`,
			wantKind:   deepSeekMuted,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "biz_msg names a mute",
			body:       `{"code":0,"data":{"biz_msg":"account is muted for 3 days"}}`,
			wantKind:   deepSeekMuted,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "the mute code",
			body:       `{"code":50006,"msg":"user is muted"}`,
			wantKind:   deepSeekMuted,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "the ban code",
			body:       `{"code":40012,"msg":"user is banned"}`,
			wantKind:   deepSeekBanned,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "the address refusal code",
			body:       `{"code":40029,"msg":"ip access restricted"}`,
			wantKind:   deepSeekIPRestricted,
			wantStatus: http.StatusForbidden,
		},
		{
			name:    "a missing token is an auth failure, not an enforcement",
			body:    `{"code":40002,"msg":"Missing Token"}`,
			wantNil: true,
		},
		{
			name:    "auth failure is not an enforcement",
			body:    `{"code":40003,"msg":"Authorization Failed"}`,
			wantNil: true,
		},
		{
			name:    "an ordinary answer is not an enforcement",
			body:    `{"code":0,"data":{"biz_data":{"token":"access"}}}`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deepSeekEnforcementFromBody([]byte(tt.body))
			if tt.wantNil {
				if got != nil {
					t.Fatalf("enforcement = %+v, want none", got)
				}
				return
			}
			if got == nil {
				t.Fatal("no enforcement recognised")
			}
			if got.Kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", got.Kind, tt.wantKind)
			}
			if got.status() != tt.wantStatus {
				t.Errorf("status = %d, want %d", got.status(), tt.wantStatus)
			}
		})
	}
}

// The in-stream refusal arrives as an error payload, sometimes wrapped in `v`.
// Both spellings have to be read, and only a real error counts.
func TestDeepSeekStreamedEnforcementShapes(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantKind deepSeekEnforcementKind
		wantNil  bool
	}{
		{
			name:     "rate limit hint",
			payload:  `{"type":"error","content":"消息发送过于频繁","finish_reason":"rate_limit_reached"}`,
			wantKind: deepSeekRateLimited,
		},
		{
			name:     "wrapped in v",
			payload:  `{"v":{"type":"error","content":"slow down","finish_reason":"rate_limit_reached"}}`,
			wantKind: deepSeekRateLimited,
		},
		{
			name:     "client refusal",
			payload:  `{"type":"error","content":"update to the latest version","finish_reason":"unsupported_client_by_model"}`,
			wantKind: deepSeekClientRejected,
		},
		{
			name:     "other error frame",
			payload:  `{"type":"error","content":"something broke","finish_reason":"internal"}`,
			wantKind: deepSeekEnforced,
		},
		{
			name:    "ordinary fragment is not an error",
			payload: `{"v":{"response":{"fragments":[{"type":"RESPONSE","content":"hi"}]}}}`,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deepSeekEnforcementFromEvent([]byte(tt.payload))
			if tt.wantNil {
				if got != nil {
					t.Fatalf("enforcement = %+v, want none", got)
				}
				return
			}
			if got == nil {
				t.Fatal("no enforcement recognised")
			}
			if got.Kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", got.Kind, tt.wantKind)
			}
		})
	}
}

// The quiet window after a turn is drawn from the upper half of its bound, so
// the 4s default reads as 2-4s and never as a constant interval, which would
// be a scheduler's signature rather than a person's.
func TestDeepSeekTurnGapDrawsFromTheUpperHalf(t *testing.T) {
	upper := 4 * time.Second
	for range 100 {
		got := deepSeekTurnGapFn(upper)
		if got < upper/2 || got > upper {
			t.Fatalf("gap = %s, want within [%s, %s]", got, upper/2, upper)
		}
	}
	if got := deepSeekTurnGapFn(0); got != 0 {
		t.Errorf("gap(0) = %s, want 0: a disabled gap must not delay the release", got)
	}
}

// A zero gap frees the slot on the spot, and the freed slot must be usable
// again: the quiet window narrows the account, it must not break it.
func TestDeepSeekReleaseAfterTurnWithNoGapFreesTheSlot(t *testing.T) {
	gate := &deepSeekGate{slots: map[string]chan struct{}{}}
	release, err := gate.acquire(context.Background(), "account-gap", 1, 0)
	if err != nil {
		t.Fatalf("slot: %v", err)
	}
	done := make(chan struct{})
	go func() {
		deepSeekReleaseAfterTurn(release, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a zero gap must release without waiting on a timer")
	}
	next, err := gate.acquire(context.Background(), "account-gap", 1, 0)
	if err != nil {
		t.Fatalf("the slot must be free again after the release: %v", err)
	}
	next()
}

// Retired sessions leave in a batch, on the flush, and never in the same
// breath the turn that created them: an instant create-then-delete is the
// scripted cadence the queue exists to unsend.
func TestDeepSeekRetiredSessionsDrainOnTheFlush(t *testing.T) {
	t.Cleanup(deepSeekSessions.reset)
	upstream := newDeepSeekFakeUpstream(t, deepSeekTurnFrames(1, "ok"))
	server := httptest.NewServer(upstream)
	defer server.Close()

	// Stateless turns — session persistence off — so each session retires
	// when its turn ends. With persistence on the same opening message would
	// pin the two turns to one session, which is the anti-mute behavior its
	// own tests cover; the queue test needs sessions that actually retire.
	for range 2 {
		req := &Request{
			Ctx:    context.Background(),
			Client: server.Client(),
			Config: deepSeekTestConfig(server.URL),
			APIKey: "user-token-flush",
			Body:   deepSeekTestBody(t, "deepseek-chat", map[string]any{"role": "user", "content": "hi"}),
			ConnData: map[string]any{
				"persistSession": false,
			},
		}
		if err := forwardTurn(t, req); err != nil {
			t.Fatalf("turn: %v", err)
		}
	}
	if got := upstream.countCalls("/api/v0/chat_session/delete"); got != 0 {
		t.Fatalf("session deletes = %d, want 0 before the flush: the delete is the signal", got)
	}

	deepSeekFlushSessionDeletes(context.Background())
	if got := upstream.countCalls("/api/v0/chat_session/delete"); got != 2 {
		t.Errorf("session deletes = %d, want 2 after the flush drained the queue", got)
	}
}

// --- helpers -------------------------------------------------------------

// deepSeekTurnRequest builds a request aimed at the fake upstream, naming the
// client conversation the turn belongs to.
func deepSeekTurnRequest(server *httptest.Server, token, conversation string, body []byte) *Request {
	return &Request{
		Ctx:       context.Background(),
		Client:    server.Client(),
		Config:    deepSeekTestConfig(server.URL),
		APIKey:    token,
		Body:      body,
		SessionID: conversation,
	}
}

func forwardTurn(t *testing.T, turn *Request) error {
	t.Helper()
	return ForwardDeepSeekWeb(httptest.NewRecorder(), turn)
}

// retryAfterOf reads the wait an enforcement named, which is what the router's
// cooldown path reads.
func retryAfterOf(err error) string {
	var upstreamErr *proxy.UpstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr.Header == nil {
		return ""
	}
	return upstreamErr.Header.Get("Retry-After")
}
