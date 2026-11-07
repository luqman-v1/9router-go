### fix(media): route cloudflare-ai image models to the /ai/run endpoint

All Cloudflare Workers AI image requests were forwarded to the OpenAI-shaped
`/ai/v1/images/generations` URL the vendor does not serve — even after the
`{accountId}` fix (PR #865) landed, every `/v1/images/generations` call to a
`cloudflare-ai`/`cf` model came back `400 code 7000 "No route for that URI"`
(issue #864). FLUX.2 models were only special-cased when a reference image was
present on the direct path; `flux-2-klein-9b` without one, plus every
flux-1-schnell / stable-diffusion model, fell through to the broken generic
forwarder.

Mirrors upstream `decolua/9router` `open-sse/handlers/imageProviders/cloudflareAi.js`:

- **Dedicated lane** (`internal/handlers/media/cloudflare_ai.go`): any
  cloudflare-ai/cf image model now posts to `POST
  /accounts/<accountId>/ai/run/<model>`, derived from the chat BaseURL after
  `getProviderConfig` has substituted the account id (or the connection's custom
  base URL verbatim).
- **Body shape per model**: FLUX.2 (`flux-2-dev/-klein-4b/-klein-9b`) require
  `multipart/form-data` even for text-only prompts; the older text-to-image
  models take a JSON body. Dimensions come from `size` or `width`/`height`;
  the FLUX.1 Schnell closed schema still rejects dimensions (upstream #5373).
- **Reference images**: URL inputs are downloaded (SSRF-guarded) and sent
  base64 as `image_b64`; the mask follows the same path.
- **Response normalization**: Cloudflare's `{result:{image}}` / `{result:"url"}`
  / raw-binary shapes are normalized to the OpenAI images shape `{created,
  data:[{b64_json}|{url}]}` the client already expects.
- The dead flux-2 reference-image forwarder, `isFluxSchnellModel` /
  `isCloudflareFlux2Model` matching, and the generic-path dimension-stripping
  blocks are removed.

**Verified**: `go build ./cmd/9router-go/` OK; `go vet
./internal/handlers/media/` clean; `go test ./internal/handlers/media/` passes
including new `cloudflare_ai_test.go` coverage (flux-1-schnell JSON run,
flux-2-klein-9b multipart run, reference-image pass-through, response
normalization). The only `go test ./...` failures are pre-existing box-local
ones in `internal/config` and `internal/handlers/dashboard` (a stray `.env` and
a persisted dashboard password at `E:\project\database\9router\db\data.sqlite`),
unchanged by this diff.
