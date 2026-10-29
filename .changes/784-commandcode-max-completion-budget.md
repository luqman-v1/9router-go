### fix(executor): keep the client's output budget on CommandCode

`commandcodeMaxTokens` resolved the budget from `max_tokens ?? max_output_tokens
?? 1024`. It did not read `max_completion_tokens`, the spelling
`normalizeChatTokenFields` leaves behind on a body that carried both (#766). A
client that sent `max_tokens` and `max_completion_tokens` therefore reached the
provider with no budget the resolver recognised, and the 1024 default was
billed in place of what it asked for.

Measured on the real dispatch path (`tryForwardWithConnection` →
`buildCommandcodeBody`, upstream recording `params.max_tokens`): a request with
`max_tokens=4096, max_completion_tokens=2048` arrived as **1024** before this
change and arrives as **2048** after it — the value #766 deliberately kept.
`max_completion_tokens` alone went from 1024 to its own value.

The new spelling is **last**, not first, so every body that resolved a budget
before this change still resolves the same one: `max_tokens` and
`max_output_tokens` keep their precedence, a null value is still absent, and a
body that names no budget still gets 1024.

**No second field on the wire.** The resolver reads a field; it does not emit
one. `buildCommandcodeBody` rebuilds `params` from a whitelist, so the provider
still receives exactly one `params.max_tokens` and no `max_completion_tokens` or
`max_output_tokens` at either level. `TestBuildCommandcodeBody_SingleBudgetFieldOnWire`
pins that across the four client shapes, and the same assertion run through the
full handler path confirms it end to end.

**Verification.** `TestCommandcodeMaxTokensResolution` pins the precedence table
(seven cases, including the post-normalizer shape and the null-is-absent rule);
`TestBuildCommandcodeBody_SingleBudgetFieldOnWire` pins the wire shape. Both fail
on the previous code (`commandcodeMaxTokens() = 1024, want 4096` and
`params.max_tokens = 1024, want 2048`) and pass with it. `go test
./internal/proxy/executor/` and `./internal/handlers/chat/` are green, `go build
./...` is clean.
