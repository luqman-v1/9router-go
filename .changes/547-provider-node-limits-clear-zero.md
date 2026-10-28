### Bug — provider node capacity limits cannot be cleared to 0 (unlimited)

`ProviderDetailView.handleSaveEditedNode` gated the `PUT /api/provider-nodes/{id}/limits` call behind `hasAnyNodeLimitPatch`, which returns `false` when every field is 0 (because it checks `> 0`). An operator clearing all limits back to unlimited had the write silently skipped — the node kept its previous non-zero policy.

**Fix:** Remove the gate on the edit path. The edit modal always produces a complete patch (all five count fields + contractExpiresAt), so sending it on every save is correct: an explicit 0 clears the column via merge semantics, a non-zero value sets it, and an unchanged value is a harmless no-op. The add path in `ConnectionsView.handleCreateNode` keeps its gate (a freshly created node with all-zero limits already reads as unlimited by default).

**Files:**
- `web/src/components/connections/ProviderDetailView.svelte` — always call `setProviderNodeLimits`, remove `hasAnyNodeLimitPatch` gate; drop now-unused `hasAnyNodeLimitPatch` import
- `internal/db/provider_limits_test.go` — new `TestSetProviderLimits_AcceptsZeroAsUnlimited` proving the DB layer stores 0
- `internal/integration/provider_limits_test.go` — new `TestProviderNodeLimits_ClearedToZero` driving the full router, confirming an explicit all-zero patch reaches the DB

### Test results
- `go test ./internal/db/` — PASS
- `bun test src/lib/node-limits.test.ts` — 19 pass
- `bun run ratchet:svelte` — 0 unresolved identifiers, 78 errors (baseline 78)
- `go test -tags=integration ./internal/integration` (targeted) — PASS

Closes #546
