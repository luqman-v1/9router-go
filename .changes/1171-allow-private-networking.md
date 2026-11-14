### feat(ssrf): allow private/LAN upstreams by default

- `ALLOW_PRIVATE_NETWORKING` now defaults to on: a self-hosted or LAN provider (ollama on loopback, a second gateway on the LAN, a Tailscale peer) is a normal deployment, so the SSRF guard no longer refuses it out of the box. Set `ALLOW_PRIVATE_NETWORKING=false` to restore the strict refusal.
- Creating a provider node from a LAN dashboard peer no longer fails with `Base URL is not allowed` when the node points at a private/LAN host. Cloud-metadata URLs are still refused for every caller.
- The media web-fetch pre-flight follows the same switch, so `/v1/web/fetch` dials private targets while private networking is on.
