### fix(mistral): parse array content delta in SSE stream and pin Large 4 to 1M context

- Parse array content parts in Mistral SSE delta, routing thinking parts to reasoning and text parts to text_delta.
- Pin native Mistral Large 4 models to 1M context window per AGENTS.md §3.E.

Upstream parity: decolua/9router#213558f15. Closes #1385.
