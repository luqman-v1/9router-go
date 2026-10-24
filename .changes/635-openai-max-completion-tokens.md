### fix(openai): rename max_tokens to max_completion_tokens provider-scoped

Current OpenAI models reject `max_tokens` with 400 "Use
'max_completion_tokens' instead", but the Go rename only applied inside
the Claude translator and only for `gpt-5*/o1-/o3-/o4-` names: an
OpenAI-format client's `max_tokens` reached the openai provider as-is,
and `gpt-6.x` never matched.

The shared OpenAI executor now renames `max_tokens` onto
`max_completion_tokens` when the provider is `openai`, keeping an
explicit client-sent target value (upstream 7c42d8f8's paramSupport
openai rule). The executor `Request` carries the canonical provider id
so provider-scoped wire rules can key off it, and the Claude lane's
predicate covers `gpt-6` too.
