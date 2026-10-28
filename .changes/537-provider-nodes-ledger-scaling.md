### perf(db): `GET /api/provider-nodes` reads the nodes' own ledger rows instead of the whole ledger

- **The symptom**: the provider-node list took over a second, and got worse the
  more the gateway served — every served request appends a row to the ledger
  the read then scans. Measured against a 400k-row ledger with 200 custom
  nodes, one `GET /api/provider-nodes` took **1,505 ms**.
- **The cause**: `ProviderTokenUsageByNode` read the whole ledger and grouped
  it by provider (`GROUP BY h.provider` over `usageHistory`, unioned with the
  carryover). The cost therefore scaled with the **size of the ledger**, not
  with the number of custom providers, and it visited rows belonging to
  providers that are not custom nodes at all — the registry providers, which
  are the bulk of the traffic.
- **What it scales with now**: the read is driven by `providerNodes`, and each
  node's spend is read by a seek on that node's own rows. The cost follows the
  number of nodes and the ledger rows that belong to those nodes — it no longer
  follows the ledger as a whole. Measured on the corpus below, the read is
  unchanged by 60,000 rows belonging to registry providers, and it still grows
  with the rows that belong to a node, which is the point: those are the rows
  the answer is made of.
- **The fix**: the read is now driven by `providerNodes`. Each node's spend is
  read through an indexed seek that covers the token columns, and the carryover
  is **added** rather than joined, so a node whose history a retention prune
  already deleted still reports what it spent. A node with neither history nor
  carryover reports zero, which is what the caller already read.
- **The index**: `idx_uh_prov_tok` on `(provider, id, promptTokens,
  completionTokens)`. The upstream `idx_uh_provider` answered the equality
  search but not the mark, so the sum still went back to the table once per
  matching row. The composite replaces it rather than sitting beside it —
  SQLite would otherwise keep the narrow index as a second candidate that every
  ledger insert has to maintain. Over 50,000 inserts, keeping both cost
  71 us/row against **56 us/row** for the composite alone, so the swap makes the
  write path cheaper too.
- **The other ledger readers are untouched.** The analytics breakdowns still
  ride `idx_uh_ts`, the usage window still rides `idx_uh_ts_prov`, the per-key
  quota still uses the multi-index OR over `idx_uh_clientkey` and
  `idx_uh_apikey`, and the retention prune still walks the rowid. The plans
  were checked one by one, not assumed.
- **Measured on a 400k-row ledger with 200 custom nodes** — the same ledger
  file, the same request, two binaries, over real HTTP, with a byte-identical
  response payload:

  | read | before | after |
  |---|---|---|
  | `GET /api/provider-nodes`, warm | 1,442–4,021 ms | 19–80 ms |
  | `GET /api/provider-nodes`, first request after boot | 34,284 ms | 945 ms |
  | one node's spend (the request path) | 0.5 ms | 0.1 ms |

  The cold number is the page cache filling up; the warm numbers are what the
  dashboard's ten-second poll actually sees.
- **Verified** with `go test ./...`, the feature integration suite
  (`make test-integration`), and the new tests in
  `internal/db/provider_quota_list_test.go`. Two of them pin the *plan*: the
  node spend must be a covering-index seek, and the statement must not scan
  the ledger. One measures the *cost*: the same read over a ledger holding
  60,000 rows belonging to registry providers must not get slower, which is
  the property the plan alone cannot show.
