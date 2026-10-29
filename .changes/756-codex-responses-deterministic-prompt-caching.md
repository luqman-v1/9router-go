### fix(codex): use deterministic JSON marshaling to preserve OpenAI prompt prefix caching

- Fix `buildResponsesBody`, `stripCodexUnsupportedTokenParams`, `rewriteCodexUpstreamModel`, and `applyCodexCompact` using default non-deterministic `json.Marshal` from `json/v2`, which randomized JSON map keys across turns and caused 100% prompt cache misses on OpenAI Codex backends, exhausting account token quotas rapidly.
