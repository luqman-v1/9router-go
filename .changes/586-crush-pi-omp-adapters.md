### feat(clientconfig): crush, pi, and omp adapters

Three clients that each keep a `providers["9router"]` table and disagree on what
an empty one means — the one distinction §7.1 warns about, because reading them
as one group deletes two files that should survive.

- **crush** and **pi** drop `providers["9router"]` and keep the file.
- **omp** deletes the file once nothing but a bare `providers:` is left.
- **pi merges** into its entry rather than replacing it: the user may have other
  fields on the same `providers["9router"]`, and the matrix names that as the
  thing this adapter must not break.

Clearing a YAML block needed work in the patch layer. The YAML parser reports a
nested mapping at the line its *first child* sits on, not the line of its key,
so the delete was cutting from inside the value and leaving the key behind. The
delete path now walks back to the owning key line and extends the cut past every
line indented deeper — which is also what lets it remove a whole subtree, where
the JSON path cannot because there is no comma to keep in step.

- Evidence: `go test ./internal/clientconfig/` passes. Covered: crush Reset
  keeping its file and the user's other provider, pi's merge preserving a foreign
  field on the same entry, omp deleting its file when only `providers:` remains,
  and omp keeping the file when the user still has keys outside `providers`.
