### fix(media): register the /v1/web/fetch route

`MediaHandler.HandleWebFetch` has existed since the media lane landed, and
`forwardMediaRequest` already handles the `/v1/web/fetch` endpoint — including
the per-provider `FetchURL` override that Jina Reader and Firecrawl use. The
route itself was never registered, so `POST /v1/web/fetch` answered
`404 page not found` for every caller. The unit tests in
`internal/handlers/media` call `HandleWebFetch` directly and therefore could not
see the gap; nothing exercised the path through the router.

`internal/handlers/router.go` now registers `r.Post("/web/fetch",
mediaH.HandleWebFetch)` next to `/search` and `/scrape`, which are the sibling
web-tool endpoints and share the same forwarding code.

The gateway never dials the caller's URL on this path: the request body is
forwarded to the configured provider, and `targetURL` comes from the provider
registry (`providerCfg.BaseURL` or `providerCfg.FetchURL`). Registering the
route does not widen the outbound surface.

Verified with `TestWebFetchRouteIsRegistered` in `internal/integration`, which
boots the production router and asserts the request reaches the handler. It
fails with `404 page not found` on the pre-fix code and passes afterwards.
