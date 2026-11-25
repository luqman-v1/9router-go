### Fixed

- Antigravity and `agy-acp` OAuth now work when the dashboard is served through a reverse proxy such as Cloudflare Tunnel. The authorize request asks the gateway to pin the Google loopback redirect to its own listener port, so the callback page records the code server-side and the modal receives it cross-origin via `/api/oauth/callback-result` instead of relying on `localhost:80/443` storage handoff.
