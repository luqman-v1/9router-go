### Fixed

- **`requestDetails` rows no longer store a pasted credential in the clear.**
  The Recent Requests tab writes the whole conversation to disk, so a provider
  key or `Authorization` header a client pasted into a prompt was retained
  verbatim and outlived every retention window — the debt register's DATA-01
  said so: retention bounds how long a secret lives, not whether it is written
  at all. Both the success and the failure payload are now masked through the
  guardrails `secrets` detector set before the marshal, so a stored row reads
  `Deploy with this key: [REDACTED] and nothing else.`
- The mask runs on storage only, and is always on. It is not the operator's
  outbound guardrail policy — that decides what may leave the gateway, this
  decides what the gateway keeps about itself — so there is nothing to
  configure and nothing an operator can turn off by accident. The detector set
  fires only on shapes only a live credential carries (a provider key prefix,
  or a label that names the field), so code, diffs and logs in the same payload
  survive unchanged; a request with no credential is byte-identical to before.
- Provenance: DATA-01 in `TECHNICAL_DEBT.md`, plus the ROADMAP's
  "Secret/PII redaction: explicit detection rules with safe defaults" feature.
  Depends on the `secrets` detector set; if that set is not in this build the
  mask is a no-op and rows are stored as before.

Verification: `go test -tags=integration -count=1 -timeout 20m
./internal/integration/` — `TestRequestDetailsMaskPastedCredentials` posts a
completion whose prompt carries a pasted key and reads the raw stored JSON back
out of SQLite: the key is gone, the mask is present, and the model, the prompt
text and the endpoint still are. `TestFailedRequestDetailsMaskPastedCredentials`
does the same on the failure row. Both fail on this branch's parent, where the
payload is stored verbatim.