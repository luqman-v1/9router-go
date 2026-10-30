### feat(api-keys): quota per API key, a context ceiling per key, and spend per client key
A key that ran out of tokens was answered with 403, which reads like "you may
not do this" rather than "you are over your allowance". It is 429 now, with
the error type `insufficient_quota`, so a spent budget can be told apart from
a rate limit — only one of the two clears by itself.
Each key carries two ceilings, both settable on create and in the policy
dialog:
- `tokenQuota` — the tokens the key may spend over its whole life.
- `contextLimit` — the context one request may carry. A conversation over the
  ceiling is trimmed to fit (history dropped, the answer's budget capped to
  what is left) instead of refused, because a ceiling that rejected a long
  conversation would make the key useless exactly when it is used hard. A
  request that already fits goes out untouched.
Trimming can take the assistant turn that made a tool call with it and leave
the tool result behind, which upstream answers with a 400 — so the pairing is
repaired after every trim.
The Usage page gained the other axis of the spend: the Overview now carries a
"Usage per API key" table with requests, in/out tokens, cached tokens, cost
and share, per key the callers presented. The table it replaces grouped the
upstream credential the router spent — a connection, not the key that was
sold. A key deleted after it spent still gets a row, under its masked form,
and a request that carried no client key gets a bucket of its own rather than
disappearing from the totals.
In the dashboard both amounts are written as scales ("10M", "500K", "1B"), and
model access now picks from the models the gateway publishes instead of from a
free-text field, with the pattern syntax kept for the glob and deny forms the
picker cannot express.
