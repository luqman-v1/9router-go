### feat(providers): add `commandcode-goat` (`cmc-goat`) provider backed by official Provider API

- Command Code's documented Provider API (`commandcode.ai/docs/provider`) publishes three dedicated standard lanes on a single bearer key (`/provider/v1/chat/completions`, `/provider/v1/responses`, `/provider/v1/messages`), plus a decision-model endpoint (`/provider/v1/systemone` for `typesafe/jev`).
- The existing `commandcode` (`cmc`) provider continues to speak the internal CLI `/alpha/generate` envelope with spoofed CLI identity headers.
- Added `commandcode-goat` (alias `cmc-goat`) to route each model to its declared lane based on the live catalogue's `supported_endpoints` (`commandcode_goat_catalog.go`): Claude models route to `/messages`, OpenAI and open models route to `/chat/completions` (or `/responses`).
- Added per-model context window declarations from the live catalog (spanning 200k to 1.05M).
- Added `x-cmd-zdr: 1` support when requested via `connData["zdr"]`.
- Added `typesafe/jev` decision model support via `/systemone`.
- Added quota tracker and dashboard usage support (`usageSupportedProviders`, `usageApikeyProviders`, and window normalization for 5-hour rolling limit, weekly limit, and monthly credits).
- Added unit tests covering capability resolution, multi-lane routing, quota tracking, and prefix stripping.
