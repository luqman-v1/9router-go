### fix(claude): normalize final Claude assistant whitespace after compression

- Trim trailing whitespace from final assistant prefill message.
- Remove empty trailing text blocks resulting from trimming, preventing HTTP 400 from Anthropic.
- Verified with unit tests in `internal/translator/claude_whitespace_test.go` (upstream `decolua/9router#5415` parity).
