### Fixes

- **CLI Tools shipped a working API key inside the dashboard bundle.** The guide
  cards interpolate the instance's first API key into a pasteable snippet, and
  `effectiveApiKey` fell back to a real machine-minted `sk-…` literal when the
  key list was empty. `web/dist` is embedded in the binary and served to anyone
  who can reach the gateway, so that fallback was a public credential, and
  opening any card on an instance with no key handed it to the visitor. The
  fallback is gone: the snippet now carries `<your-9router-api-key>` and both
  the Pi and Oh My Pi cards say to create a key first — a placeholder cannot
  authenticate upstream, and an empty `apiKey` is worse still, because pi reads
  that as an unauthenticated provider and fails at request time rather than at
  paste time. The footer reads `Active Token: none created` instead of slicing
  an empty string into `••••`.

  Guarded twice. `web/src/lib/no-shipped-secrets.test.ts` scans every bundled
  source file for an `sk-…` literal, exempting only the `sk-9router-*`
  placeholders the env cards have always shown. `web/e2e/cliToolsPi.test.ts`
  asserts the same invariant against the bytes the gateway actually serves, and
  now seeds a key through `POST /api/keys` first — without that, every
  paste-shaped assertion in the file passed against the placeholder rather than
  against a real credential, which is why the shipped key went unnoticed. A
  third case deletes the seeded key and asserts the empty-state card shows the
  placeholder, never `"apiKey":""`. Both suites were confirmed failing with a
  literal restored — the bundle scan reported the token out of
  `/assets/index-*.js`, and the source scan reported it with its file and line —
  and passing once removed. `bun test` (361 pass), `make vet-svelte` (0
  unresolved, 83 = baseline), `go test ./internal/handlers/...` (1557 pass) and
  the pi/omp e2e (7 pass) all green.

- **Reported as a security fix rather than a UI tweak.** A credential compiled
  into the shipped SPA is readable by anyone who can reach a gateway, and this
  entry is served by the public `GET /api/changelog`. It therefore names no
  key, host, or deployment — the value it removes is not reproduced here.