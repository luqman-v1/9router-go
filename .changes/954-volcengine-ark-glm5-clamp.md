### fix(volcengine-ark): clamp GLM-5 output budget to model capability ceiling

- `internal/proxy/executor/openai.go`: `ForwardOpenAI` now folds a GLM-5 output
  budget down to the model's declared maxOutput capability (capped at Volcengine
  Ark's 4096 output-token ceiling) before dispatch, matching upstream
  decolua/9router#2428 (commit bbae990b). All three output-budget spellings are
  clamped: `max_tokens`, `max_completion_tokens` and `max_output_tokens`. A
  budget at or below the ceiling, a request that sets no budget, and every
  non-GLM-5 model are left byte-identical. Ark answers HTTP 400 for a GLM-5
  request asking for more than its ceiling, so this prevents the failure before
  it reaches upstream.
- Model match is case-insensitive substring `/glm-5/i`
  (`strings.Contains(strings.ToLower(model), "glm-5")`): covers bare `glm-5*`,
  `GLM-5.1`, `GLM-5.2`, `glm-5-turbo`, `zai-org/GLM-5.2`, and any id containing
  `glm-5`, not just `^glm-5` or `^doubao.*glm-5`.
- The model maxOutput ceiling is read from the model capability registry
  (`providers.GetCapabilitiesForModel`) when available and the body clamps to
  `min(maxOutput, maxOutputCap)` (further bounded by Ark's 4096), rather than a
  hardcoded 4096.
- Unit tests in `internal/proxy/executor/openai_volcengine_ark_glm5_clamp_test.go`
  cover clamping on all three fields, preservation at/below the ceiling,
  case-insensitive variant matching (`zai-org/GLM-5.2`, `GLM-5.1`, `glm-5-turbo`,
  `doubao-glm-5-256k`), non-GLM-5 models untouched, and clamping to a lower
  registered `maxOutput`.
