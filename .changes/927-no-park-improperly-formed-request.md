### fix(errorclassify): do not cool down accounts for generic 400 improperly formed requests

- Port of upstream `decolua/9router#4958`, **deliberate divergence**: upstream retains `{text:"improperly formed request", cooldownMs:2m}` (Kiro model-tier lock), this port removes it. The text's provenance in the Go port is generic 400 (Codex/Claude malformed payloads), so parking all accounts for 2 minutes for a client payload error is wrong here — every account is parked by a request no account could serve. Upstream's own comment ("model not available on this account tier") no longer describes the Go port's error source.
- A 400 containing this text now matches no cooldown rule and falls through to `ShouldFallback: false` (request-scoped error), leaving accounts unparked.
- Add unit test `TestClassifyError_ImproperlyFormedRequestNoPark` in `internal/providers/errorclassify_test.go`.

Part of #927.
