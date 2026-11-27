---
title: Providers
order: 6
section: Using the Gateway
since: 1.9.14
---

# Providers

9router-go supports 150+ providers and 1000+ models. Every provider is **isolated
and independent** — no provider is merged, cross-aliased, or redirected into
another.

## The provider catalog

### OAuth & CLI sessions (28)

Antigravity (`antigravity`), Antigravity CLI (ACP) (`agy-acp`), Claude Code (`claude`), Cline (`cline`), ClinePass (`clinepass`), CodeBuddy (`codebuddy-intl`), CodeBuddy CN (`codebuddy-cn`), Cursor IDE (`cursor`), Free Pi (`free-pi`), Freebuff (`freebuff`), GitHub Copilot (`github`), Grok CLI (Grok Build) (`grok-cli`), Kilo Code (`kilocode`), Kimi (`kimi`), kimi-coding (`kimi-coding`), MiniMax Code (`minimax-code`), MiniMax Code (Global) (`minimax-code-global`), Muse (Meta Model API) (`muse`), OpenAI Codex (`codex`), Qoder (`qoder`), Qoder CN (`qoder-cn`), trae (`trae`), windsurf (`windsurf`), xAI (Grok) (`xai`), Xiaomi MiMo (`xiaomi-mimo`), Zai GLM Coding (`glm`), ZCode (Z.ai) (`zcode`), Zed (`zed`)


### Free tier (26)

Agnes AI (`agnes`), API.airforce (`api-airforce`), Bazaarlink (`bazaarlink`), BytePlus ModelArk (`byteplus`), Cloudflare (`cloudflare-ai`), DaoXE (`daoxe`), Devin CLI (`devin-cli`), Edge TTS (`edge-tts`), Gemini (`gemini`), Gemini CLI (`gemini-cli`), Google TTS (`google-tts`), Keenable (`keenable`), Kilo Gateway (`kilo-gateway`), Kimchi (`kimchi`), Kiro AI (`kiro`), Local Device (`local-device`), NaN.Builders (`nan`), Nous Research (`nous`), NVIDIA NIM (`nvidia`), Ollama Cloud (`ollama`), OpenAdapter (`openadapter`), OpenCode Free (`opencode`), OpenRouter (`openrouter`), Poolside (`poolside`), SearXNG (`searxng`), Vertex AI (`vertex`)


### API key (92)

Alibaba (`alicode`), Alibaba Coding (`alicode-intl`), Alibaba Studio (`alims-intl`), Alibaba Token Plan (`alitp-intl`), Anthropic (`anthropic`), AssemblyAI (`assemblyai`), AtmoRouter (`atmorouter`), Atria Dawn (`atria`), AWS Bedrock (`bedrock`), AWS Bedrock (xAI) (`bedrock-xai`), AWS Polly (`aws-polly`), Azure OpenAI (`azure`), B.AI (`bai`), Baidu Qianfan (`baidu`), Black Forest Labs (`black-forest-labs`), Blackbox AI (`blackbox`), Brave Search (`brave-search`), Cerebras (`cerebras`), Chutes AI (`chutes`), Cohere (`cohere`), ComfyUI (`comfyui`), Command Code (`commandcode`), Command Code (Provider API) (`commandcode-goat`), Dahl Inference (`dahl`), Deepgram (`deepgram`), DeepRelay (`deeprelay`), DeepSeek (`deepseek`), ElevenLabs (`elevenlabs`), Exa (`exa`), Fal.ai (`fal-ai`), Featherless (`featherless`), Firecrawl (`firecrawl`), Fireworks AI (`fireworks`), Fish Audio (`fish-audio`), GLM (China) (`glm-cn`), Google PSE (`google-pse`), Groq (`groq`), HCNSec (`hcnsec`), HuggingFace (`huggingface`), Hyperbolic (`hyperbolic`), Inworld TTS (`inworld`), Jina AI (`jina-ai`), Jina Reader (`jina-reader`), Kenari (`kenari`), Linkup (`linkup`), LLM7 (`llm7`), Minimax (China) (`minimax-cn`), Minimax Coding (`minimax`), Mistral (`mistral`), Morph (`morph`), NanoBanana API (`nanobanana`), Nebius AI (`nebius`), Ollama Local (`ollama-local`), Ollama Search (`ollama-search`), OpenAI (`openai`), OpenCode Go (`opencode-go`), OpenCode Zen (`opencode-zen`), OpenDesign Cloud (`opendesign`), Perplexity (`perplexity`), Perplexity Agent (`perplexity-agent`), QwenCloud (`qwencloud`), Recraft (`recraft`), RunCrate (`runcrate`), Runway ML (`runwayml`), SD WebUI (`sdwebui`), SearchAPI (`searchapi`), Self-Hosted Embedding (`selfhosted-embedding`), Self-Hosted STT (`selfhosted-stt`), Self-Hosted TTS (`selfhosted-tts`), SenseNova (`sensenova`), SenseNova (Intl) (`sensenova-intl`), Serper (`serper`), SiliconFlow (`siliconflow`), Stability AI (`stability-ai`), SumoPod (`sumopod`), Synthetic (`synthetic`), Tavily (`tavily`), Tencent Hunyuan (`tencent`), TinyFish (`tinyfish`), Together AI (`together`), Token Harbor (`tokenharbor`), TokenRouter (`tokenrouter`), Topaz (`topaz`), v1m (System One) (`v1m`), Venice AI (`venice`), Vercel AI Gateway (`vercel-ai-gateway`), Vertex Partner (`vertex-partner`), Volcengine Ark (`volcengine-ark`), Voyage AI (`voyage-ai`), Xiaomi MiMo (Token Plan) (`xiaomi-tokenplan`), Xquik (`xquik`), You.com Search (`youcom`), Z.ai Web Search (`zai-search`)


### Web session cookie (4)

DeepSeek Web (`deepseek-web`), Gemini Web (`gemini-web`), Grok Web (Subscription) (`grok-web`), Perplexity Web (Pro/Max) (`perplexity-web`)

## Connecting

Dashboard → Providers → pick a provider → Connect.

No signup needed:

- **Kiro AI** — around 50 free credits per month
- **OpenCode Free** — no auth

## OAuth flows

Different providers authenticate differently, and the dashboard offers the right
one per provider:

| Flow | Providers |
|---|---|
| Social / authorize-exchange | Kiro, Antigravity, Cline, Trae, Windsurf, Zed, Kimchi, Xiaomi MiMo |
| Device code | Any provider exposing a device flow |
| Local proxy callback | Codex |
| Bulk import | Codex, Grok CLI |
| API key paste | Kiro |
| Cookie / PAT paste | iFlow (cookie), GitLab (PAT) |
| Session polling | Freebuff — initiate, poll, inspect, and switch between sessions |

Built-in public client credentials exist for antigravity, gemini-cli, xai, codex,
iflow and others, so most operators never set an OAuth client ID. Override per
provider with `{PROVIDER}_OAUTH_CLIENT_ID` / `{PROVIDER}_OAUTH_CLIENT_SECRET`
when you run your own OAuth app.

Tokens are refreshed automatically, and a request that fails with `401` is retried
once after a refresh.

## Browser-based login

Some web providers have no OAuth endpoint at all. Two paths:

- **Gemini Web** (`/api/providers/gemini-web/browser-login`) and
  **DeepSeek Web** (`/api/providers/deepseek-web/browser-login`) open a browser,
  wait for the sign-in, and read the session out.
- **Import from a signed-in browser** (`/api/providers/{gemini,deepseek}-web/import-chrome`)
  reads the browser's own cookie store. **Close the browser first** — a browser
  that is still open rotates the same session from the other side and the two
  renewals cancel each other out, which signs the account out.

Renew a DeepSeek session with
`POST /api/connections/{id}/renew-deepseek`.

## Custom providers

One shared dialog adds an OpenAI- or Anthropic-compatible endpoint. One custom
provider is one `providerNodes` row, and a node can have many connections; a
connection points at its node through `provider = node.id`.

| Field | Purpose |
|---|---|
| Name | Display name — typing it suggests a prefix and a URL suffix, which follow the name until you edit them yourself |
| Prefix | Wire syntax prefix (`provider/model`). Optional for OpenAI/Anthropic-compatible nodes: left blank, it is filled once server-side from the URL suffix when that is safe, else a random 8-char token. `custom-embedding` still requires an explicit prefix |
| Base URL | Upstream endpoint |
| Headers | Your own request headers |

The Add dialog checks a candidate prefix live — `POST
/api/provider-nodes/availability` answers `available`, `taken` (with the owner
named), `invalid`, `automatic`, or `random` under the field. Advisory only:
create re-validates. A registry id or alias is never suggested as a prefix,
because it would shadow that provider's routing.

The Edit dialog does not autofill: a suffix change renames the node id and
rewrites saved combos.

Validate an endpoint before saving with `POST /api/providers/validate`.

## Per-provider limits

Each custom node can carry a capacity budget:

| # | Limit | Unit | Meaning |
|---|---|---|---|
| L1 | RPM | requests/minute | Request rate this endpoint tolerates |
| L2 | TPM | tokens/minute | Input (prompt) token rate this endpoint tolerates |
| L3 | Concurrency | parallel requests | Number of simultaneous requests |
| L4 | Lifetime quota | total tokens | Cumulative cap before the node is declared exhausted |
| L5 | Queue timeout | ms | How long a request may wait in the FIFO queue for L1/L3 |
| L6 | Contract expiry | RFC3339 UTC date | Validity period of the node's contract |

`0` means unlimited; an empty string means no expiry — the same convention as
API keys.

**Behaviour when exceeded**: congestion (RPM/TPM/concurrency) marks the candidate
as full for that attempt only, and the router tries the next candidate; `429` is
returned only when every candidate is exhausted. Node state (quota exhausted,
contract ended) skips all of that node's connections and returns a typed `403`.

**A congestion burst writes no cooldown.** One request over a limit must not
globally park a healthy node — only a debug trace is written. The `lastError` row
the dashboard's node-health table reads is still written, so you can see the
cause without the node being locked.

Set limits with `PUT /api/provider-nodes/{id}/limits` and lift a spent quota with
`POST /api/provider-nodes/{id}/quota/reset` (it moves the watermark, it does not
delete history).

## Transient upstream retries
A gateway-level failure on the upstream call — not a refusal, a **transient** one
— is retried before the account is blamed:

| Status | Attempts | Delay between them |
|---|---|---|
| `502` Bad Gateway | 3 | 3 s |
| `503` Service Unavailable | 3 | 2 s |
| `504` Gateway Timeout | 2 | 3 s |

`429` is deliberately **not** on that list. A throttled credential belongs to the
account-fallback path, not to a retry against the same window, and retrying it
would spend the window twice.
The whole retry run can be bounded, because three 3-second retries is nine
seconds a client sits through for a lane that is simply down. `seatAttemptBudgetMs`
in settings, a provider's own `seatBudgetMs` override, or the
`SEAT_ATTEMPT_BUDGET_MS` / `NINEROUTER_SEAT_ATTEMPT_BUDGET_MS` environment
variable (resolved in that order) caps the elapsed time: a retry whose *delay*
would carry the run past the budget is skipped and the failure is returned
immediately. `0` or unset means unbounded.
The wait itself is context-aware — a client that hangs up during the backoff ends
the attempt there instead of holding the goroutine for the rest of the delay.

## Models
Browse a provider's models with `GET /api/providers/{id}/models`, export keys
with `GET /api/providers/{id}/export-keys`, and remove many at once with
`DELETE /api/providers/{id}/models`.

**Custom models** are stored in the `customModels` key-value scope, not as columns
on `providerNodes`. Add one at a time or paste many (separated by spaces, commas
or newlines) — one request, atomic, per-row results. Rows can be edited (the
Add modal reopens prefilled, model type locked) and deleted outright — a delete
removes the row instead of parking its id in Disabled models.

An OpenRouter custom model can carry a per-model JSON `extraBody` (the Add
modal's "Custom request body" field) that the gateway shallow-merges into the
outbound request body — pin `temperature` or `reasoning.effort` without the
client sending them.

### Live model lists

`GET /v1/models` prefers a connection's **own** catalog over the static registry
when a connection exists and the connection has no explicit `enabledModels` — the
registry cannot know which models an account may actually use, and it lags model
launches. That is why `kiro` publishes `auto`/`minimax-m2.1` variants the
registry does not carry, and `grok-cli` publishes only the model its account is
allowed to call.

- Sources differ per provider: OpenCode and Qoder sign their lists per account,
  Anthropic answers `/v1/models` on the token, command-code has its own shape,
  custom `*-compatible` nodes publish whatever their own endpoint reports, and a
  provider with an OpenAI-style models feed (nous, openrouter, tokenharbor,
  opencode zen/go, …) is read through it. That feed is the provider's own
  declaration of each window, so it outranks the static tables.
- Cached for **5 minutes** per credential, with a **60-second negative cache**:
  a provider whose endpoint times out used to be re-fetched by every combo leaf,
  which turned `/v1/models` into minutes of sequential timeouts at thousands of
  connections.
- One upstream call per credential even when many requests race a cold cache —
  the losers wait for the winner. The shared call is detached from the leader's
  cancellation, so one client hanging up does not empty everyone else's cache.
- A 401/403 on the fetch refreshes the OAuth token and retries once. A fetch that
  still yields nothing falls back to the registry: the listing degrades to the
  static catalog, it never fails.

### Aliases
**Aliases** (`/api/models/alias`) let a client write a short name for a long
model id. **Deprecations** (`/api/models/deprecations`) surface models upstream
has retired, and **auto-disable** (`/api/models/auto-disable`) turns off the ones
that stopped working.
### Per-model capabilities
Detection knows what a catalog vendor publishes; it cannot know that *your*
gateway-only model accepts PDFs, or that a resold model has a 32k context. One
row per model pins what detection got wrong, without touching the catalog:
`GET|PUT /api/providers/{id}/models/{model}/caps`.
Eleven fields can be pinned — `vision`, `pdf`, `audioInput`, `videoInput`,
`imageOutput`, `audioOutput`, `search`, `tools`, `reasoning`, `contextWindow`,
`maxOutput`. The override is **sparse**: a field left on **Auto** is simply absent
from the stored row, so detection keeps answering that field and only the pinned
ones stop moving. Saving an all-Auto row deletes it instead of storing an empty
one.
Normalization follows upstream's rule — drop what is not usable, keep the rest:
unknown keys are dropped, a boolean must be a real boolean, and a limit must be
an integer in 1…10,000,000 (`0` is dropped rather than stored, because "no
opinion" and "zero tokens" would be indistinguishable to a client, and anything
above ten million is a typo, not a model). A payload that is not a JSON object
at all answers `400` instead of silently clearing a stored override.
The row is cached for five seconds and the whole path **fails open**: a missing
loader, a failed read or a corrupt row all mean "no override", which is exactly
detection. A capability the gateway cannot read is never a reason to refuse a
request that would otherwise be sent.

## Important notes

- **No cross-provider aliasing.** `antigravity` is not `opencode`; a model
  belonging to one provider is never redirected to another.
- **No substring-based model rewriting.** Routing decisions are driven by catalog
  registration, database aliases, custom node prefixes, or standard
  `provider/model` wire syntax — never by ad-hoc string inspection.
- **The Import from /models button** only appears for providers that genuinely
  support dynamic catalog discovery upstream.
