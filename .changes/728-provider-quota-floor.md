### feat(provider-limits): add quota floor to preserve near-exhausted nodes

- New `quotaFloor` column on `providerNodes`: minimum remaining quota before
  the node is skipped. 0 means no floor (default).
- Backend gate: `providerNodeVerdict` now returns `axisQuotaFloor` when
  `quota - used <= quotaFloor`, producing a 403 refusal so failover picks the
  next candidate instead of burning a nearly dead account.
- Dashboard API: `ProviderNodeLimitsResponse` and `ProviderNodeLimitsPatch`
  both carry `quotaFloor`; `HandleUpdateProviderNodeLimits` accepts it.
- Frontend: `NodeLimitsDraft`, `nodeLimitsSummary`, `nodeLimitsUsageSummary`,
  `ProviderNodeLimitsField` and `ProviderDetailView` all expose the new field
  and a red badge when the floor is reached.
- Tests: DB round-trip + negative rejection + chat handler table-driven test
  covering no-floor, unlimited, above-floor, at-floor, below-floor, and
  spent-quota cases.
