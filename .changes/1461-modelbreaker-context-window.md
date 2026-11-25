### Fix: per-model breaker no longer charges context-window errors

- `CountsTowardBreaker` now counts only availability failures (`ScopeModel` and `KindModelUnavailable`). A context-window overflow is produced by the caller's own prompt, so charging it let any caller open the breaker for a provider/model pair and answer 503 to every other tenant after five oversized requests (#1450).
