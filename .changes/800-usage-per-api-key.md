### feat(usage): a table of spend per client API key

The Usage page had one breakdown by key, and it grouped the upstream
credential the router spent — a connection, not the key that was sold. An
operator who resells the gateway needs the other axis, so the Overview now
carries a "Usage per API key" table: requests, in/out tokens, cached tokens,
cost and share, per key the callers presented.

The panel is part of the page's own panel arrangement, so it can be moved,
hidden, or switched off with the other Overview panels. It reads its own
endpoint (`GET /api/usage/api-keys`) because the stats read groups by a
different dimension and cannot answer this question.

A key that was deleted after it spent still gets a row, under its masked
form, and a request that carried no client key gets a bucket of its own
rather than disappearing from the totals.
