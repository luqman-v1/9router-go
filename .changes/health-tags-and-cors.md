### fix(gateway): answer the health probe with CORS, and list Ollama models at /api/tags

**`/api/health` was missing and `/health` carried no CORS headers.** The
dashboard's tunnel and Tailscale cards probe the gateway from the browser
(`EndpointView.svelte clientPingUrl`) and treat a failed fetch as "unreachable".
A cross-origin response without `Access-Control-Allow-Origin` fails that fetch
even when the gateway is healthy, so a working tunnel could read as down. Both
paths now answer with the upstream CORS shape (`*`, GET/OPTIONS, `*` headers),
and `/api/health` exists because an older dashboard build polls that path.

**Ollama clients had no model to talk to.** An Ollama client lists models from
its base URL before it chats; with no `/api/tags` the gateway looked like an
Ollama server with nothing installed, so `ollama list` printed an empty table.
Upstream answers this route from a hardcoded two-model fixture; this asks the
provider registry instead, so the list is the models the gateway can actually
route and stays correct as the catalogue changes. `/tags` is served too — some
clients use the un-prefixed path when the base URL already ends in `/api`.
