### Fixed

- `/v1/models` no longer takes >10s when a provider's `/v1/models` feed is slow. Two causes were measured against the live `xai.terablade.my.id` feed: (1) the gateway-shaped response is ~4.6 MB decoded, which exceeded the 4 MiB read cap and made JSON parsing fail only after the full upstream round trip had already been paid; (2) when the 5-minute live-catalog cache expired, every request blocked on a fresh multi-second fetch. The read cap is now 32 MiB (still bounded), and an expired-but-good catalog is served immediately while one background refresh repopulates it. Regression tests cover both cases.

- The live catalog is now warmed on boot and every 4 minutes in the background, so the first `/v1/models` request after startup also sees a warm cache instead of paying the full upstream latency. Inactive connections are skipped.
