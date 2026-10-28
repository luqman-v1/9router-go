### fix(media): let `ALLOW_PRIVATE_NETWORKING` reach the literal-IP branch too

`targetPrivate` answered `ALLOW_PRIVATE_NETWORKING` on the name branch only: a
literal IP was judged directly by `proxy.IsPrivateIP` before the switch was
consulted, so `http://127.0.0.1:8080/` was refused even with the operator's flag
switched on, while `http://localhost/` — the same address by name — was
forwarded. A caller could not tell which shape the guard would accept.

The flag is now answered first, before either branch, so a literal IP and a name
that resolves to it get the same verdict. `targetPrivate`'s comment says which
question the flag answers.

Verified with a table-driven `TestTargetPrivate_RespectsAllowPrivateNetworking`
that runs both flag values across both shapes: four private literals (`127.0.0.1`,
`10.0.0.5`, `::1`, `169.254.169.254`), a loopback name (`localhost`), and public
controls (`example.com`, `8.8.8.8`). The four `allowed: * literal` rows fail on
the pre-fix code — `targetPrivate("127.0.0.1") with ALLOW_PRIVATE_NETWORKING=true
= true, want false` — and pass after.

The previous version of this test asserted only on `example.com`, a public name,
so it never reached the literal branch it was meant to cover.
