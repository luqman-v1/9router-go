### fix(codex-image): fall back to gpt-5.6-terra when the account rejects gpt-6-sol (#1036)

Codex image generation requests using `gpt-6-sol` as the `image_generation`
tool host can be rejected by certain ChatGPT accounts with HTTP 400 "The
'gpt-6-sol' model is not supported when using Codex with a ChatGPT account."

- Detect HTTP 400 with message containing "The 'gpt-6-sol' model is not supported"
  or similar ChatGPT rejection and automatically retry with `gpt-5.6-terra`.
- Preserve other 400 and 5xx error responses without triggering fallback.
- Mirrors upstream decolua/9router PR #5546.
- Tests: `TestIsCodexGpt6SolUnsupported` and `TestHandleImages_Codex_Gpt6SolFallback`
  in `internal/handlers/media/codex_image_test.go`.
