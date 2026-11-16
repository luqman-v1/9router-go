### Kiro: only the official service regions reach a hostname, and a toggle for the whole-turn integrity pass

- **A region the `q.*` host does not serve can no longer reach the hostname.** The live catalog
  (`internal/handlers/chat/live_catalog.go`) took the region straight out of the profile ARN and
  spliced it into `https://q.<region>.amazonaws.com/ListAvailableModels`. A `eu-west-1` profile —
  a real ARN shape, not a hostile one — therefore queried a host that does not exist, so the
  catalog request NXDOMAIN'd instead of answering. `providers.KiroCatalogRegion` now folds the
  region onto the set the host actually serves (`us-east-1`, `eu-central-1`) and falls back to
  `us-east-1` for anything else. The runtime surface order in `internal/proxy/grokcli.go` already
  guarded this; it now reads the same `providers.KiroQServiceRegions` set, so the two paths cannot
  drift apart.
- **`KIRO_INTEGRITY_GATE=off`** (`internal/proxy/executor/kiro_normalize.go`,
  `internal/proxy/executor/stream.go`). Kiro fragments one tool call across frames, so the
  arguments are only complete at the end of the stream; the default path assembles them and emits
  one complete call. That costs the whole turn's latency before the client sees anything. With the
  gate off each fragment is emitted as it arrives — the OpenAI streaming contract, where the client
  concatenates the argument fragments itself — which a latency-critical setup can prefer. The
  default is unchanged, and the `-thinking`/`-agentic`/model-id handling is untouched.
- Tool-call indices are now assigned per call when its buffer is created (`kiroToolCall.index`)
  instead of from a counter advanced at flush time. With the gate off the flush loop emits nothing,
  so a counter advanced only there would have reported every call at index 0.

Verified: `go test ./internal/proxy/executor/ -run 'TestKiro|TestHandleKiroStream|TestForwardKiro'`
and `go test ./internal/handlers/chat/ -run 'TestKiro'`, plus `go build ./...` and `go vet` on the
touched packages. New tests pin the region fallback (served region, unserved ARN region, empty,
non-ARN, hostile string), the toggle's accepted off-values, and both stream shapes — with the gate
off two fragments arrive as two `tool_calls` chunks, with it on one reassembled call carrying
`finish_reason: tool_calls`.
