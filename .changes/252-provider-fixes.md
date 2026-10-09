### Fixes

- **Gemini**: duplicate `tool_call_id`s are now rewritten to unique ids before the request goes out. Gemini validates `functionCall` id uniqueness across the *whole* history and rejects the entire request with 400 `INVALID_ARGUMENT` when one repeats, while an OpenAI `tool_call_id` is only unique within its own assistant turn — so a long agent session can legitimately reuse one. Uniqueness is per *occurrence*, not per id: two calls sharing an id end up with two different emitted ids, and each result answers the call it belongs to instead of cross-wiring the loop. Ids that were already unique pass through untouched. Upstream `#4532`.

  The rewrite keys on the id with this gateway's `__ts__` thought-signature suffix stripped, so the private transport encoding still never reaches the wire.

- **Ollama**: `prompt_eval_cached_count` is recorded as cached tokens. Ollama reports prompt tokens cache-*inclusive* — the same convention as OpenAI and Gemini — so the value is recorded, never subtracted. Without it every Ollama turn showed a zero cache hit rate and the cache analytics under-reported savings. Read both from the live response (whose counters sit at the top level, not under `usage`) and from persisted usage blobs, which is where the historical rows are re-read from. Upstream `46627249`.

## Known gaps

These upstream fixes in the same range are **not** in this PR, each because it needs a subsystem this port does not have rather than because it was small:

- **ElevenLabs Scribe STT** (`2f827bcf`, ~100 lines upstream) is a full provider transport — multipart POST, `xi-api-key` auth, `additional_formats` extraction for srt/vtt/seg_json, and diarize/num_speakers arbitration. It is not the data-only model row it first appears to be.
- **Codex image usage** (`e10da160`, +40 lines) is a usage-accounting fix *inside* an image-generation adapter registry. This port has no Codex image lane at all: its Codex image models are catalogued but unroutable. The fix cannot land before the subsystem does.
- **Cursor** (`18ad1a89`, `cbd594ac`) needs a CONNECT-RPC-over-HTTP/2 protobuf executor, which this port does not have — only a config entry exists. It is *portable* with no new dependency: the stdlib HTTP/2 client is already negotiated repo-wide, and `windsurf.go` already has a varint + frame codec byte-identical in framing to upstream's. It is roughly 1000 lines of new executor, so it belongs in its own PR.
- **Kimi Responses transport** (`3125ac2b`) needs a second URL per provider, which `ProviderConfig` has no field for, plus an executor that owns lane selection.