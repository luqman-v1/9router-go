# Design Spec: Combo Strategy Sync & Gemini thought_signature Fix

## 1. Overview
This document outlines the design for two major improvements in `9router-go`:
1. **Combo Strategy Sync (Option A):** Syncing the intelligence of the Next.js combo strategy (per-combo state, safe auto-switch ordering, comprehensive capability detection) into Golang, while retaining Golang's advanced features like connection locking and nested combos.
2. **Gemini Proxy Fix:** Fixing the `400 Function call is missing a thought_signature` error encountered when Claude Code hits Gemini models through the router.

## 2. Gemini `thought_signature` Proxy Fix
**Problem:** Gemini APIs now require a `thought` or `thought_signature` property inside `functionCall` items in message history, especially for thinking models. The Anthropic translation logic in `internal/proxy/gemini.go` does not map or inject this field.
**Solution:**
- Update `ForwardGemini` (or the specific Anthropic-to-Gemini message converter) in `internal/proxy/gemini.go`.
- When converting historical assistant messages that contain `tool_use` (which maps to `functionCall`), inject a `thought_signature` field or ensure it gracefully handles missing thoughts by stripping or mocking the signature as required by the latest Gemini schema.

## 3. Combo Strategy Sync (Best of Both Worlds)

### 3.1. Per-Combo Round-Robin State
**Current:** `rrIdx` is a global counter.
**Change:** Introduce a thread-safe map `comboRotationState` mapping `comboName` (string) to `RotationState { Index int, ConsecutiveUseCount int }`. This ensures combo A's rotation doesn't advance combo B's rotation.

### 3.2. Auto-Switch Order
**Current:** `reorderByCapabilities` happens *before* applying the combo strategy.
**Change:** Swap the order in `handleComboFallback` and `handleMessagesComboFallback`. Execute the combo strategy (rotation/sticky) *first* to determine the base array, then apply `reorderByCapabilities` so model capabilities override rotation.

### 3.3. Capability Detection Enhancements
**Current:** Only supports `vision` and `pdf`.
**Change:**
- Add `audioInput` and `videoInput` to capability scanning.
- Change `reorderByCapabilities` to implement a 3-tier system (Tier 0: Hard+Soft, Tier 1: Hard Only, Tier 2: Fails requirements) as seen in Next.js.

### 3.4. Trailing-Turn Scan Scope
**Current:** Scans *all* messages in the history for capability requirements (like image URLs).
**Change:** Modify the `detectRequiredCapabilities` function to only scan the *trailing user turn* (the last user message). This prevents a combo from being permanently locked to a vision model just because an image was sent 10 turns ago.

### 3.5. Retained Golang Features
- **Connection Locking & Backoff:** Maintained.
- **Nested Combos:** Maintained.
- **Database Schema:** The `stickyLimit` and `strategy` fields remain in the DB schema for Golang.

## 4. Open Questions
- None. This design directly addresses the identified gaps and the user's explicit approval of Option A.
