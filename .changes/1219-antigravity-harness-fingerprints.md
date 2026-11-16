### Antigravity: rewrite Hermes/OpenClaw and OMP fingerprints that earn a false 429

- Antigravity's Cloud Code backend answers system text carrying another coding
  harness's branding with `429 RESOURCE_EXHAUSTED` even when the account still
  has quota. The system-instruction sanitizer now neutralizes the Hermes Agent /
  OpenClaw fingerprint rows: the pair-programming and tool-output sentences,
  `Be direct.`, `is your authoritative reference`, the Google
  operational-directives header, Hermes section headers and environment
  identifiers, and the OpenClaw identity sentence.
- OMP coverage is completed. The `<system-conventions>` block is now replaced
  whole with canonical neutral wording instead of only renaming its tag, because
  the RFC 2119 text inside is the fingerprint pair reported in
  `can1357/oh-my-pi#11699`. A standalone RFC 2119/8174 clause is collapsed too,
  and OMP brand matching accepts hyphen and underscore separators
  (`oh-my-pi`, `oh_my_pi`) in addition to `Oh My Pi`.
- A live OMP 18.20.3 request against `ag/gemini-3.8-flash-high` still answered
  429 after those rewrites. Bisecting the captured system prompt showed two more
  standalone triggers: the identity sentence
  `You are omp's trusted coding assistant.` and the internal `xd://` URL scheme.
  Both are neutralized now, along with the `omp://` scheme, the `~/.omp/` home
  path, and a leftover standalone `omp` token. The same request then returned
  `200 OK` from the real backend, and the captured upstream payload no longer
  contains any of those markers.
- Tool descriptions are prompt text the backend scans the same way, so they go
  through the Hermes fingerprint rewrite and the OMP brand/scheme rows.
  Conversation turns are left alone: the caller's own vocabulary is not harness
  text.
- Ported from ag-shield `0ffb051` (issues #5697, #5701, #5703) and the OMP
  masking sidecar's block canonicalization. Two rows are deliberately not
  ported: the self-brand rewrites (`antigravity` → …, `9router` → …) would fight
  the upstream `opencode` → `antigravity` rewrite, and the reference's 429 retry
  loop replays against the same exhausted quota window that
  `proxy.transientRetryPolicy` leaves to the account-fallback path.
- Verified with `go test ./internal/translator/...` and
  `go test ./internal/proxy/...`; the tables cover every row, idempotence,
  system-only scope, and tool-description copying. The live OMP run used a
  temporary provider entry in the OMP config pointed at a local build of this
  branch; the config was restored afterwards.
