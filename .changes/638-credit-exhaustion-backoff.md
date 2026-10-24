### fix(combos): try the next model when credits run out

Out-of-credits answers arrive as an HTTP 400 ("insufficient credits",
Anthropic's "credit balance is too low"), which the request-scoped 4xx
path advances past in a combo, but the account-side classification said
nothing: the spent account read as healthy and was retried immediately
on every pass.

The two texts are now classified as backoff-able, so the failing account
cools down with an incremented backoff level while the combo moves on
(upstream decolua/9router b82f5c4a, fixes upstream #4713).

Verified: `TestClassifyError_CreditExhaustionBacksOff` covers both texts
(fallback, level 1, base cooldown) and the plain-400 no-fallback case.
