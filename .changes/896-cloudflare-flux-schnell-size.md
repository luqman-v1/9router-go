### fix(cloudflare-ai): don't send width/height to FLUX.1 Schnell

- Strip `width`, `height`, and `size` parameters on Cloudflare AI requests for `@cf/black-forest-labs/flux-1-schnell`.
- Prevent HTTP 400 (code 5006) error caused by closed input schema on Cloudflare FLUX.1 Schnell.
- Remove `size` from `params` for `flux-1-schnell` in `web/src/lib/models.ts`.
- Verified with unit tests in `internal/handlers/media/multimodal_test.go` (upstream `decolua/9router#5373` parity).
