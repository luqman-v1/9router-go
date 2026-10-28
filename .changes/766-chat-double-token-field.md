### fix(chat): never forward both `max_tokens` and `max_completion_tokens`

A streaming request through a custom OpenAI-compatible node was answered
`400 max_tokens and max_completion_tokens cannot both be set`. The client sent
both fields — some OpenAI-compatible clients do, since the two spellings are
both legal on the wire — and the gateway forwarded the body untouched on the
Chat Completions path.

`renameMaxTokensBody` only rewrites the body for the registry `openai` provider.
A custom node (`openai-compatible-chat-*`) takes the generic `ForwardOpenAI`
path, which forwards the client's bytes verbatim, so the duplicate reached the
upstream. The upstream in the report (Sakoo, backed by GitHub models) rejects
the pair on a streamed call and accepts either field alone — verified against
the live endpoint: `max_tokens` alone 200, `max_completion_tokens` alone 200,
both 400.

**The gateway now guarantees a single token-limit field on the Chat Completions
wire.** `normalizeChatTokenFields` runs in `tryForwardWithConnection`, after
every outbound rewrite and before dispatch, so a single-model attempt and a
combo attempt (the reported failure was a combo) hit the same guard. When both
fields are present the explicit `max_completion_tokens` value is kept and
`max_tokens` is dropped — upstream's `renameParams` direction for the modern
OpenAI dialect. When the client sends one field, it passes through unchanged;
when it sends neither, the upstream default stands. Nothing is injected.

Scoped to `endpoint == "/v1/chat/completions"`: the Responses wire carries
`max_output_tokens`, and the Messages wire spells the limit `max_tokens` only,
so neither can produce the pair.

**Verification.** `TestForwardRequest_SingleTokenFieldOnWire` pins the four
cases (either field alone, both, neither) against a recording upstream. The live
tests in `internal/integration/live_sakoo_test.go` drive the real router against
`api.sakoo.biz.id` through a recording proxy, and assert on the bytes the
gateway actually sends rather than on the upstream's answer — the live endpoint
has since started accepting the pair, so a 200 alone would not distinguish a
fixed gateway from an unfixed one. Disabling the guard makes both the
single-model and the combo live test fail with the recorded duplicate body;
re-enabling it turns them green, three runs in a row.

The live tests are gated on `SAKOO_LIVE_KEY` and skip in CI.
