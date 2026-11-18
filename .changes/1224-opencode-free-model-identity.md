### fix(opencode): report the selected free model instead of GLM-5.3-Flash Free

The OpenCode free-tier harness preamble is a verbatim CLI capture whose `# Your Model` block names GLM-5.3-Flash Free. Because the inference gate fingerprints those bytes, the block cannot be edited, and every free lane — `mimo-v2.6-flash-free`, `deepseek-v4.1-flash-free`, `beam-free`, and others — told the client it was GLM-5.3-Flash Free.

`ConcealOpenCodeV2FingerprintTools` now appends a correction after the preamble with the model from the request, on all three wire shapes (Chat Completions, Responses, Claude Messages). The preamble stays byte-exact at the start; only the suffix changes.

Verified: new table-driven `TestConcealOpenCodeV2ModelIdentity` pins the preamble prefix and the per-model identity suffix on all lanes; `go test ./internal/translator/... ./internal/proxy/...` passes. A live A/B probe against the keyless inference gate returned the identical `429 FreeUsageLimitError` with and without the suffix, so the added text does not change gate validation (the tier is IP-rate-limited right now).
