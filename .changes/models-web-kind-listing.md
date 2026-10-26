### fix(models): publish the web search and fetch lanes in /v1/models/web

The kind switch carried `case "web": match = false`, so the listing was always
empty and no client could discover a search or fetch model, even with the
providers configured.

Upstream publishes one entry per lane rather than one per provider:
`<alias>/search` when the provider has a `searchConfig` and `<alias>/fetch` when
it has a `fetchConfig` (`src/app/api/v1/models/route.js:612-631`). The listing
now does the same, and the per-key access filter keeps each entry's own kind
(`webSearch` / `webFetch`) and endpoint instead of collapsing both lanes back to
the requested kind.

A provider that offers both lanes therefore appears twice, and one that offers
only search gets no fetch entry — the same shape upstream emits.
