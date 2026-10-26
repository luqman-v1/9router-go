### fix(media): dial each search and fetch vendor's own URL

`forwardMediaRequest` built the upstream URL by appending the gateway's own route
to the provider's base URL, so `/v1/search` became `api.tavily.com/search/v1/search`
and `google.serper.dev/v1/search` — paths no vendor serves. A search through any
provider but antigravity and xquik could not succeed.

`ProviderConfig` gains `SearchURL`/`SearchMethod`, filled from each vendor's
`searchConfig` in the upstream registry, and the forwarder uses them the way it
already used `FetchURL`/`FetchMethod`. The verbs matter as much as the hosts:
brave-search, google-pse, searchapi, searxng, youcom, xquik and tinyfish are all
GET, while tavily, serper, exa, linkup and ollama-search are POST.

`exa` and `tavily` also gain their upstream `fetchConfig` URLs — `/contents` and
`/extract` — which were missing entirely, so `/v1/web/fetch` through either
provider was dialling a search endpoint.
