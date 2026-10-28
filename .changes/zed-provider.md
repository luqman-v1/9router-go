### feat(zed): serve Zed's hosted models through the OpenAI-compatible API

Upstream registers `zed` as `ForwardOpenAI`, so a Zed connection can log in
but never answer: every model returns `provider zed/... is unhealthy`. Zed is
not an OpenAI-compatible endpoint, and four separate layers break the generic
path.

**Auth is two layers.** The stored credential is an account blob that only
authorizes `/client/*`. Model calls need a short-lived LLM token minted from
it — `POST /client/llm_tokens {"organization_id": …}` with
`Authorization: {user_id} {access_token}`, then `Bearer {llm_token}` for
`/models` and `/completions`. The blob is sent verbatim: extracting its inner
`"token"` field is rejected with 401 even though the secret is intact.

**The body is Zed's own envelope.** `{provider, model, provider_request}`,
where `provider` is `anthropic`, `google` or `open_ai` and
`provider_request` is that provider's native body. Each dialect has a
required shape — Anthropic wants `messages[].content` as an array of blocks,
Gemini wants `contents`/`parts` plus `generationConfig`, the OpenAI side
wants the Responses API.

**The stream is JSON Lines.** One JSON object per line, no `data:` prefix and
no blank-line terminator, so an SSE scanner reads the whole stream as one
malformed payload. A framing adapter rewrites it to SSE without touching the
event payloads, and the existing Claude/Codex/Gemini bridges translate from
there.

**The catalogue is behind the token exchange**, and it states far more than
an id: the real windows, the accepted effort levels and which one is the
default, whether thinking can be disabled, and whether the model can call
tools in parallel. All of it is published on `/v1/models` and in the
dashboard, so a client never has to hard-code an override. The level
spelling is passed through verbatim — Gemini uses `LOW`/`MEDIUM`/`HIGH`,
Anthropic `low`/`medium`/`high` — because only the catalogue knows which
set a given model accepts.

Two supporting fixes come with it: the live catalogue now outranks the
static substring table when it states its own limits (the table only knows
model families, so it reported a neighbouring model's window), and the
connection test walks all three auth steps instead of answering "valid"
whenever a token string exists.

Verified live against a real account: 23/23 models non-streaming, 3/3
streaming across the three families, effort levels accepted at
`low`/`high`/`max`, dashboard panel and connection test green, `ttft_ms`
recorded on both the streaming and the buffered path.
