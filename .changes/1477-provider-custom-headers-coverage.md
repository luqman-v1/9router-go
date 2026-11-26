### Custom headers now reach every forwarder, and every provider

- **The operator's Custom Headers are applied on every lane, not only OpenAI.**
  Gemini, Bedrock, trae, windsurf, vertex, and the Freebuff session calls each
  assemble their own header map, so an override saved for antigravity, bedrock
  or vertex was shown as saved in the dashboard and then dropped from the
  request. Each now applies `ApplyHeaderOverrides` last, so an override wins.
- **Bedrock joins the map before signing.** A header added after the SigV4
  signature would be one AWS did not authenticate; the override is applied to
  `baseHeaders` before `aws.SignRequest`, so the request stays valid.
- **CodeBuddy telemetry carries the overrides too.** The chat call already did
  (it goes through `ForwardOpenAI`), but every `/v2/report` POST and the aegis
  check built their own headers. The credential now carries the override map,
  so a header corrected for the provider reaches the telemetry senders.
- **A no-auth provider gets the Custom headers button.** The card was hidden on
  the noAuth branch, which is exactly the free lanes where a vendor UA gate is
  most likely to need correcting. This is a deliberate local behaviour change,
  not upstream parity: at `decolua/9router` `5ae63d22`,
  `CustomConfigCard.js:87` returns `null` when the provider has no registry
  builtins and no saved override, so a no-auth provider with neither shows no
  card there.
- Verified: `go test ./internal/proxy/...` green. `TestHeaderOverridesReachEveryForwarder`
  drives `ForwardOpenAI` and `ForwardGemini` end to end against a fake
  upstream; `TestHeaderOverridesCaseVariantWinsOnGeminiLane` covers the
  case-variant collapse, and `TestBedrockOverrideIsSignedWithRequest` asserts
  both that the header arrives and that SigV4 `SignedHeaders` covers it (an
  override applied after signing would be one AWS did not authenticate).
  Removing the `ApplyHeaderOverrides` call from `bedrock.go` makes that test
  fail on both assertions. Trae, windsurf and vertex are covered by inspection
  of the call site, not by a test. The telemetry tests fail on base (the
  credential had no `HeaderOverrides`) and pass with the change. Svelte ratchet
  stays at 0 errors.
