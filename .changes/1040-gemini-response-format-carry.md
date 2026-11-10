### fix(translator): carry response_format onto Gemini native structured output
- An OpenAI-format client sending `response_format` (`json_object` /
  `json_schema`) to a Gemini native upstream got **free text instead of the
  JSON it asked for**, with no error. The Gemini request translator
  (`TranslateOpenAIToGeminiWithSession`) rebuilds the body field by field, and
  a field the parse struct does not name is a field the upstream never sees —
  so `response_format` was dropped on the way.
- The client's schema is now carried onto Gemini's native structured output:
  `generationConfig.responseMimeType` (`application/json`) plus
  `responseJsonSchema` for `json_schema`, with OpenAPI-style type names
  lowercased so the schema passes Gemini's validator. A `json_schema` with no
  schema degrades to plain JSON mode; a missing, null, or non-JSON
  `response_format` sets nothing, so a client that did not ask for structured
  output is untouched.
- No response-side change is needed: Gemini answers a request with
  `responseMimeType` as `part.text`, which the existing Gemini→OpenAI response
  translators already surface as `content`.
- Scoped to the one lane that genuinely dropped the field. The OpenAI-compatible
  and `claude` (`ForwardOpenAI`) lanes forward the body raw (so
  `response_format` already passes through), the Codex/Responses lane already
  maps it to `text.format` (#2896), and the Claude Messages lane is reached by
  native Claude clients who send `output_config` themselves.
- Verified with table-driven decode tests (absent / null / `json_object` /
  `json_schema` with uppercase + nested types / schema-less / unknown type /
  malformed / non-object) plus end-to-end assertions on the built Gemini body.
  `go test ./internal/translator/` green; `go build ./...` and
  `golangci-lint --new-from-rev` clean.
