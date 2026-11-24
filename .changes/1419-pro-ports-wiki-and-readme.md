### docs: document which 9Router Pro features are already installed

The per-model circuit breaker from PR #1419 shipped with no operator-facing
documentation: `wiki/advanced-features.md` is the page that records what came
from another project and from where, and this feature was absent from it, as it
was from the README's Key Features list and from every cross-link that should
point at it.

- **`wiki/advanced-features.md`** gains a *Model Breakers* section, in the same
  shape as the three already there (`**Since:**` / `**Source:**`, what it does,
  how to use it, what you should know). It says what the layer does and does not
  charge — the scope table is the part that matters, because a reader who thinks
  a provider outage opens a model cooldown has understood the layer backwards.
  The page's opening paragraph now names 9Router Pro as a source and says what
  was ported from it: the operator-facing behaviour, never its UI, so the
  dashboard here stays the one this project already has.
- **README** Key Features gains one line for the layer, worded so the two things
  an operator actually wants to know come first: a bad model sits out its
  cooldown while its siblings keep serving, and a provider outage never parks the
  whole model list.
- **Cross-links.** The wiki index table gains a row for the symptom an operator
  arrives with ("a model keeps failing"), and the Settings page's Model Health
  card — the other layer whose job a reader is likely to confuse with this one —
  now points at it and says which is scheduled versus live. Without that pointer,
  the two look like duplicates and the live one stays undiscovered.

Verified: prose only, no code paths touched. `grep` confirms no dangling
reference and every anchor target exists (`#model-breakers` is the section
heading), and `gofmt`/`go build` are unaffected by a Markdown-only change.