### Per-model capability editor: pin any capability on one model

- **A model's capabilities can be pinned from the dashboard.** Each of the
  eleven fields (vision, PDF, audio/video input, image/audio output, search,
  tools, reasoning, context window, max output) can be set on one
  provider+model pair. The override is **sparse**: a field left on Auto is
  absent, so detection still answers it; the editor shows the detected value as
  the placeholder, so the operator sees the real number rather than a guess.
- **The override outranks every detection level.** It is stamped on last in
  both `GetCapabilitiesForModel` and `GetCapabilitiesDetailForModel`, because
  the detail block's token limits come from a longer chain (custom row,
  provider declaration, catalog, substring table) that runs after the
  capability block. A number declared on purpose is not overridden by a table.
- **Storage is its own table on both engines.** `model_cap_overrides`
  (`provider`, `model`, `caps_override` JSON, `updated_at`) is declared in both
  `coreSchema`/`keiRouterTables` and `postgresTables`; an empty object is the
  clear action, so there is no DELETE route. `internal/providers` stays DB-free
  the same way the synced catalog does: the server installs a loader that reads
  the table into a 5s TTL cache, and a failed read is fail-open (detection
  only), never an error a request handles.
- **Endpoints:** `GET`/`PUT /api/providers/{id}/models/{model}/caps`. The model
  segment is percent-encoded because an id carries slashes and colons; the GET
  returns both the stored override and the currently detected block, plus the
  field metadata so the UI keeps no second copy of the labels or the ceiling.
- **UI:** a `tune` button on every model row (registry and custom alike) opens
  the editor modal; after a save the provider's capability map is re-read so
  the row's chips reflect the pin.
- Cross-references upstream `9router-pro` (model editor) as the source of the
  behaviour; the dashboard here stays the Svelte 5 one this project has.
- Verified: `go test ./internal/db/ ./internal/providers/
  ./internal/handlers/dashboard/` green; `TestSchemaParity` covers the new table
  on both engines. Frontend `bun run build` and the svelte ratchet stay at 0
  errors.
