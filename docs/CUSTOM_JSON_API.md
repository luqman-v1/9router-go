# Custom JSON APIs

Connect a JSON business endpoint that does not implement the OpenAI or Anthropic
protocols. Port of upstream [decolua/9router#4929](https://github.com/decolua/9router/pull/4929).

## Dashboard setup

1. Open **Providers → Add Custom Provider**, keep **OpenAI Compatible**, and set
   **API Type** to **Custom JSON API**.
2. Enter a name, a model prefix (for example `custom`), and the **complete
   endpoint URL**, such as `https://example.com/api/chat`. The router does not
   append `/chat/completions`, `/responses` or `/models`.
3. Choose **POST**, **PUT** or **PATCH** and the authentication: a Bearer token,
   an API key in a named header, or none. Store the secret in a connection, not
   in the template or the custom headers.
4. The request template starts as `model` plus `messages`; nothing else is required:

   ```json
   {
     "model": "{{model}}",
     "messages": "{{messages}}"
   }
   ```

   **Add field** appends optional fields such as `project_id`, as text/placeholder
   or as a JSON value (number, boolean, array or object). An untouched empty row
   is ignored. **Full JSON (advanced)** edits the whole template, for endpoints
   that need a different schema; a stored non-standard template opens there and
   is kept as it is.

   The default response path is `choices[0].message.content`. For a response
   such as `{"data":{"answer":"Example reply"}}`, set it to `data.answer`.
5. **Send Test Request** sends one real request with `ping` as the latest user
   text. Creating the provider or saving a connection never calls the endpoint.
6. Add a connection, set its default model (for example `custom-model`) and add
   that ID under the node's models. Clients then request `custom/custom-model`
   through the usual `/v1/chat/completions`, `/v1/messages` and `/v1/responses`
   endpoints.

Editing the node updates the endpoint and template on every existing
connection. Switching the node back to Chat Completions or Responses removes the
template from those connections.

## Request templates

Templates are JSON objects. Placeholders are substituted inside string values
only; keys are never substituted and nothing is executed.

| Placeholder | Value |
| --- | --- |
| `{{prompt}}` | Text of the latest user message; multi-part text is joined with newlines |
| `{{messages}}` | Chat history in the OpenAI message format the router uses internally |
| `{{model}}` | The upstream model ID, without the provider prefix |
| `{{stream}}` | `false`; the endpoint always answers JSON |
| `{{request.field}}` | A field of the translated OpenAI request, including extra fields a Chat Completions client sent |

A string that is exactly one placeholder keeps the value's JSON type:
`"history": "{{messages}}"` sends an array, not an encoded string. An embedded
placeholder (`"Explain {{prompt}}"`) is interpolated as text with normal JSON
escaping. A missing variable fails the request instead of silently dropping the
field. `request.*` reads the request after protocol translation, so a field a
translator does not carry over is unavailable; use static values or `prompt` for
anything that must work from every client protocol.

## Responses and limitations

Response paths use dot notation and numeric indexes (`answer`, `data.answer`,
`results[0].text`); `$` returns the whole document. Strings are returned as
they are; objects, arrays, numbers and booleans as their JSON text. A missing or
null field, or a body that is not JSON, answers HTTP 502.

The reply is normalized to an OpenAI Chat Completion and then converted to the
client's protocol. A client that omits `stream` gets one JSON response; an
explicit `stream: true` gets SSE, emitted after the whole business response has
arrived (this is not token-by-token upstream streaming). Native SSE endpoints,
tool-call mapping and usage-field mapping are not supported.

**No replay.** A business request may have side effects, so it is sent once:
no transient-status retry, no rotation to the node's other connections, and no
fallback to the next model of a combo. A combo's model response timeout does
not apply to a custom JSON member either: the timeout exists to move on to the
next member, which here would replay the operation, so the member runs to
completion and its answer is served. Upstream error statuses are passed
through. An empty reply is served as the answer rather than treated as an empty
upstream. Separate client requests still call the endpoint independently.

Like every compatible node, a private or loopback endpoint is refused by the
gateway's SSRF guard unless `ALLOW_PRIVATE_NETWORKING=true` is set.

## Provider configuration API

`POST /api/provider-nodes` takes the existing compatible node type with
`apiType: "custom-json"`; the node id becomes `openai-compatible-custom-json-<suffix>`.

```json
{
  "type": "openai-compatible",
  "apiType": "custom-json",
  "name": "Custom API",
  "prefix": "custom",
  "baseUrl": "https://example.com/api/chat",
  "customJson": {
    "method": "POST",
    "authType": "api-key",
    "authHeader": "X-API-Key",
    "headers": {},
    "requestTemplate": { "project_id": "demo-project", "question": "{{prompt}}" },
    "responsePath": "data.answer"
  }
}
```

`PUT /api/provider-nodes/{id}` accepts the same configuration. It is validated
on the server (method, auth type, header names and values, template depth and
size, placeholder names, response path) and only the documented fields are
stored. No schema migration is needed: the configuration lives in the node's
`data` JSON and is copied into each connection's `providerSpecificData`.

`POST /api/providers/validate` with `validateOnly: true` re-checks a custom JSON
node's configuration without calling the endpoint; without it, it sends one test
request (`modelId` picks the model).
