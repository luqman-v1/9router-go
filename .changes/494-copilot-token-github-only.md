### fix(media): scope the embeddings Copilot token to the GitHub provider

**Embeddings.** The GitHub Copilot token in a connection's
`providerSpecificData` was overriding the `Authorization` header of every
upstream, because `HandleEmbeddings` never looked at which provider the
request was headed for. The token is a GitHub credential and upstream sends
it only from the GitHub embeddings adapter
(`open-sse/handlers/embeddingProviders/github.js`), but the gateway applied it
to whichever provider the connection pointed at: a connection for `openai`
carrying the key had its own API key replaced and the GitHub credential sent
to OpenAI, which answered `Incorrect API key provided`. The override is now
applied only when the resolved provider is the GitHub one, resolved through
the alias table so a request addressed to `gh/<model>` is still recognised as
GitHub and no other provider is ever affected.

Verified with `TestHandleEmbeddings_CopilotTokenIgnoredForOtherProvider`, which
seeds an `openai` connection carrying `copilotToken` and asserts the upstream
sees the connection's own key. On the pre-fix code:

```
expected Authorization Bearer sk-openai-own-key, got "Bearer tid=should-not-be-here"
```

`TestHandleEmbeddings_CopilotTokenOverride` (the GitHub connection still
authenticates with the token) and `TestHandleEmbeddings_CopilotTokenOverrideViaAlias`
(the `gh` alias is still recognised as GitHub) both pass, together with the
rest of `./internal/handlers/media/...` and `./internal/providers/...`.
