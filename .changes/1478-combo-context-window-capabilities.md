### Combo context window: a nested combo's own mode is honoured

- **A nested combo's `max`/`custom` choice now reaches its parent.** The seat
  walk folded raw leaves, so an inner combo set to `max` contributed only its
  narrowest leaf when a parent in the default mode folded it. The walk now
  resolves each nested combo's effective window first, mirroring upstream's
  `resolveComboContextWindow` in `combosRepo.js`, so the parent advertises what
  the inner combo actually promised.
- **The mode logic lives in one function.** `resolveComboContextWindow` picks
  the advertised window from the stored mode and the seat range: `custom` wins
  with a positive value, `max` advertises the widest seat, everything else
  (including an unset mode) the narrowest. Before this the mode was branched at
  the call site, and the branch was where the nested case went wrong.
- Verified: `go test ./internal/handlers/chat/ ./internal/providers/` green. The
  new test `TestHandleModels_NestedComboModeResolvedBeforeParentFolds` fails on
  base (it publishes 128000, inner's narrow leaf) and passes with the change.
