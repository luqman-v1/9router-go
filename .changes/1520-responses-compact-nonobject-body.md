### Fixed

- **`POST /v1/responses/compact` answers 400 for a non-object JSON body instead of a recovered 500.** The endpoint unmarshals the client body into a map and writes the `_compact` marker before anything else validates the request. A body of `null`, `4`, `"x"` or `[]` is valid JSON that decodes into a nil map with a nil error, so the marker write panicked with "assignment to entry in nil map" and chi's Recoverer answered a bare 500 with no payload — one unauthenticated-in-substance request, no model needed. The nil map is now rejected at the boundary with the same "invalid JSON body" 400 the invalid-JSON branch already returns.

- Pinned by `TestHandleResponsesCompact_NonObjectBodyIsABadRequest`, which drives the handler with `null`/`4`/`"x"`/`[]` (each panicked before the fix) plus an object-body control proving the happy path is unchanged.
