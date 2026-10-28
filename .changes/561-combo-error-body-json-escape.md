### Combo error bodies are marshalled, not formatted

A combo whose every member failed with a non-`upstreamError` error answered the
client with a body built by `fmt.Sprintf`, interpolating the raw Go error text
into a JSON string literal:

```go
Body: []byte(fmt.Sprintf(`{"error":{"message":"upstream error: %v",...}}`, fwdErr))
```

`%v` emits the error text verbatim, so any character that is illegal raw inside
a JSON string breaks the whole response. Go's own `url.Parse` error is the
common carrier: it embeds the offending URL in double quotes and reports the
invalid character in quotes too, so a connection `baseUrl` that cannot be parsed
into a request produces

```json
{"error":{"message":"upstream error: forward to http://a"\b: create request: parse "http://a"\b": invalid character "\" in host name",...}}
```

The quote inside the message closes the JSON string early and the backslash that
follows becomes a bare token. A JavaScript client reports that as
`JSON Parse error: Unrecognized token '\'` and reads nothing at all — no status,
no type, no code — so a misconfigured endpoint surfaced as an opaque parse crash
instead of a 502 naming the cause.

`internal/handlers/chat/combo.go` now renders the terminal error through
`upstreamErrorBody`, which encodes the message with `json.Marshal`. Both the
Chat Completions and the Claude Messages combo loops use it. The same defect
existed in three more places a combo can reach, and they are fixed the same way:
`internal/proxy/executor/providers.go` (kiro request-build failure),
`internal/proxy/executor/stream.go` (commandcode error event) and
`internal/mitm/handlers/base.go` (MITM proxy `SendError`).

Verified: three new cases in `internal/handlers/chat/combo_error_body_test.go`
drive the real handler with a connection whose `baseUrl` is `http://a"\b` and
assert the client can decode the body. Against the pre-fix code they fail with
`jsontext: invalid character '\\' after object value ... within "/error"`;
with the fix they pass. `go test ./internal/handlers/chat/ ./internal/proxy/executor/
./internal/mitm/...` is green.
