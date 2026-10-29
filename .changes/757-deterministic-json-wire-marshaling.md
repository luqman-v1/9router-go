### fix(providers): preserve prompt caching with deterministic JSON marshaling across providers

Upstream LLM providers (OpenAI, Claude/Anthropic, Gemini, DeepSeek, Kimi, NVIDIA NIM) key their prompt prefix caching on byte-identical request prefixes. Go's `encoding/json/v2` randomizes map member iteration order by default, which causes request rewriting seams (such as parameter renaming, schema sanitization, and header/envelope wrapping) to produce different byte sequences across turns, resulting in 100% prompt cache misses and rapid quota exhaustion.

This fix ensures deterministic JSON marshaling (`marshalStable` / `json.Deterministic(true)`) across all request transformation pathways:
- `openai`: `renameMaxTokensBody`
- `providers`: `ensureMessagesFor`, `ForwardKimchi`, `ForwardIflow`
- `nvidia`: `normalizeNvidiaReasoning`
- `zed`: `BuildZedCompletionBody`
- `antigravity`: `WrapForAntigravity`, `hardenAntigravityRequest`, `StripThoughtSignatures`, `ReplaceThoughtSignatures`, `cloakToolDeclarations`
- `opencode`: `ApplyFingerprintSystemPrompt`
- `responses`: `ResponsesToChatRequest`, `convertResponsesContent`
- `tools`: `FitToolNames`, `CleanParametersSchema`, `SanitizeOpenAITools`, `EnsureTrailingUserTurnInBody`, `sanitizeToolArgs`

Verified: Added regression tests to `TestWireBodiesAreDeterministic` and `TestFilterToOpenAIFormatJSONDeterministic`. All tests in `./internal/proxy/executor/...` and `./internal/translator/...` pass.
