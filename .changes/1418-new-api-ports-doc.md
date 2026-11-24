### Documentation

- New wiki page **New-API Ports** (`wiki/new-api-ports.md`): documents every feature
  ported from QuantumNous/new-api into 9router-go — budget enforcement, `/v1/rerank`,
  usage export, the Uptime Kuma heartbeat, the Vertex `v1beta1` connection switch, the
  HTTP/2 PING idle probe, and per-key model restriction with buyer presets — each with
  what it does, how to turn it on, and the PR that landed it. Also lists what was
  considered and deliberately not ported (plugin marketplace, topup/redemption,
  task plugins, expression-based pricing) and why.
- README Key Features gains the new-api-derived entries with a link to the page.
- Wiki index links the new page.
