### Dashboard: split the API client by domain

`web/src/api/client.ts` was 3390 lines. It is now a barrel that re-exports every type and runtime helper and composes the same `api` object from endpoint modules, so no import path and no call site changed.

- `runtime.ts` — auth, error handling, `request`, `normalizeConnection`.
- `types.ts` — the request/response type catalog, kept whole.
- `endpoints/*.ts` — 14 domain groups (connections, nodes, combos, keys, models, settings, oauth, usage, tunnel, headroom, pools, cli, auth, jailbreak).

No request path, payload, response handling or route changed: the `api` object exposes the same 226 methods before and after. Verified with `bun run build`, `bun test src scripts`, `make vet-svelte`, and a method-set diff of the composed object.
