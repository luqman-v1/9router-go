### Routing: `least-busy` connection strategy

- A new value for the connection fallback strategy: each request goes to the
  eligible connection carrying the fewest outstanding requests, so an agent
  hammering a multi-key provider spreads across its keys instead of filling the
  first one while the rest idle.
- The load signal is in-memory and bounded — no timer, no polling loop, no new
  dependency, no extra database read. The dispatch loop notes a pick before
  every attempt and its outcome after, so a long streaming reply counts as load
  for the whole turn it holds the key.
- Score = in-flight requests × 1.0 + failures in the last 30s × 0.5. A key that
  just refused a request is deprioritized behind one that has been serving, but
  an outstanding request still outranks a past failure. Exact ties draw at
  random, so a burst of equally idle keys spreads instead of always taking the
  first row.
- Stale safety: a pick whose outcome never reports stops counting after 10
  minutes rather than wedging its key forever, and the table is capped at 4096
  entries with per-key caps on stored timestamps and idle entries evicted
  first.
- Settings → Default Routing Strategy offers it beside Failover, Round Robin
  and Sticky Round-Robin. Both spellings (`least-busy`, `leastbusy`) resolve to
  the strategy.
