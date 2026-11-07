### fix(antigravity): unify client identity, scope request IDs by session, and fix metadata platform

- **Session-scoped request IDs**: `WrapForAntigravity` and `WrapAntigravityImageRequest` now scope conversation and trajectory UUIDs using the client's session ID or connection ID rather than a static `aicode-consumers` project string, preventing every pooled account from presenting identical conversation identifiers to Google Cloud Code across both chat and image lanes. Image generation payloads also send the session ID rather than an ephemeral timestamp.
- **Consistent User-Agent**: Unified User-Agent and client version headers across chat, quota, and probe paths to consistently use `providers.AntigravityCLIUserAgent` and `providers.AntigravityCLIVersion` (`1.5.1`), eliminating conflicting client fingerprints.
- **Metadata platform enum**: Corrected `lcaMetadata["platform"]` from `2` (`DARWIN_ARM64`) to `5` (`WINDOWS_AMD64`) to align with `AntigravityCLIUserAgent`'s `os_type=windows; arch=amd64`.
- **Stream Accept header**: Set `Accept: text/event-stream` for streaming requests (and `application/json` for non-streaming) on the Antigravity chat lane matching upstream base executor standards.
- **Risk disclosure**: Added `Notice` risk disclosure to Antigravity and Gemini CLI provider registry entries informing users about upstream third-party OAuth session restrictions.
