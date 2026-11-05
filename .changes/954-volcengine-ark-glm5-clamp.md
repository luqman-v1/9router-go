### fix(volcengine-ark): clamp GLM-5 output budget to 4096 tokens

- `internal/proxy/executor/openai.go`: `ForwardOpenAI` now folds a GLM-5
  output budget above Volcengine Ark's 4096 output-token ceiling down to
  4096 before dispatch (`max_tokens` and `max_completion_tokens`). A budget
  at or below the ceiling, a request that sets no budget, and every
  non-GLM-5 model are left byte-identical. Ark answers HTTP 400 for a GLM-5
  request asking for more than 4096 output tokens, so this prevents the
  failure before it reaches upstream.
- GLM-5 detection covers bare `glm-5*` ids and Doubao-family ids carrying
  the `glm-5` qualifier (case-insensitive).
- Unit tests in `internal/proxy/executor/openai_volcengine_ark_glm5_clamp_test.go`
  cover clamping above 4096, preservation at/below 4096, and non-GLM-5
  models untouched.
