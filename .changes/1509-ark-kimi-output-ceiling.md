### Fixed

- `volcengine-ark` now clamps the **Kimi** family's output budget to Ark's
  endpoint ceiling of 32768. The catalogue advertises a far higher `maxOutput`
  (Kimi-K2.7-Code resolves to 262144), so clamping to the model ceiling left it
  uncapped and Ark answered `400 integer above maximum value, expected <= 32768`.
  The GLM-5 ceiling (4096) was already handled; both families now share one
  clamp, and a model in neither family keeps its advertised budget untouched.
- Renamed `clampVolcengineArkGLM5OutputTokens` to `clampVolcengineArkOutputTokens`
  and split the family match into `volcengineArkOutputCeilingFor`, so adding a
  capped family is one case in one switch rather than a second function.

Parity: `open-sse/translator/concerns/paramSupport.js` (decolua/9router master)
carries `{ provider: "volcengine-ark", match: /kimi/i, maxOutputCap: 32768,
clampToModelMaxOutput: true }`. The other rules in that table were checked
against this tree and are already implemented: `max_tokens` →
`max_completion_tokens` (openai, `renameMaxTokensBody`), `reasoning_details`
drop for groq/mistral/cerebras (`StrictReasoningRemoving` reaches
`filterMessages`), cloudflare-ai content flatten (`FlattenContent`), and the
openai-go deepseek `response_format` drop. No rule was duplicated.

Verification: `go test ./internal/proxy/executor/` passes.
`TestClampVolcengineArkOutputTokens` covers all three budget spellings for Kimi,
both boundary cases (at and below the ceiling), the Doubao non-capped family,
and the GLM-5 rules that existed before. Removing the Kimi case makes the three
Kimi subtests fail, which is the mutation proof that the new cases bite.
