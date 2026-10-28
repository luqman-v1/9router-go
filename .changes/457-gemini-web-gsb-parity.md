### feat(gemini-web): bring the provider to parity with azrinaldi/gemini-stealth-bridge

The `gemini-web` provider reached `gemini.google.com` directly and served an
anonymous session, but several of the site's own features were missing or
wrong, so a caller paid for the reference bridge's behaviour and did not get
it. The gaps and what changed:

- **The model's own number was never sent.** Slot 79 named the model of the
  account's tier and it was hardcoded to `1` — the Flash-Lite number every
  anonymous session serves. A signed-in session has other models, and the
  site's registry reports their numbers, so a turn for one of those went out
  claiming to be another. The resolved model's number goes there now.
- **The registry's thinking policy was never read.** The site publishes the
  levels each model has, and a level it does not list is not a smaller answer:
  the site substitutes a different model, silently, and files the answer under
  the requested model's name. The policy is read from the registry now, and a
  requested level the model does not offer is clamped into the set it does.
  A client that named no level gets the model's own default.
- **`tool_choice` was parsed and ignored.** A client that declared tools with
  `tool_choice: "none"` had its declarations rendered into the prompt and its
  calls recovered from the answer — the opposite of what it asked for. The
  choice is honoured now: `none` sends no declarations and recovers no call,
  and a named tool is demanded in the prompt. A recovered call whose arguments
  do not fit the schema the client declared is dropped rather than handed
  over broken, and the Lite models no longer advertise a reasoning level the
  site's registry does not give them.
- **An image could not be sent at all.** The site's own composer accepts one:
  the bytes go to Google's content-push service and the path it returns travels
  with the turn in slot 0. The upload, the path, and the push-id header are
  implemented now, and the provider advertises `Vision: true` because the
  surface really does take an image. The upload endpoint is derived from the
  connection's origin, so a mirror or a test serves it from one server. A
  remote image is fetched through the repository's SSRF-guarded transport, so
  a client cannot name a private address and have the router read it.
- **The answer reported zero tokens.** The site reports no counts, so they are
  estimated from the prompt that was sent and the answer that came back, with
  a CJK character counted as roughly a token rather than a quarter of one. The
  counts are published as the OpenAI usage object and on the request context,
  so the router's own accounting reads them like any other upstream's.
- **A Claude or Responses client received OpenAI frames.** The executor
  produces OpenAI chunks, and a `/v1/messages` client cannot read them: the
  turn looked like an upstream answering in the wrong format. The chunks are
  now translated into the Claude events that client expects, and replayed
  through the Responses bridge for a `/v1/responses` client. The mid-stream
  failure frame follows the same rule.
- **A prompt could speak in the model's voice.** The site takes one flat
  prompt, so a user turn that opens with `Assistant:` reads as the model
  speaking, and a replayed assistant turn that carries the model's own
  reasoning makes the model continue that reasoning instead of answering. Both
  are sanitized on the way in, with the client's own text kept.
- **The quota is published.** The site's `qpEbW` RPC reports the credit
  buckets of the 5-hour and weekly windows, and the GraphQL usage query
  reports the same windows as percentages with the exact refresh timestamps.
  Both are read, and the dashboard's quota panel parses them.

Reference: `azrinaldi/gemini-stealth-bridge` — the model number, the thinking
policy, the content-push upload, the token estimate, the prompt sanitizer and
the quota RPCs are that project's shape, and the wire details were taken from
it rather than guessed.

Verified with a table-driven unit suite over the new behaviour (the model's
number, the level clamp, the tool choice, the tool argument schema, the
upload, the token estimate, the prompt sanitizer, the client formats), and
with an integration suite that boots the real router against a fake site and
asserts that a chat client and a streaming chat client both get an answer
they can read. The anonymous turn was also exercised live against
`gemini.google.com` on 2026-10-20: the site answered, the answer arrived as
chat completion chunks, and the usage frame carried the estimated counts.
