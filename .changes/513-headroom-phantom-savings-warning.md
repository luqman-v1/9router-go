### fix(headroom): restore byte floor and warn on upstream phantom savings

Upstream decolua/9router commit db81a8c9 (PR #4855).

- **Restore 5% byte floor**: `HeadroomClient.Compress` restores the port's 5% byte floor (`len(newBody) >= len(body)*95/100`), ensuring that requests which grow or fail to shrink meaningfully are safely rejected rather than committed.
- **Upstream phantom savings warning**: `IsHeadroomPhantomSavings` evaluates both `messageBytes` and `bodyBytes` and surfaces as a warning via `log.Warn` using `FormatHeadroomPhantomWarning`, matching upstream's warning-only behavior.
- **Updated warning text**: warning formatting updated to `"reported token delta, but messages and outbound JSON shrank <5%"`.
