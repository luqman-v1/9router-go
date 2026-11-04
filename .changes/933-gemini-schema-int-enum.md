### Gemini schema normalization

`cleanGeminiSchema` now keeps `minLength`, `maxLength`, `minItems`, `maxItems`,
`minimum` and `maximum` instead of blanket-stripping them, coercing string-encoded
values (`"1"` → `1`) so they reach Gemini as valid integers. Enum arrays have
empty strings filtered out (`["", "foo"]` → `["foo"]`), and the `enum` key is
removed entirely when nothing remains. String constraints that were already
stripped (`exclusiveMinimum`, `exclusiveMaximum`, `format`, `multipleOf`) stay
stripped.
