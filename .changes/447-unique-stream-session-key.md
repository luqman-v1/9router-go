### fix(translator): make stream session keys unique

Two live requests could be handed the same stream session key. The key was
built from `time.Now().UnixNano()` alone, and on Windows the wall clock ticks at
about 15.6 ms: 200 concurrent requests observed only two distinct nanosecond
values, and 104 of them shared one key. Requests that share a key also share one
`*StreamState`, and the translator mutates that state's maps outside the lock,
so the gateway crashed with `concurrent map writes` under load.

`translator.NewSessionKey` appends a process-wide atomic counter to the
timestamp. The counter makes the key unique within the process; the timestamp
keeps it sortable and lets the stale-state pruner keep working on age. The
prefix is preserved, so log lines and the pruner can still tell a stream session
from a gemini one.

Both call sites move over: `internal/proxy/executor/openai.go` (`sseStream`) and
`internal/proxy/executor/stream.go` (`handleCodexStream`).

Verified with `TestNewSessionKeyIsUniqueUnderConcurrency` (200 goroutines, no
duplicate key) and `TestNewSessionKeyKeepsPrefix`, which fail on the pre-fix
code.
