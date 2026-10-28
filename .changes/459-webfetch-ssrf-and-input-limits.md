### fix(media): refuse a web-fetch target that points inside the network

`/v1/web/fetch` forwards a caller-supplied URL to an extractor provider, and
`HandleWebFetch` passed the body straight through: no scheme check, no address
check, no size limit. A caller could name `http://169.254.169.254/latest/meta-data/`
or any other internal address and have the gateway fetch it.

Three changes to the handler:

- **Address refusal.** A literal IP is judged directly; a name is resolved once
  before dispatch, and any private, loopback, link-local, or metadata address
  in the answer is refused with 403 `address_forbidden`. `ALLOW_PRIVATE_NETWORKING`
  still switches this off for the operator who needs it.
- **Scheme check.** Only `http` and `https` are accepted; `file:`, `gopher:`,
  and anything else is a 400.
- **Body limit.** The read is capped at 1 MiB.

The check is a *refusal*, not the boundary. The socket-level guard in
`proxy.NewSafeDialer` is what actually stops the connection, and it also covers
the window between the lookup and the dial. Checking here means the common case
answers with a 403 that says why, instead of surfacing as an opaque transport
error from a provider several layers down. `targetPrivate` says so in its own
comment so nobody later mistakes it for the security boundary.

The same handler now also honours `provider` when `model` is absent: the body
carries the extractor in `model`, and a caller that sent only `provider` was
being dispatched to the default extractor instead of the one it named.

Verified with four tests. `TestHandleWebFetch_RejectsPrivateTargets` covers six
forms — loopback v4 and v6, `localhost`, the metadata address, two RFC 1918
ranges — and all six fail on the pre-fix code with the request forwarded
instead of refused. The scheme, missing-URL, and `ALLOW_PRIVATE_NETWORKING`
cases are covered too.

Note for anyone running the package's tests: `main_test.go` sets
`ALLOW_PRIVATE_NETWORKING=true` process-wide so the fixtures can reach their
`httptest` servers. The SSRF test sets it back to `false` locally, which is why
it can test the refusal at all.
