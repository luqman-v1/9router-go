### fix(providers): give gemini-cli its own envelope so the Cloud Code lane can answer

The registry entry for `gemini-cli` carried a base URL, an auth header and a
scheme — and no format. A request for the lane therefore fell through to
`ForwardOpenAI`, which POSTs an OpenAI body at `cloudcode-pa.googleapis.com`, an
endpoint that only speaks the Gemini envelope. The lane could not have answered a
single request.

Cloud Code Assist differs from antigravity, which shares the host family, in
three ways:

- the path is `:generateContent` on the `v1internal` root, not
  `/v1beta/models/{model}:...`;
- the body is `{project, model, request: <gemini payload>}` with no `userId`,
  `requestType` or `requestId` — those belong to the antigravity wrapper and the
  CLI backend rejects them;
- the request carries `X-Goog-Api-Client` and a UA whose model id must match the
  envelope's, or Google answers 403.

`WrapForGeminiCLI` builds that envelope, `IsGeminiNative` now routes both
formats through the native handler, and `ForwardGemini` branches on the format
for the URL, the wrapper and the headers. The base host is corrected to
`cloudcode-pa.googleapis.com` — upstream's `daily-` prefix is the CLI's own
staging host and answers 404 for this lane.
