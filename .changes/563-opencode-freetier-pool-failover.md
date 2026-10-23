### Free-tier direct replay, proxy pool rotation, and fallback on pool exhaustion

- OpenCode free-tier refusal (`FreeTierError` / 403) is now retried once over a direct egress rather than burning key rotations or remaining in a failed state.
- Proxy pools are tracked and excluded across rotation attempts within a strategy so consecutive attempts do not re-select an egress that just failed.
- When all proxy pools are excluded/exhausted, the attempt drops its pool binding and falls back to direct connection instead of returning a hard 403 failure.
- `isModelScopedError` excludes `FreeTierError` so accounts/connections are not falsely locked out on IP proxy gate refusals.
