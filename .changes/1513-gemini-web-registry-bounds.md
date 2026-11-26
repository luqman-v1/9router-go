### Fixed

- **Gemini Web model discovery no longer panics on a short registry payload.** `geminiWebParseModelRegistry` guards `len(arr) <= 15` but then indexed slots 16 and 17 directly for the tier and capacity flags, so an upstream payload with exactly 16 elements crashed the session discovery path with `index out of range [16]`. Both slots are now read through `geminiWebPayloadAt`, the bounds-checked helper the same function already used for slot 24; a payload without the flag lists degrades to no capacity metadata instead of taking the discovery path down.

- Pinned by `TestGeminiWebParseModelRegistryShortPayload`: 16- and 17-element payloads panic before the fix and parse to the model list after it.
