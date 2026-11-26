### Persona: model-id suggestions on the binding and resolver inputs

- The three free-text model-id inputs on the Persona page — the add-binding
  modal, the per-key bindings editor, and the resolver check — now suggest from
  the live model catalog via a page-level `<datalist>`, the same pattern the
  injection tester already used. Typing `deepseek/` narrows to the provider's
  ids; free text still works, so an id the catalog has not published yet can
  still be armed.
- The catalog is fetched once on page mount (`GET /api/jailbreak/models/catalog`)
  and shared with the bulk-arm picker, which previously loaded it lazily on
  first open of the Bulk tab. One fetch instead of two, and the suggestions are
  available before the operator has ever opened the bulk tab.
- Verified with a live e2e test (`web/e2e/personaKeyScope.test.ts`): the
  datalist renders catalog options on a fresh gateway, and all three inputs
  carry `list="jailbreak-model-catalog"`.
