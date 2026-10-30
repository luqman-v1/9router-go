### fix(chat): treat anthropic-compatible nodes as Claude Messages upstreams

An anthropic-compatible custom node dials the `/messages` lane, but previously
nothing informed the wire-format machinery that the node speaks Anthropic
Messages: its resolved config carried no `Format`, so a `/v1/messages` client's
body was converted to Chat Completions, POSTed to the Messages endpoint, and the
Claude-shaped reply failed in `TranslateOpenAIToClaude` as "no choices in OpenAI
response" (HTTP 502 on every attempt, combo falling through a healthy model).

Three changes close the lane end to end:

- `getProviderConfig` declares `Format: claude` for anthropic-compatible nodes,
  ensuring `/v1/messages` requests reach the endpoint in Claude shape and Chat
  Completions clients convert before dialing.
- `TranslateOpenAIToClaude` relays a body that is already an Anthropic Message
  response unchanged (with its usage) instead of failing on missing choices.
- `forwardRequest` handles `upstreamClaude` / `clientClaude` combinations:
  translates Claude replies back to OpenAI format for Chat Completions clients,
  and relays Claude replies as-is for Claude clients.
