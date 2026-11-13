### Changed

- Claude prompt-cache head anchors now use the 5-minute ephemeral window instead of a
  hardcoded 1-hour one. A 1h write is billed at 2x input against 1.25x for 5m, and the
  longer window only pays off across idle gaps it rarely sees, so the default was
  charging every instance 60% more per written token for nothing.
- New `claudeCacheTtl` setting (`"5m"` | `"1h"`, empty = 5m) restores the old behaviour
  and applies live, without a restart. It is exposed on the Claude provider page and in
  the Claude quota tracker with the billing tradeoff written next to the control.

### Added

- Cache writes are recorded per window: `cache_creation.ephemeral_5m_input_tokens` and
  `ephemeral_1h_input_tokens` are parsed on all four usage paths, stored on the usage
  row, logged, and shown in the request detail badge as `5m: N · 1h: N`.

### Fixed

- 1-hour cache writes are priced at 2x input instead of the 5-minute rate, so a request
  that wrote its prefix at 1h no longer records a bill ~40% below what Anthropic charges.
