### Smart select combo: keyword, glob and regex rules

A combo can now carry pattern rules that keep its membership in step with the models the gateway knows about. A model matching a rule joins the combo by itself; one that disappears leaves it. Register `kenari/deepseek-v4` on a custom provider and a combo carrying `kenari/deepseek*` contains it — without opening the Combos page. Delete that model row and the combo drops it.

- **Modes**: `glob` (`*` spans `/`, case-insensitive), `keyword` (substring), `regex` (`re:` prefix, RE2, matched whole). An `exclude` rule always wins over an include.
- **Rules are bookkeeping, not routing**: the combo's stored model list stays the single truth the resolver reads. `internal/combos/smart` is pure — matching, diffing and the snapshot a rule owns — and the dashboard supplies the candidates. The resolver, `/v1/models`, the capacity adapter and fusion are untouched, so there is no hot-path cost.
- **Addable ≠ Live**: a rule may only *add* a model that is reachable and of the right kind, but a member is only pruned when it stops existing. A provider that loses its connection keeps its members and stops contributing new ones, because absence from a catalogue is weak evidence of death.
- **Triggers**: model saved, model deleted, models disabled, provider node deleted, combo created/updated, plus `POST /api/combos/reconcile` for the changes no write announces (a catalogue sync, a restore). Every trigger runs after the operator's write is answered, so a failed membership update never turns a saved model into a 500.
- **Bounded**: each rule caps its contribution at 20 members by default, and the editor previews the count before saving, so a `*` pattern cannot quietly build a hundred-slot fallback chain.
- **Storage**: one additive column, `combos.matchRules`, defaulting to `'[]'`. Upstream declares neither the column nor the concept and ignores it; backup export/import carry it, so a restore keeps the rules.
- **API**: `matchRules` on combo create/update, `POST /api/combos/{id}/smart/preview` (dry run), `POST /api/combos/reconcile`, and `matchRules` + derived `autoMembers` on `GET /api/combos`.
- **UI**: rules editor in the create/edit modal with live preview, a rules badge and `auto` marker on managed members, and a **Reconcile rules** action.

Verified with `go test ./...`, `go test -tags=integration ./internal/integration/...`, `bun run build`, `bun run ratchet:svelte` (83 = baseline, 0 unresolved identifiers), `bun test src scripts` and `go test ./web/...`. Plan and decisions: `docs/smart-combo-plan.md`.
