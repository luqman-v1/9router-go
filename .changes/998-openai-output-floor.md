### fix(translator): raise an output floor for openai thinking so a small budget is not eaten whole by reasoning

Companion to #997, which fixed the Claude wire. On the `openai` and
`openai-responses` formats, a model whose thinking can never be switched
off (e.g. `glm-5.3-flash-free`, the muse-spark contributor lane) spends
reasoning inside the output budget — `max_completion_tokens` on Chat
Completions, `max_output_tokens` on Responses. A client that sends a small
budget (a compaction snapshot, or a short answer cap) can have the whole
budget consumed by reasoning, so the answer never starts and the client
reads `finish_reason: "length"` with empty text.

`writeOpenAI` and `writeOpenAIResponses` now ensure the same level-sized
output floor as the Claude writers (level budget + 1024 headroom, capped
at the model's own `MaxOutput`), raising only the field the client
actually sent — `max_tokens`/`max_completion_tokens` for Chat Completions,
`max_output_tokens`/`max_tokens` for Responses — and writing the canonical
field when the client sent none. Explicit `none` on a model that can
disable thinking is untouched: the client's small budget is honoured as
written.

Verified: `go test ./internal/thinking/...` passing, including new
table-driven regression tests `TestApplyOpenAIOutputFloor` and
`TestApplyOpenAIResponsesOutputFloor`, plus the full repo build (`go build
./...`).
