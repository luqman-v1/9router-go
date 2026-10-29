### feat(cli-tools): pi settings endpoint

upstream decolua/9router 342ecb0d parity (#630). The pi coding agent gets the
same settings API Hermes already has: `GET /api/cli-tools/pi-settings` reports
the install state and the parsed `~/.pi/agent/models.json` with its 9router
detection, `POST` writes the 9router provider block merging over the previously
saved one — a stored key, a custom `api`, or any other field the dashboard
cannot read back survives an apply, which is the behaviour the upstream
`existingProvider` ReferenceError fix (342ecb0d) restored — and `DELETE`
removes only that block, leaving every other provider untouched.

The API-key fallback is upstream's `resolveApiKey.js`: the caller's key, the
stored one, then the first active dashboard key, then empty — the placeholder
`sk_9router` is never written, and a deployment without `requireApiKey` works
with no key at all. `has9Router` reads both default gateway ports (20128
upstream's, 20130 this gateway's), so a config written by either gateway shows
as connected.

Verified: 11 new subtests in `pi_settings_test.go` covering all three verbs
against a scrubbed HOME/PATH (install probing, block merge, foreign-provider
preservation, model-list normalization including an id-less object reading as
the upstream default id, the active-key fallback against a scratch database
with the production schema, and the delete matrix), `go test ./...` green, the
integration suite green, and `golangci-lint --new-from-rev` clean. The pi card
stays a guide card for now — upstream serves pi settings through its generic
detail page, and wiring an apply UI into the dashboard is a follow-up.
