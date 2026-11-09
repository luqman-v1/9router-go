### fix(quota): catch Claude resets missed between auto-ping ticks

Catch up an already observed, due, unpinged Claude reset deadline while retaining the newly observed reset for the next cycle. Keep the missed deadline pending if its HTTP request fails or transport errors, then retry after the failure cooldown. Mark the caught-up cycle only after a successful ping.
