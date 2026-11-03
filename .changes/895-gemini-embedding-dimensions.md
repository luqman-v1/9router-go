### feat(embeddings): forward OpenAI dimensions to Gemini outputDimensionality

- Map incoming `dimensions` property in `/v1/embeddings` requests to Gemini's native `outputDimensionality`.
- Ensures pgvector schema queries requesting specific vector lengths (e.g. 1536) receive properly dimensioned vectors from Gemini.
- Verified with unit tests in `internal/handlers/media/embeddings_test.go` (upstream `decolua/9router#1366` parity).
