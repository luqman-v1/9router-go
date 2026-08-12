# Design Spec: Restore Gemini thought_signature, Turn-aware Combo, and Nested-Combo Flatten

## 1. Overview

Three coordinated fixes for two live errors the combo (`combo-wombo`) has been hitting:

1. **`400 Function call is missing a thought_signature in functionCall parts`** — Gemini thinking models reject request history whose `functionCall` parts carry no `thoughtSignature`.
2. **`429 · all connections for this provider are rate-limited`** — a synthetic error the proxy returns when every connection of a provider is locked; caused here by a combo that effectively never rotates and hammers one account.

| # | Change | File(s) | Purpose |
|---|--------|---------|---------|
| A | Restore `thoughtSignature` transport (regression fix) | `internal/translator/gemini.go` | Fixes the 400 at the wire level |
| B | Turn-aware combo stickiness | `internal/handlers/chat/combo.go` | Prevents mid-turn provider switch to a Gemini thinking model (400) |
| C1 | Flatten nested combos | `internal/handlers/chat/resolution.go` | Makes rotation actually work → fixes the 429 |

---

## 2. A — Restore `thoughtSignature` transport

### 2.1 Background

The Gemini native `generateContent`/`streamGenerateContent` API places the thinking signature at the **part level**, as a sibling of `functionCall`:

```json
{
  "role": "model",
  "parts": [
    { "functionCall": { "name": "ScheduleWakeup", "args": {} }, "thoughtSignature": "<sig>" }
  ]
}
```

When replaying history, the client must echo the signature back in the **same part it was received**. A missing signature in the current turn's first `functionCall` part → HTTP 400.

Commit `44ef587` fixed this correctly (field JSON tag → camelCase `thoughtSignature`, and a `__ts__<sig>` round-trip encoded inside tool-call IDs). Commit `374b3fa` ("feat(combo)") **silently reverted it** in the same file: the tag went back to `thought_signature` (snake) and the `Thought` field on `GeminiFunctionCall` was removed. Result:

- **Write direction broken** — the translator emits `"thought_signature": "..."` at part level; the native API expects `"thoughtSignature"`. The unknown field is ignored → part is treated as unsigned → 400.
- **Read direction broken** — `GeminiPart.UnmarshalJSON` only captures snake_case, but real responses carry camelCase, so model-produced signatures are dropped before they can be encoded into `__ts__`.

Tests pass today only because the fixtures mirror the (wrong) snake_case wire format.

### 2.2 Change

In `internal/translator/gemini.go`:

- `GeminiPart.ThoughtSignature` tag: `json:"thought_signature,omitempty"` → `json:"thoughtSignature,omitempty"`.
- Keep `UnmarshalJSON` dual-read as-is — with the camel tag, the embedded `*Alias` reads camelCase from responses and the explicit `aux.ThoughtSignatureSnake` still reads snake_case. No code change needed there.
- Do **not** re-add `Thought` inside `GeminiFunctionCall` (wrong location per the API contract; part-level is correct).

### 2.3 Tests (`internal/translator/gemini_test.go`)

- Update fixtures that assert the wrong snake_case part field → assert `thoughtSignature`.
- Add a **full round-trip test**: Gemini response (`functionCall` + `thoughtSignature`) → `TranslateGeminiResponseToOpenAI` → tool-call ID carries `__ts__<sig>` → back through `TranslateOpenAIToGemini` → emitted as part-level `thoughtSignature` again. This is the property that was actually broken.

---

## 3. B — Turn-aware combo stickiness

### 3.1 Problem

`ApplyComboStrategy` with `round-robin` (a.k.a. sticky limit 1) advances to the next model on **every request**. Claude Code's tool-use loop issues consecutive requests with no plain-text user message in between — i.e. mid-turn. If the combo rotates from a non-Gemini model to a Gemini thinking model mid-turn, the current-turn `functionCall` parts (made by the previous model) carry no signature, and Gemini 400s **even after fix A**, because a signature can only come from a Gemini response, never be invented.

### 3.2 Change

- Add helper `detectNewTurn(body []byte) bool`:
  - Scan `messages` (OpenAI format — both combo handlers already work on OpenAI-format bodies) from the last message backwards.
  - Last user-type message has `role: "tool"` → mid-turn (false).
  - Last `role: "user"` message whose `content` is a non-empty string or contains a `text` part → new turn (true).
  - No user message at all → true (treat as a new turn).
- `ApplyComboStrategy(strategy, models, comboName, stickyLimit, newTurn bool)`:
  - **`state.Index` and `state.ConsecutiveUseCount` only advance when `newTurn` is true.**
  - Mid-turn requests return the same rotated array (same lead model) → the provider does not change inside a turn.
  - Edge: mid-turn but no model has been chosen for this combo yet (fresh process) — treat as a new turn so the rotation can start.
- Call `detectNewTurn` in `handleComboFallback` and `handleMessagesComboFallback` and pass the result into `ApplyComboStrategy`. `handleFusion` passes `true` (each fusion fan-out is a fresh panel).

### 3.3 Tests (`internal/handlers/chat/combo_test.go`)

- Round-robin advances on a text-user-message request.
- Round-robin does **not** advance when the last message is `role:"tool"`.
- Rotation resumes on the next text message.
- Per-combo independence is preserved (existing test still passes).

---

## 4. C1 — Flatten nested combos

### 4.1 Problem (the 429)

Current DB:

```
combo-wombo ──> ["free-tier"]
free-tier   ──> ["oc/deepseek-v4-flash-free", "oc/ling-3.0-flash-free",
                 "nvidia/.../nemotron-3-ultra-550b-a55b", "gemini/gemini-3.5-flash"]
```

`resolveModel("combo-wombo")` correctly reaches into `free-tier` via `resolveModelEntry`, but then **overwrites** the expanded list at `resolution.go:152`:

```go
firstInfo.ComboModels = modelStrings // outer ["free-tier"], 1 element
```

So `handleComboFallback` receives `comboModels = ["free-tier"]`, `ApplyComboStrategy` early-returns on `len <= 1`, and the loop resolves `"free-tier"` to its **first** leaf model every time. The combo never rotates; one account (`oc/deepseek-v4-flash-free`) is hammered; its connections get locked by `comboLockRetryable`; the proxy then emits the synthetic 429.

### 4.2 Change

In `internal/handlers/chat/resolution.go`:

- Add `flattenComboModels(models []string) ([]string, error)` on `ChatHandler`: recursively replace combo-name entries with their own model lists; **dedupe identical consecutive leaf entries** (keeping first occurrence) so duplicates don't create pointless rotation slots; guard against cycles with a visited set.
- In `resolveModel`'s combo branch (lines ~128–157), set `ComboModels` to the flattened leaf list instead of the raw outer list. Keep `Strategy` = the top-level combo's strategy (the top-level strategy governs rotation over the flattened leaves; inner-combo strategies are organizational and ignored).
- `resolveModelEntry` needs no change — after flattening, fallback-loop entries are all concrete `provider/model`.

### 4.3 Tests (`internal/handlers/chat/resolution_test.go`)

- Update `TestResolveModel_ComboWithNestedComboName`: `["inner-only", "deepseek/deepseek-chat"]` now flattens+dedupes to `["deepseek/deepseek-chat"]` → `len(ComboModels) == 1`.
- Add a test for a nested combo whose inner list has multiple leaves → `ComboModels` = flattened leaves in order, no combo names remaining.
- Add a cycle-guard test (combo referencing itself or a cycle) → no infinite loop.

---

## 5. Housekeeping

- Delete `patch_combo.go` (stray throwaway `package main` patch script at repo root).

## 6. Out of Scope / Deferred

- **D (deferred):** retry the combo once (bounded wait honoring `Retry-After`) when every entry 429s, so a transient provider blip doesn't kill a client subagent. Not in this plan.
- Nested-combo inner strategies: only the top-level strategy is applied. If a future combo needs mixed strategies per group, that is a separate change.
- Pre-existing `/v1/v1/messages` doubled prefix in `chat.go:182` (unrelated to these errors) — noted, not fixed here.

## 7. Acceptance Criteria

- `go test ./...` passes.
- With `combo-wombo` selected, successive **new turns** advance rotation across `free-tier`'s four leaf models (the nested list is flattened and actually rotates).
- A multi-turn Gemini conversation whose history contains `functionCall` parts no longer returns the `400 thought_signature` error.
- A mid-turn tool-use sequence stays on the same provider/model.
- `patch_combo.go` is gone.
