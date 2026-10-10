### Features

- **Hermes is configured per profile** (upstream decolua/9router `a9c9f683`,
  #4660). Hermes treats `~/.hermes/profiles/<name>` as an independent agent home
  with its own `config.yaml` and `.env`, but the dashboard only ever wrote the
  default one — configuring a work profile meant editing YAML by hand, and a
  profile switch silently applied one profile's model to another. `GET
  /api/cli-tools/hermes-profiles` lists the default home plus every recognised
  profile, and `GET`/`POST`/`DELETE /api/cli-tools/hermes-settings` take a
  `profile` (JSON body or `?profile=`) and answer 400 for a name that is not
  inert and 404 for a named profile whose directory is gone, so the card can
  fall back to default instead of showing a dead selection. A directory under
  `profiles/` is only listed once it carries an identity file, matching Hermes
  itself — a bare directory left by a cron run is not a profile.
- **`applyToAll` propagates the endpoint without flattening the profiles.** A
  profile already routed through 9router keeps its own model and only has the
  endpoint and API key refreshed; a profile on another provider is reported
  `skipped` with the blocking provider named rather than rewritten; a profile
  with no model yet is wired to the model chosen in the request. The response
  carries one `{profile, status, model, reason}` line per profile plus
  `updated`/`skipped` counts, so the dashboard reports exactly what moved. The
  card refuses to run it against an endpoint that is not a known 9router one:
  the field mirrors the active profile, and it can hold a foreign provider URL
  whose key would then be written into every other profile.
- **The YAML editor is surgical, never a round-trip.** The blocks are matched
  and replaced in place rather than unmarshalled into a map and re-marshalled,
  which would drop every comment and key order Hermes put in the file and
  happily delete keys that are not ours. DELETE therefore removes only blocks
  with `provider: "custom"` and reports the ones it kept; a foreign provider's
  `model:`, `delegation:` or auxiliary role survives a reset. `.env` is only
  ever touched through an `OPENAI_API_KEY` upsert, because a profile `.env`
  also carries bot tokens and messaging-channel credentials. The `model: ""`
  sentinel a fresh install ships is replaced on upsert — left in place it would
  give the file two `model:` keys, and the last-wins loader would silently
  ignore the block just written.
- **Hermes home is resolved through `HERMES_HOME`.** Path resolution honours
  the variable before the user's home directory, which is what lets the tests
  point at `t.TempDir()` and leave the real `~/.hermes` untouched; a profile
  name is additionally required to land inside `<root>/profiles`, so a crafted
  `?profile=` cannot reach another directory on the host.
- **The dashboard card grew a profile picker.** Status dots per profile, the
  command that runs it (`hermes` / `hermes -p <name>`) with a copy button,
  Apply/Reset scoped to the selected profile, a per-profile Manual Config
  preview, and the role list from upstream's current constants — `web_extract`
  is gone because it no longer calls an LLM, replaced by `compression`,
  `title_generation`, `approval` and the rest.

  `api.request` now throws an `ApiError` carrying the HTTP status instead of a
  bare `Error`: the card branches on 404 to recover a vanished profile, and
  matching on the message text would have broken the first time that sentence
  was reworded.

  Verified with `go test ./internal/handlers/media/` (212 pass), `bun test`
  (26 pass) and `make vet-svelte` (0 unresolved identifiers, 83 errors =
  baseline). Every test drives the handlers with `HERMES_HOME` pointed at
  `t.TempDir()`.
