---
"9router @ 2026-10-25": park perplexity-web behind Hidden
---

Parked the perplexity-web provider the way upstream does (`serviceKinds: []`): with no executor behind the entry it now stays out of the provider pickers while the cookie probe, saved connections and `pplx-*` model resolution keep working.
