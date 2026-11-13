### fix(combo): forward in-band error events instead of failing over silently

- Commits in-band error SSE frames in the combo stream peeker so upstream failures reach the client instead of triggering silent failover to subsequent models.
- Parity with upstream `decolua/9router#5234`.
