### feat(web): banned tab, appeal URL, provider risk notice (#1004)

List banned accounts in their own tab (Active / Error / Banned), surface
the appeal URL, and show the provider risk notice. Dashboard side of
#1004; depends on backend half #996.

- Banned tab classification is driven from the persisted ban signal in
  conn.lastError (the deactivation sentence, or tos_violation together
  with cloudcode-pa), never a date heuristic or a hardcoded provider id.
  A banned connection is excluded from Error.
- Submit Appeal renders when providerSpecificData.verificationUrl passes
  host allowlisting (forms.gle, accounts.google.com,
  docs.google.com/forms/) and opens it in a new tab with rel="noopener
  noreferrer". Invalid URLs render nothing.
- Risk notice is driven from connection auth type (oauth) and registry
  notice text instead of a hardcoded antigravity check.

Tests: web/src/lib/ban.test.ts. Verified with bun run build, svelte-check
ratchet, oxlint --deny-warnings, bun test, go test ./web/...
