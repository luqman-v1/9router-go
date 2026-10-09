### Security

- **Per-API-key access control now covers every inference endpoint, not just chat.** The chat lane had the allowlist gate; ten media endpoints sat behind `RequireApiKey` with no policy check at all, so a key restricted to one model was authenticated and then served unconditionally by embeddings, image generation, TTS, STT, video generations/edits/extensions, search and scrape. All ten now return **403** for a model outside the allowlist, through the same decision function the chat lane uses, so a model an operator allowed is the same model a client can reach on every route.

  TTS is gated in `forwardTTSRequest` rather than the shared funnel: its `edge-tts` and `google-tts` branches synthesise locally and never reach `forwardMediaRequest`, so gating only the funnel would have left those two paths open.

- **The capacity-adapter pool can no longer route around the allowlist.** The pool is invisible to the gate that admitted the request, so a key restricted to one model could still have its traffic silently rerouted to a pool model it was never granted. A pool model the key may not dispatch is no longer injected.

### Fixes

- The per-key model allowlist now survives a database backup and restore. It lives in its own table, which the export never selected and the import never wrote back, so restoring a backup silently widened every restricted key back to allow-all — the one outcome the restriction exists to prevent. The import also clears the table, so access rows no longer outlive their key as orphans.
- The allowlist is bounded at 200 entries and 256 characters per entry, matching upstream `validateKeyAccessInput`. Every entry is matched against every candidate id on every request, so an unbounded list was a load a single dashboard call could set up. The rejection message names the limit and truncates the offending pattern.

### Known divergences

Go stores an allowlist as one row per `(key, pattern)` in a dedicated table; upstream stores two columns on `apiKeys`. Go's empty allowlist therefore means allow-all, and "restricted but allow nothing" is not expressible. This predates this change and is a deliberate design decision, not a regression.