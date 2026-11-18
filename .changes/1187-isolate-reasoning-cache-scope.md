### Fixed

- `internal/translator/thought_signature_store.go`: Scope bare-key thought-signature lookups by `apiKeyId` when no session header is sent. Previously two API keys sharing an account collided on bare `toolCallID` keys, allowing one key's signature to be replayed for another key's tool call. Read keys (`signatureLookupKeys`, `GetGeminiThoughtSignature`) and write keys (`StoreGeminiThoughtSignature`) now both namespace to `apiKeyId:toolCallID` when `sessionID` is empty and an apiKey ID is available. Backward compatible — keys and calls with no apiKey ID behave exactly as before.

- `internal/translator/gemini.go`: `TranslateOpenAIToGeminiWithSession` accepts a trailing variadic `apiKeyID` argument; `GeminiStreamState` gains `APIKeyId`. `TranslateOpenAIToGemini` wraps it with no apiKey.

- `internal/proxy/gemini.go`, `internal/proxy/executor/providers.go`, `internal/proxy/executor/gemini.go`, `internal/proxy/executor/zed_google.go`, `internal/handlers/chat/gemini_handler.go`: Thread the authenticated apiKey ID from request context into the translation path, so write-side scoping mirrors read-side scoping for every Gemini-style call lane (antigravity, opencode-go, zed-google, vertex).

- `internal/proxy/executor/vertex.go`, `internal/proxy/executor/zed.go`: Thread `req.APIKeyID` into `TranslateOpenAIToGeminiWithSession` on the Vertex and Zed google lanes. Vertex passes it alongside `req.SessionID`; Zed passes it through `BuildZedCompletionBody` → `zedGoogleProviderRequest` (empty session, since Zed sends none). All Gemini-style lanes are now key-scoped.
