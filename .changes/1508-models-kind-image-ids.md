### Fixed

- `GET /v1/models/image` now lists each provider's actual image models as
  `{provider}/{model}` instead of the bare provider id. The bare id was not
  addressable: `POST /v1/images/generations` resolves `{provider}/{model}` and
  answers `400 could not resolve model: xai`, so the discovery listing could not
  be fed back into the endpoint it advertised. Entries keep the kind listing's
  existing shape (`id`, `object`, `kind`, `owned_by`, `endpoint`, `created`),
  which is what upstream `buildModelsList(["image"])` publishes.
- Added `providers.ImageModelsForProvider` and `providers.DefaultImageModel`,
  which detect image models by catalog marker (`imagen`, `image`, `dall-e`,
  `gpt-image`, `flux`, `stable-diffusion`, `seedance`, `seedream`, `recraft`, …)
  so a new image family is picked up without a second edit. A provider that
  declares an `ImageURL` but ships no recognisable image entry (a relay, a
  user-defined node) still gets an addressable `{provider}/{model}` id.
- Non-image kinds (`tts`, `stt`, `embedding`, `systemone`, `web`) keep their
  provider-level shape; only image generation needed the model segment.

Verification: `go test ./internal/providers/ ./internal/handlers/media/
./internal/handlers/chat/` passes. `TestHandleModelsByKind_ImageEmitsResolvableIDs`
drives the real handler and asserts every emitted id resolves through
`ResolveModel`; reverting `chat.go` makes it fail with the four unusable ids
(`sensenova`, `recraft`, `comfyui`, `huggingface`) it was written to catch. The
same test pins the wire field set: the response is rebuilt through
`kindEntriesFromModelInfo`, which projects each entry onto a fixed field list, so
a field added to the producer maps alone never reaches a client. Teaching the
projection to emit an extra key makes the test fail rather than silently
widening the response.
