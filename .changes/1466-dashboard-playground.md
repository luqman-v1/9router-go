### Playground

- A Playground tab: one chat turn from the dashboard against any model the gateway routes, no engine key needed. The turn is a synthetic request through the engine's own `HandleChatCompletions` (the path the Gemini surface already takes), so routing, combo fallback, persona injection, the limiter and usage accounting are the production paths.
- The turn is attributed: an optional key pick puts that key on the request context the same way the auth middleware does, so the usage row names it as the client, its budget is spent, and its model allowlist is enforced by the same gate the public lane uses. No key is the global scope.
- The reply panel shows the gateway's own Chat Completions answer: message, resolved model, token count, finish reason.
- The rebuilt body marshals deterministically, so an identical turn produces identical bytes and can hit the prompt cache `chat.go` hashes on.
- Routes: `POST /api/playground/chat` (dashboard auth, the same group as the other dashboard APIs). Verified with integration tests (`internal/integration/playground_test.go`: forwarding, key attribution, allowlist enforcement, unknown-key refusal, dashboard gate) and a browser e2e (`web/e2e/playground.test.ts`: sends without an Authorization header, renders the reply).
