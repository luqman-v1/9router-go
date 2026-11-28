### Fixed

- Guardrails `secrets` masking: bearer-token and labelled-value redactions now
  cover a base64 value's trailing `=` padding instead of leaving it in the
  forwarded request.
- Guardrails `secrets` on structured JSON: a map value is scanned together
  with its key, so `{"client_secret":"..."}` is masked by an inbound policy
  instead of passing through untouched.
- Guardrails attribution: the generic `sk-` detector no longer fires on
  `sk-ant-` and `sk-or-v1-` keys, which their own detectors claim — one key
  now produces one finding.
- Security panel: the Secrets detector chip is covered by a live
  `web/e2e/securityPanelDetectors.test.ts` (offered, toggles, saved).