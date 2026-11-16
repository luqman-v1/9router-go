### Security

- SSRF guard on web search base URL overrides (SearXNG and all web-search providers). A client-supplied `providerSpecificData.baseUrl` override for `/v1/search` is now run through `handlerutil.AssertPublicURL` before dialling, refusing loopback/private/link-local/cloud-metadata addresses with a 400 instead of fetching them on the caller's behalf. Registry defaults and explicit public http(s) URLs still work. The registry default (e.g. SearXNG's `http://localhost:4000/search`) is trusted as admin-controlled and is not gated. Port of upstream `resolveBaseUrl` / `assertPublicUrl`.
