### Capacity adapter defaults apply when the settings row predates the toggle

- A settings row saved before the capacity-adapter feature stored no
  `capacityAdapter` key, so `capacityAdapterPools()` returned an empty map and a
  request carrying an image was forwarded verbatim to its text-only model — the
  upstream answered `400 unsupported value "image_url"`. An operator with no
  OpenCode/vision account shares this path when the default vision pool model is
  unavailable.
- `capacityAdapterPools` now merges the declared defaults (vision/audioInput
  enabled-and-empty → the default fallback model) when the stored row omits the
  key entirely, so a legacy image request is routed to the vision adapter pool
  instead of a model the upstream rejects. An explicitly configured pool map is
  left untouched, so an operator who turned a pool off keeps it off.
- Verified by the existing capacity-adapter suite and a new regression case
  (`absent capacityAdapter key falls back to the defaults`).
