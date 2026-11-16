### keenable provider: free web search and web fetch

Register the keenable provider (`keenable`) with `SearchURL` (`POST https://api.keenable.ai/v1/search`) and `FetchURL` (`GET https://api.keenable.ai/v1/fetch`), marked `NoAuth` so it works keyless.

- `/v1/search` lane forwards only `query` (strips `model`/`mode`, which keenable rejects as unknown params).
- `/v1/web/fetch` lane rebuilds `GET /v1/fetch` with `url` (and optional `format`) as query parameters.
- Alias `keenable` → `keenable` and registry metadata (free tier, apikey auth, `webSearch`+`webFetch` service kinds).
