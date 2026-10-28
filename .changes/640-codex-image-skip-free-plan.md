### fix(codex-image): skip ChatGPT Free accounts and use gpt-5.6-terra as the main model

Codex image generation failed for every non-Plus/Pro setup:

- ChatGPT Free accounts get the image_generation tool stripped
  server-side, so upstream answers 400 "Tool choice 'image_generation'
  must be specified with 'tools' parameter". A 400 is not
  fallback-eligible, so the request died on the first Free account even
  when a paid account was available. The rotation now resolves the plan
  at request time (token claims first, then the stored
  chatgptPlanType, which goes stale on plan changes) and skips Free
  accounts without locking them, so they stay usable for chat; when no
  entitled account is left the request fails with an entitlement
  message. Unknown plans are allowed so claim-less accounts keep
  working.
- The main model answering the tool call moves from gpt-5.6-sol to
  gpt-5.6-terra, which is accepted for ChatGPT Go accounts where
  gpt-5.6-sol answers "model is not supported when using Codex with a
  ChatGPT account".

Mirrors upstream decolua/9router b08f12d5. Verified with the plan
resolution tests in `codex_image_plan_test.go` (token claims win over
the stale stored value, unknown plans stay allowed).
