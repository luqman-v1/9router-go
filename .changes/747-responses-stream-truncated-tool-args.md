### 🐛 A `/v1/responses` turn cut mid tool-arguments now ends `response.incomplete`, not a false `completed`

When a combo resolved to an OpenAI-compatible Chat Completions upstream, a streamed
`/v1/responses` turn whose tool-call `arguments` were cut mid-token still closed with
`response.completed` and `status: "completed"`. Codex Desktop read that as a successful
turn, then failed to parse the arguments (`EOF while parsing a string at line 1 column N`)
with nothing on the wire to retry on — 36 such errors on 224 requests, and 26 of 64 turns
finishing with an empty final message, all logged as `status=200` by the gateway.

- **The bridge does not truncate.** The reported capture with exactly 100 argument deltas
  looked like a cap, so it was measured rather than assumed: 200 argument fragments fed
  through `StreamChatToResponses` arrive as 200 `response.function_call_arguments.delta`
  events. Neither the bridge, the translator, nor the SSE scanner has a delta or argument
  limit (the scanner allows 10 MiB lines, and the upstream body is read unbounded).
- **The missing signal was the defect.** `closeToolCall` buffered whatever arrived and
  closed the item with it, and `sendCompleted` hardcoded `status: "completed"`. A buffer
  that cannot parse is the shape a stream cut between two argument tokens leaves, so the
  translator now records `Incomplete` and the terminal event becomes `response.incomplete`
  carrying `incomplete_details.reason: "tool_call_arguments_incomplete"`.
- **The reason names the cause the upstream gave.** A truncation the upstream attributed
  to `finish_reason: "length"` is reported with the reason the Responses API already
  defines for it, `max_output_tokens`; a stream that simply ends — the issue's case — has
  no finish_reason to inherit, so the added `tool_call_arguments_incomplete` names it.
- **The partial arguments still reach the client.** Withholding them would leave the client
  holding a `function_call` item with no end, which is worse than an unparsable one it can
  report. The item closes with the bytes that arrived, exactly as before.
- **Custom tools are exempt.** Their input is a freeform program, not JSON, so a buffer
  that does not parse is the normal case and must not be read as truncation.
- **The non-streaming converter accepts the new terminal.** `ChatResponseToResponses`
  picked the object out of `response.completed` only; a truncated non-streaming body would
  have made it return an error, and the caller relays the raw Chat body on error — the
  malformed reply that conversion exists to prevent. It now accepts `response.incomplete`.

**Verification:** `TestTranslateOpenAIToResponses_TruncatedToolArgumentsAreIncomplete`
(six cases: cut mid-token, cut before any token, cut by `finish_reason: "length"`, complete
arguments, no arguments, custom-tool freeform),
`TestTranslateOpenAIToResponses_TruncatedToolArgumentsStillCloseTheItem`,
`TestTranslateOpenAIToResponses_IncompleteIsStickyAndTerminalIsSingle`,
`TestStreamChatToResponses_TruncatedToolArgumentsReportIncomplete` (the real bridge path:
no `response.completed`, `status: "incomplete"`, the reason named, every fragment delivered),
`TestStreamChatToResponses_CompleteToolArgumentsStayCompleted` (a healthy tool turn is
untouched), and `TestChatResponseToResponses_TruncatedToolArgumentsStayConvertible`.
End to end through the real router and a combo:
`TestResponsesStreamTruncatedToolArgumentsEndIncomplete` and
`TestResponsesStreamCompleteToolArgumentsStayCompleted`.
`go test ./internal/translator/ ./internal/proxy/... ./internal/handlers/chat/` and
`go test -tags=integration ./internal/integration/` green.
