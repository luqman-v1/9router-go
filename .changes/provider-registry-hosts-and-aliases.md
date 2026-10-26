### fix(providers): repair four dead upstream hosts and two wrong aliases

Four providers pointed at hosts their vendor does not serve, so a request built
from the registry could never succeed:

- **firecrawl** used `api.firecrawl.com`; the service lives on `.dev`
  (`open-sse/providers/registry/firecrawl.js:20`).
- **huggingface** used `api-inference.huggingface.co`, which no longer resolves —
  the upstream file says the legacy host is gone (DNS `ENOTFOUND`). It now uses
  the Inference Providers router for images and `hf-inference` for speech,
  matching `registry/huggingface.js:65,99`.
- **alicode-intl** used the dashscope Model Studio host, which rejects Coding
  Plan keys; upstream sends them to `coding-intl.dashscope.aliyuncs.com`
  (`registry/alicode-intl.js:17`).
- **alitp-intl** was missing the `.maas` host and the `/compatible-mode` path
  (`registry/alitp-intl.js:23`).

**`tr` resolved to the wrong provider.** Upstream publishes `tr` on both
`tokenrouter` (aliases[]) and `trae` (alias, `uiAlias`), but keeps trae out of
the registry array — its SOLO agent has no tool calling — and builds the alias
table from that array in order, so `tokenrouter` wins. Verified by running
upstream's own resolver: `resolveProviderAlias("tr") === "tokenrouter"`
(`open-sse/services/model.js:16-19`). Go mapped `tr` to trae, so a `tr/<model>`
request went to a hidden provider instead of the gateway that serves it;
`marscode` is now the spelling that reaches trae.

**`ocg` had the inverse problem:** it was published as `opencode-go`'s alias but
was absent from `ProviderAliasMap`, so `/v1/models` advertised `ocg/<model>` and
nothing could resolve it.
