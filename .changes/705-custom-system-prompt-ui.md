### feat(tokensaver): surface the custom system prompt in the dashboard

The backend has carried `systemPromptEnabled` / `systemPromptPrefix` /
`systemPromptSuffix` since the saver chain was ported, and `applyTokenSavers`
injects both slots on every wire format. Nothing in the dashboard could set
them, so the feature existed only for someone willing to write the settings row
by hand.

The Token Saver page gains a card with a toggle and two text areas:

- **Prefix** leads the system prompt — the first thing the model reads, which is
  where a house style or a language rule belongs.
- **Suffix** lands after the system prompt — the last word, which is what
  survives a turn built mostly from untrusted content.

The prompt is applied after every style saver, so it reads as the outermost
instruction rather than something caveman or ponytail can bury.
