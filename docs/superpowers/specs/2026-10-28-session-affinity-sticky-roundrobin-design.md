# Session Affinity Sticky Round-Robin Design

**Date:** 2026-10-28  
**Status:** Approved / Ready for Plan  
**Target:** 9router-go (`internal/handlers/chat`, `internal/handlerutil`, `internal/db`)

---

## 1. Intent & The Why

Multi-account usage on provider backends like ChatGPT Codex OAuth, OpenCode, and Anthropic Claude is common for developers running multiple simultaneous agents across different workspaces or terminal windows (e.g., Repo 1 in Terminal A, Repo 2 in Terminal B).

Upstream backends isolate prompt prefix caches per account/organization workspace. In traditional per-request round-robin or consecutive-request sticky rotation (`StickyLimit`), turns in multi-turn conversations alternate across different physical accounts. This causes:
- Continuous cache misses on alternating accounts.
- Severe token quota burn (cache hit drops from >90% to <35%).
- Unintended latency spikes from re-tokenizing large context windows on every turn.

**Session Affinity Sticky Round-Robin** solves this by pinning each distinct conversation session (`session_id`) to a single upstream connection account for the duration of the session, while distributing independent sessions evenly across available active accounts in round-robin sequence.

---

## 2. Goals, Non-Goals & Anti-Goals

### Goals
- **High Prompt Cache Parity (>85-95%):** Ensure subsequent turns in the same conversation hit the exact same upstream account prefix cache.
- **Fair Workload Balancing Across Accounts:** Distribute different sessions across multiple active accounts in rotation order (e.g., Session 1 $\rightarrow$ Account A, Session 2 $\rightarrow$ Account B, Session 3 $\rightarrow$ Account A).
- **Graceful Failover:** When an assigned account becomes exhausted, encounters HTTP 429, or enters cooldown, smoothly re-bind the active session to the next available eligible account in order without dropping the client turn.
- **Support Multiple Client Formats:** Reliably extract session identifiers from Claude Code CLI, OpenCode CLI/desktop, Amp, and standard API clients.
- **Zero Memory Leaks:** Bound in-memory affinity tables with inactivity TTLs and capacity caps.

### Non-Goals
- **Global Cross-Instance Distributed Sync:** The affinity store is process-local in-memory (matching 9router-go's single-daemon architecture). It does not require Redis or external coordination.
- **Sticky Locking to Failing Accounts:** The router will not hold a session on an account that cannot serve the requested model or is actively rate-limited.

### Anti-Goals
- **Leaking Context Across Sessions:** Never merge or share prompt payloads, session headers, or cache keys between different client sessions.
- **Blocking or Degrading Non-Session Traffic:** Requests without any identifiable session ID must continue using standard request-level rotation without regression.

---

## 3. Constraints
- **Zero External Dependencies:** Use standard Go stdlib (`sync`, `time`).
- **Thread Safety:** High concurrency under streaming and concurrent multi-tool turns.
- **Parity with Upstream 9router Specification:** Maintain compatibility with existing `ProviderStrategy` settings and DB schema.

---

## 4. Architecture & Technical Design

### A. Session Identification Layer
The router resolves the incoming session ID from both headers and request envelopes:
1. **HTTP Headers (`handlerutil.ExtractSessionID`):**
   - `x-claude-code-session-id`
   - `x-session-id`
   - `session-id`
   - `session_id`
   - `x-amp-thread-id`
   - `x-opencode-session` / `x-opencode-session-id`
   - `x-session-affinity`
2. **Context & Body Extraction (`handlerutil.WithSessionID`):**
   - Extract from Claude Code body metadata (`claudeSessionIDFromBody`) when headers are stripped or proxied.
   - Attach cleanly to `r.Context()` on gateway entry (`chat.go`, `responses.go`).

### B. In-Memory Session Affinity Table (`SessionAffinityStore`)
Create a dedicated concurrency-safe session store:
```go
type SessionAffinityStore struct {
    mu       sync.RWMutex
    bindings map[string]*sessionBinding // key: provider + ":" + modelFamily + ":" + sessionID
    maxSize  int
    ttl      time.Duration
}

type sessionBinding struct {
    Provider     string
    ModelFamily  string
    SessionID    string
    ConnectionID string
    AssignedAt   time.Time
    LastSeenAt   time.Time
}
```
- **Key Scoping (Builder Decision):** Key is scoped by `provider + ":" + modelFamily + ":" + sessionID`. This prevents eviction ping-pong when an agent alternates between large reasoning models (e.g. Sonnet/Opus) and lightweight tool models (e.g. Haiku) where accounts may have different model grants or strict model assignments.
- **TTL Expiration:** Default 2 hours of inactivity.
- **Capacity Bound:** Default 10,000 concurrent sessions. Evicts oldest inactive entries when reaching capacity.
- **Lookup & Mutation Operations:**
  - `Get(provider, modelFamily, sessionID string) (connectionID string, ok bool)`
  - `Bind(provider, modelFamily, sessionID, connectionID string)`
  - `Evict(provider, modelFamily, sessionID string)`

### C. Connection Selection & Lifecycle Hooks

```
                  ┌───────────────────────────────┐
                  │ Request arrives with Context  │
                  └──────────────┬────────────────┘
                                 │
                     Has SessionID in Context?
                     ┌───────────┴───────────┐
                    YES                      NO
                     │                       │
      Check SessionAffinityStore       Standard fallback
      Is bound connection eligible?   (selectByRecency / priority)
      (Active, supports model,
       not in cooldown/lock)
           ┌─────────┴─────────┐
          YES                  NO
           │                   │
    Use bound conn       Evict binding.
                         Pick next eligible conn in order (session round-robin).
                         Optimistically bind session to new conn.
```

1. **Optimistic Binding:** Bind at connection selection time so concurrent parallel tool calls originating from the same new session route to the same account immediately.
2. **Failover Re-binding (`internal/handlers/chat/fallback.go`):**
   - If an account encounters terminal upstream errors (HTTP 429 Rate Limit, HTTP 401 Expired Grant, HTTP 403 Forbidden, or enters cooldown), `fallback.go` evicts the binding and updates `SessionAffinityStore` with the winning fallback connection.
   - Client-side input errors (HTTP 400 Context Length / Validation) and transient connection drops do NOT evict the warm session binding.
3. **Session Distribution vs DB Writes:**
   - New session distribution uses round-robin with effective `stickyLimit = 1` across candidate accounts so distinct sessions alternate evenly.
   - Subsequent turns on an existing bound session update in-memory `LastSeenAt` without performing redundant disk writes to SQLite on every turn.

---

## 5. What's Left to the Builder
- Implementing `SessionAffinityStore` in `internal/handlers/chat` or `internal/handlerutil`.
- Integrating session affinity check into `getBestConnectionWithContext`.
- Adding failover re-binding hook in `internal/handlers/chat/fallback.go`.
- Updating `SessionHeaderKeys` in `internal/handlerutil/response.go` to include `x-opencode-session`.
- Unit tests validating:
  - Multi-session distribution (Session 1 $\rightarrow$ Acc A, Session 2 $\rightarrow$ Acc B).
  - Multi-turn cache consistency (Turn 1, 2, 3 of Session 1 stay on Acc A).
  - Failover when Acc A encounters 429/cooldown $\rightarrow$ re-routes Session 1 to Acc B.
  - Multi-model isolation within same session (Sonnet on Acc A, Haiku on Acc B without thrashing).
