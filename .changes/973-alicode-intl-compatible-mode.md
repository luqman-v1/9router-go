### alicode-intl: route through compatible-mode on dashscope-intl host

The alicode-intl BaseURL moves from `coding-intl.dashscope.aliyuncs.com/v1/chat/completions` to
`dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions` so Coding Plan keys land on the
compatible-mode path of the Model Studio (alims-intl) host, which is where they are accepted
(upstream registry/alicode-intl.js:17). Auth scheme and PreserveCacheControl are unchanged.

Verified: `go test ./internal/providers/...` passes; new unit test
`TestAlicodeIntl_BaseURL_CompatibleMode` locks in the compatible-mode BaseURL.
