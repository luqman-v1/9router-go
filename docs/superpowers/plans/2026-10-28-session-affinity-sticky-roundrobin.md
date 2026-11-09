# Session Affinity Sticky Round-Robin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement Session Affinity Sticky Round-Robin in 9router-go to bind client conversation sessions (`session_id`) to upstream accounts, maximizing prompt cache hit rate across multi-turn sessions while balancing load across multiple accounts.

**Architecture:** A concurrent in-memory `SessionAffinityStore` with inactivity TTL and capacity bounds maps `(provider, modelFamily, sessionID)` tuples to active connection IDs. The connection selector `getBestConnectionWithContext` checks this store when rotation is enabled, serving subsequent turns from the pinned connection when eligible and alternating new sessions across accounts. On terminal upstream errors (HTTP 429, 401, cooldown), `fallback.go` evicts the failed binding and re-binds to the winning fallback connection.

**Tech Stack:** Go stdlib (`sync`, `time`, `strings`, `net/http`), existing 9router SQLite persistence & db types.

**Spec:** `docs/superpowers/specs/2026-10-28-session-affinity-sticky-roundrobin-design.md`

## Global Constraints
- Zero external dependencies: use Go stdlib only.
- Strict thread safety: all store mutations protected by mutexes.
- Non-session requests must continue functioning without regression.
- In-memory store capped at 10,000 entries and 2-hour inactivity TTL.

## Review Focus
1. **Multi-Model Thrashing:** A session using both Opus/Sonnet and Haiku does not ping-pong evict connections; `modelFamily` key scoping prevents cross-model cache fragmentation.
2. **Concurrent Request Race on New Session:** Parallel tool calls on turn 1 of a new session both route to the same account via optimistic selection-time binding.
3. **Failover on Rate Limit (429):** When an account hits 429 or enters cooldown, the binding is evicted and re-bound to the next eligible connection without failing the client request.
4. **Non-Session Traffic Unaffected:** Requests missing session headers fall through to standard round-robin rotation without populating affinity tables.
5. **Memory Leak Prevention:** Expired entries older than 2 hours are pruned, and reaching 10,000 entries triggers oldest-inactive eviction.

---

### Task 1: Add OpenCode and Affinity Headers to `handlerutil.ExtractSessionID`

**Files:**
- Modify: `internal/handlerutil/response.go:183-205`
- Test: `internal/handlerutil/handlerutil_test.go:280-320`

**Interfaces:**
- Consumes: `r.Header` from `*http.Request`
- Produces: `ExtractSessionID(r *http.Request) string` recognizing `x-opencode-session`, `x-opencode-session-id`, `x-session-affinity`

- [ ] **Step 1: Write failing test for new session headers in `internal/handlerutil/handlerutil_test.go`**

```go
func TestExtractSessionID_OpenCodeAndAffinityHeaders(t *testing.T) {
	req1, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req1.Header.Set("x-opencode-session", "ses_opencode_12345")
	if got := ExtractSessionID(req1); got != "ses_opencode_12345" {
		t.Errorf("ExtractSessionID(x-opencode-session) = %q, want ses_opencode_12345", got)
	}

	req2, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req2.Header.Set("x-session-affinity", "aff_9988")
	if got := ExtractSessionID(req2); got != "aff_9988" {
		t.Errorf("ExtractSessionID(x-session-affinity) = %q, want aff_9988", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/handlerutil -run TestExtractSessionID_OpenCodeAndAffinityHeaders`  
Expected: FAIL ("want ses_opencode_12345")

- [ ] **Step 3: Update `SessionHeaderKeys` in `internal/handlerutil/response.go`**

Add `"x-opencode-session"`, `"x-opencode-session-id"`, and `"x-session-affinity"` to `SessionHeaderKeys`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/handlerutil -run TestExtractSessionID`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlerutil/response.go internal/handlerutil/handlerutil_test.go
git commit -m "feat(handlerutil): extract opencode and session-affinity headers"
```

---

### Task 2: Implement `SessionAffinityStore`

**Files:**
- Create: `internal/handlers/chat/session_affinity.go`
- Test: `internal/handlers/chat/session_affinity_test.go`

**Interfaces:**
- Produces:
  - `NewSessionAffinityStore(maxEntries int, ttl time.Duration) *SessionAffinityStore`
  - `(s *SessionAffinityStore) Get(provider, model, sessionID string) (connectionID string, ok bool)`
  - `(s *SessionAffinityStore) Bind(provider, model, sessionID, connectionID string)`
  - `(s *SessionAffinityStore) Evict(provider, model, sessionID string)`
  - `(s *SessionAffinityStore) EvictConnection(connectionID string)`

- [ ] **Step 1: Write failing test in `internal/handlers/chat/session_affinity_test.go`**

```go
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

	// Multi-model separation within same session
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
	store.Bind("codex", "m1", "s2", "c2")

	// Capacity eviction: adding 3rd entry evicts oldest
	store.Bind("codex", "m1", "s3", "c3")
	if _, ok := store.Get("codex", "m1", "s1"); ok {
		t.Error("s1 should have been evicted when exceeding capacity 2")
	}

	// TTL expiration
	time.Sleep(60 * time.Millisecond)
	if _, ok := store.Get("codex", "m1", "s3"); ok {
		t.Error("s3 should expire after TTL")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/handlers/chat -run TestSessionAffinityStore_`  
Expected: FAIL (undefined `NewSessionAffinityStore`)

- [ ] **Step 3: Implement `internal/handlers/chat/session_affinity.go`**

Implement `SessionAffinityStore` struct with `RWMutex`, `map[string]*sessionBinding`, `maxEntries`, `ttl`, `modelFamily(model)` grouping, and cleanup methods.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/handlers/chat -run TestSessionAffinityStore_`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/session_affinity.go internal/handlers/chat/session_affinity_test.go
git commit -m "feat(chat): implement session affinity store with TTL and capacity bounds"
```

---

### Task 3: Integrate Session Affinity into `getBestConnectionWithContext`

**Files:**
- Modify: `internal/handlers/chat/types.go:34-60` (add `sessionAffinity *SessionAffinityStore` to `ChatHandler`)
- Modify: `internal/handlers/chat/resolution.go:24-60` (initialize store in `NewChatHandler`)
- Modify: `internal/handlers/chat/connections.go:350-410` (session lookup, eligibility check, fallback, optimistic binding)
- Test: `internal/handlers/chat/session_rotation_test.go`

**Interfaces:**
- Consumes: `SessionAffinityStore`, `handlerutil.GetSessionID(ctx)`, `strat.RotateStrategy`
- Produces: Connection selection pinned to active session ID when rotation strategy is `"round-robin"`, `"roundrobin"`, `"session-sticky"`, or `"sticky"`.

- [ ] **Step 1: Write failing test in `internal/handlers/chat/session_rotation_test.go`**

```go
func TestGetBestConnection_SessionAffinityPinning(t *testing.T) {
	// Setup repo with 2 active codex connections (conn-1, conn-2)
	// Set ProviderStrategy to round-robin
	// Call getBestConnectionWithContext with session-A turn 1 -> returns conn-1
	// Call getBestConnectionWithContext with session-B turn 1 -> returns conn-2 (round-robin alternating)
	// Call getBestConnectionWithContext with session-A turn 2 -> returns conn-1 (pinned to conn-1!)
	// Call getBestConnectionWithContext with session-B turn 2 -> returns conn-2 (pinned to conn-2!)
}

func TestGetBestConnection_SessionAffinityCooldownFailover(t *testing.T) {
	// Pinned session-A on conn-1
	// Put conn-1 into cooldown
	// Call getBestConnectionWithContext with session-A -> evicts conn-1 and re-binds to conn-2
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/handlers/chat -run TestGetBestConnection_SessionAffinity`  
Expected: FAIL

- [ ] **Step 3: Implement selection integration in `connections.go`**

1. In `ChatHandler`: ensure `h.sessionAffinity` is initialized (lazy init fallback if nil).
2. In `getBestConnectionWithContext`:
   - Extract `sessionID := handlerutil.GetSessionID(ctx)`.
   - If `sessionID != ""` and rotation is enabled:
     - Check `boundID, ok := h.sessionAffinity.Get(provider, model, sessionID)`.
     - If `ok`: find `boundConn` in candidate list. Verify: `!excludeSet[boundConn.ID]`, not in cooldown, model supported.
     - If eligible: return `boundConn` directly (updating `LastSeenAt`).
     - If ineligible: call `h.sessionAffinity.Evict(provider, model, sessionID)` and fall through to rotation.
   - If no valid binding: run rotation to pick connection, then `h.sessionAffinity.Bind(provider, model, sessionID, conn.ID)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/handlers/chat -run TestGetBestConnection_SessionAffinity`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/types.go internal/handlers/chat/resolution.go internal/handlers/chat/connections.go internal/handlers/chat/session_rotation_test.go
git commit -m "feat(chat): route requests via session affinity store in getBestConnection"
```

---

### Task 4: Failover Re-Binding in `fallback.go`

**Files:**
- Modify: `internal/handlers/chat/fallback.go:900-1050`
- Test: `internal/handlers/chat/session_failover_test.go`

**Interfaces:**
- Consumes: HTTP upstream error status codes, `SessionAffinityStore`
- Produces: Eviction of failing connection on HTTP 429/401/403/cooldown and re-binding to winning fallback connection.

- [ ] **Step 1: Write failing test in `internal/handlers/chat/session_failover_test.go`**

```go
func TestFallback_RebindsAffinityOn429(t *testing.T) {
	// Setup fake servers: srv1 returns 429 Too Many Requests, srv2 returns 200 OK
	// Session X initially bound to srv1
	// Dispatch request through fallback loop
	// Verify request succeeds via srv2
	// Verify SessionAffinityStore now has Session X bound to srv2
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -v ./internal/handlers/chat -run TestFallback_RebindsAffinityOn429`  
Expected: FAIL

- [ ] **Step 3: Implement re-binding hook in `fallback.go`**

In `fallback.go` around connection error handling:
- If upstream status is HTTP 429, 401, 403, or connection is put into cooldown:
  `h.sessionAffinity.Evict(provider, model, sessionID)`
- When fallback connection succeeds (HTTP 200 OK or streaming header):
  `h.sessionAffinity.Bind(provider, model, sessionID, winningConn.ID)`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -v ./internal/handlers/chat -run TestFallback_RebindsAffinityOn429`  
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/fallback.go internal/handlers/chat/session_failover_test.go
git commit -m "fix(chat): evict and re-bind session affinity on upstream rate limit failover"
```

---

### Task 5: End-to-End Test Suite Verification & Benchmarking

**Files:**
- Test: `internal/handlers/chat/session_affinity_e2e_test.go`

**Interfaces:**
- Validates:
  - 10 concurrent simulated terminal sessions across 3 accounts maintain >90% sticky hit rate.
  - Non-session calls rotate normally without errors.
  - Zero memory leaks under 1,000 rapid sequential sessions.

- [ ] **Step 1: Write end-to-end multi-terminal test scenario**

```go
func TestSessionAffinity_MultiTerminalSimulation(t *testing.T) {
	// Simulate Terminal 1 (Repo A) sending 5 turns
	// Simulate Terminal 2 (Repo B) sending 5 turns
	// Verify Terminal 1 consistently hits Account 1
	// Verify Terminal 2 consistently hits Account 2
	// Verify no crosstalk or cache key pollution
}
```

- [ ] **Step 2: Run full chat test package**

Run: `go test -v ./internal/handlers/chat/...`  
Expected: ALL PASS

- [ ] **Step 3: Run full repository test suite**

Run: `go test ./...`  
Expected: ALL PASS (100% PASS)

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/chat/session_affinity_e2e_test.go
git commit -m "test(chat): add multi-terminal simulation test for session affinity"
```
