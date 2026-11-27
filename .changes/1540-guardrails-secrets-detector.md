### Added

- **A `secrets` guardrail detector set.** The guardrail engine carried `pii`
  and `injection`; a credential in a request — a pasted provider key, a
  forwarded `Authorization` header, a `client_secret` in a config snippet —
  matched neither, so an operator could not ask for it to be masked. `secrets`
  is a third selectable set with ten detectors: OpenAI/Anthropic/OpenRouter
  keys, Google API keys, Hugging Face tokens, GitHub tokens, Slack tokens, AWS
  access key ids, bearer tokens, and labelled credential assignments
  (`api_key`, `client_secret`, `access_token`, `password`, …).
- **Labelled matches are masked around the label, not over it.** A new optional
  `redact` field on a detector keeps the introducing label — a masked header
  still reads `Authorization: Bearer [REDACTED]`, a masked JSON field still
  reads `"client_secret": "[REDACTED]"` — so a rewritten prompt still says which
  setting lost its value. A detector without `redact` keeps the flat
  `[REDACTED]` the PII set uses.
- Every pattern is anchored on something only a live credential carries: a
  provider's own key prefix, or a label that names the field. The same
  discipline the PII set follows, for the same reason — a bare 20-character
  token fires on every base64 blob in a diff, and a detector switched off after
  one false positive protects nothing. Env placeholders
  (`api_key = ${OPENAI_API_KEY}`), `os.Getenv(...)` reads, short placeholder
  values, and prose naming the field are all clean.

Verification: `go test -count=1 ./internal/guardrails/ ./internal/handlers/dashboard/`
passes. `TestSecretDetectorsFire` drives the engine per credential shape;
`TestSecretDetectorsLeaveCodeClean` is the false-positive guard, and
`TestSecretMaskKeepsTheLabel` pins the label-preserving rewrite. The same
suite, copied onto a pristine `origin/main` worktree, fails every positive
case (`no anthropic_key finding …`, `mask action did not mutate`) because the
set does not exist there. Dashboard `validateGuardrailConfig` accepts
`{"detectors":["secrets"]}` and still rejects an unknown name. The Security
panel lists the new set, so a policy can be saved from the UI; no other
behaviour moves.