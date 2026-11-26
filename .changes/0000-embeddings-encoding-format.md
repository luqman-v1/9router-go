### Embeddings: `encoding_format` and `dimensions` forwarding

- `/v1/embeddings` now forwards the client's `encoding_format` (`float` / `base64`)
  and `dimensions` to OpenAI-compatible providers unchanged, and keeps
  `encoding_format` when the Gemini lane remaps `dimensions` to
  `outputDimensionality` (`internal/handlers/media/media.go`).
- Previously the Gemini transform rebuilt the payload and dropped
  `encoding_format`; a base64 client got float arrays back.
- Verified by `TestEmbeddings_ForwardGeminiEncodingFormat` and
  `TestHandleEmbeddings_ForwardsEncodingFormatAndDimensions` in
  `internal/handlers/media/embeddings_test.go`, which assert the upstream body.
