### Add POST /v1/multimodal-embeddings alias to /v1/embeddings

Adds `/multimodal-embeddings` and `/v1/multimodal-embeddings` as aliases for the embeddings handler, mirroring OmniRoute's `/v1/multimodal-embeddings` route (an alias of `/v1/embeddings` for clients that call Jina's multimodal path). Both spellings resolve to the same handler, so request forwarding and usage recording are identical. `kindFromEndpoint` now classifies both spellings as `embedding`, so connection eligibility and service-kind inference match `/v1/embeddings`.

- `internal/handlers/router.go`: registered `r.Post("/multimodal-embeddings", ...)` and `r.Post("/v1/multimodal-embeddings", ...)` next to `/embeddings`.
- `internal/handlers/media/media.go`: extended `kindFromEndpoint` for both spellings.
- `internal/handlers/media/embeddings_multimodal_test.go`: new test exercising both paths through `SetupServerRouter` against a fake upstream, asserting 200 forwarding and usage recording.
