### fix(translator): resolve the auto thinking intent on OpenAI wires

- An adaptive/auto thinking intent (`thinking: {type:"adaptive"}`, `output_config: {effort:"auto"}`, or `reasoning_effort: "auto"`) reaching an OpenAI or OpenAI Responses transport was shipped as the literal `"reasoning_effort": "auto"`, which strict OpenAI-compatible upstreams reject with `HTTP 400 invalid_reasoning_effort` (they accept `none|low|medium|high` only).
- `levelFor` (the OpenAI effort clamp used by both the `openai` and `openai-responses` writers) now resolves `"auto"` to `"high"` instead of forwarding the literal string. Explicit `low|medium|high|none` levels are preserved.
- Add table-driven unit tests for `levelFor` and the OpenAI wire.

Closes #1105.
