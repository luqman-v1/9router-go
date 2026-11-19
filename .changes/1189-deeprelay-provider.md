### DeepRelay provider with wallet balance tracking

Adds the DeepRelay pay-per-token inference provider (`deeprelay`, alias `dr`)
on `api.deeprelay.ai/v1`.

- Registers `deeprelay` in `KnownProviders` (`https://api.deeprelay.ai/v1/chat/completions`,
  bearer `Authorization`), the `dr` alias, the `ForwardOpenAI` executor, the
  model catalog (chat, embedding, image and video ids), provider metadata, and
  media service kinds so non-chat models stay out of the LLM list.
- Adds usage support for `deeprelay`: `GET /v1/billing/balance` reports the
  org credit balance as a credit-balance quota row (`Wallet Balance`, currency
  `$`, `isCreditBalance`), consistent with the runcrate wallet handling.
- Probes `GET /v1/billing/balance` when validating a DeepRelay API key, so a
  key without the `billing:read` scope is rejected before it can route traffic.

Verified with unit tests for the balance fetcher (dollars path, cents fallback,
rejected key, no credential), the validate probe (200, 401, alias `dr`), and
the provider registry (auth, alias, catalog ids, media kinds).
