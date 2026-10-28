### fix(chat): fail closed when the plan half of the model-access policy cannot be read

A key's allowlist is the union of two halves: the rows the key owns, and the
rows its plan grants. The union can only ever add a model, never take one
away.

`withPlanPatterns` dropped a failed plan read and returned the key's own rows
alone. That silently discarded the plan's grant, and for a key whose only
rows came from its plan it left an empty allowlist — which reads as "no
constraint", so a key restricted by its plan became unrestricted. The
own-rows read above it already refused this same shape with a 503; the plan
half now refuses it too.

A failed plan read therefore fails closed: a 503 on the request path, and
`!*` on the deep-dispatch path that holds only a key id.

Two cases are deliberately **not** failures, and the code now tells them
apart from a real storage failure with a new `db.ErrNoPlansTable`:

- a plan that was deleted after a key was pointed at it — the key's own rows
  are all that is left, which is a policy, not an outage;
- a database created before the plans table existed — no key in it can be on
  a plan.

A closed or a locked database is neither of those, and still fails closed.

The extra table check runs only after a query has already failed, so the
happy path pays nothing for it.
