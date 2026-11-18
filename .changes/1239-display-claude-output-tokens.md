### fix(usage): display Claude output_tokens in request details

- Normalizes token display in RequestDetailsTab to fall back to `output_tokens` and `input_tokens` when `completion_tokens` or `prompt_tokens` are unset, matching merged upstream `decolua/9router@fe0d1a29`.
- Ensures accurate completion token counts and TPS calculations for Claude Messages API requests.