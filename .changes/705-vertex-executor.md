### fix(providers): give vertex and vertex-partner an executor

Both registry entries carried a base URL and nothing else — no executor, no
format — so every request POSTed an OpenAI body at
`https://aiplatform.googleapis.com/v1`, which serves neither lane.

Two credential shapes reach this host and they put the secret in different
places:

- a **service-account key** (the `apiKey` field holds the JSON) mints a Bearer
  token by signing an RS256 JWT assertion with the key's own private key, cached
  until five minutes before expiry;
- **ADC** (`gcloud auth application-default login`) exchanges its refresh token
  at Google's token endpoint;
- a **raw API key** rides in the query string (`?key=`) and never in a header.

The URL differs by model family too. Gemini models need the project-scoped
`/projects/{p}/locations/{l}/publishers/google/models/{m}:generateContent`
path — an OAuth credential on the project-less path answers
`RESOURCE_PROJECT_INVALID` — while partner models (Llama, Mistral, GLM,
DeepSeek, Qwen) use the OpenAI-compatible
`/endpoints/openapi/chat/completions` path and require a project outright.

Both provider ids share one implementation; the URL shape is decided from the
provider id, and the Gemini lanes translate the request to the native envelope
the same way the antigravity lane does.
