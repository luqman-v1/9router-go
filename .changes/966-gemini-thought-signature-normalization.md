### fix(translator): normalize corrupted thought signatures and purge on retry success

- Normalize Gemini thought signatures in `internal/translator/thought_signature_store.go` and `gemini.go` to catch double-base64 wrapping (`Q0FR...`) and oversized entries (> 32KB) that trigger upstream `400 INVALID_ARGUMENT: "Corrupted thought signature."`.
- Automatically fallback to `DefaultThinkingSignature` when corrupted signatures are detected.
- Purge in-memory thought signatures for the session via `translator.PurgeSessionThoughtSignatures(sessionID)` upon retry success in `ForwardGemini` so corrupted entries do not poison subsequent conversation turns.
