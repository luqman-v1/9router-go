### fix(usage): token fallback for input_tokens rows and stable key masking in stats

Upstream decolua/9router commit 813a0121 (PRs #3640, #2918).

- **Fallback to `input_tokens` and `output_tokens`**: live usage stats (today/24h) and recent requests now read token counts from the `tokens` JSON blob when rows are recorded with `input_tokens` or `output_tokens` (such as embeddings, Gemini, or Claude-format responses where the integer column was 0).
- **Whitespace trimming in API key masking**: `handlerutil.MaskAPIKey` trims surrounding whitespace before masking so whitespace variants mask consistently.
- **Masked key display fallback**: `apiKeyMap` / `keyNames` lookup trims whitespace, and unknown keys fall back to displaying their masked key rather than a raw prefix, keeping the grouping key on the full API key.
- **`apiKeyKey` masking parity**: `byApiKey[*].apiKeyKey` now returns the masked key instead of echoing the raw API key (upstream `apiKeyKey: apiKeyMasked || "local-no-key"` parity).
- **Short key masking in stats**: keys with <= 12 characters mask to `firstChar + "***"` instead of falling back to raw keys.
- **Timezone-safe regression test**: anchored to local midnight with a 1-hour boundary guard so `period=today` passes consistently regardless of local timezone.
