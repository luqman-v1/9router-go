### fix(proxy): walk a proxy pool within one connection instead of giving up

A proxy pool is many egresses behind **one** connection, but the account fallback
loop advances over **connections**. A provider with one connection and a hundred
proxies therefore got exactly **one attempt per request**: the resolver picked one
URL, the gate refused it, and `free tier refused after retries` was reported while
ninety-nine egresses had never been tried.

The log that prompted this shows the failure exactly — three dead sockets and a
429 across a few seconds, then surrender, on a pool that still had proxies to
offer:

```text
15:48:35  egress=...103.17.37.100  connection refused
15:48:42  egress=...103.17.38.11   connection refused
15:48:43  egress=...103.17.39.146  connection refused
15:48:48  egress=...103.17.36.106  429
15:48:48  opencode free tier refused after retries; reporting classified failure
```

- **The forward is followed by a pool walk.**
  `tryForwardWithConnection` is now called through `walkPoolOnFreeTierRefusal`,
  which re-enters the forward against the next proxy in the same pool. Each pass
  goes through the normal resolver, so rotation, the per-proxy client cache and
  the strict-proxy marker all behave as they do on a first attempt.
- **Only a free-tier refusal triggers the walk.** That refusal is the gate saying
  "not this egress", a fact about the proxy for this request. A dead socket or a
  5xx keeps the existing handling: it may be the upstream's fault, and skipping
  a healthy proxy over it would walk a good pool on someone else's bad afternoon.
  A *different* error on a later proxy ends the walk and hands that error back.
- **Bounded by the pool size.** A hundred proxies is a hundred attempts, which is
  what the operator asked for by assigning a hundred. The walk stops early when
  the client disconnects, and when the pool is exhausted it returns the **last
  upstream refusal**, so the caller's own handling — pool exclusion, then
  direct — is unchanged and the operator sees what the gate said rather than an
  internal error.
- **Refusals are tracked per pool on the per-request `ConnectionData`.** It has
  to outlive the loop body: `connData` is rebuilt from the row each iteration, so
  state on it would be discarded by the retry that needs it. Keying by pool keeps
  one pool's spent proxies from affecting another's.
- **`nextProxyURL` skips spent proxies**, and draws one extra time beyond the
  pool size. `NextURL` advances its cursor *before* reading, so a pool of N
  returns each URL once per N calls but starts mid-cycle — a loop of exactly N can
  revisit a URL and never reach another, reporting the pool exhausted while
  untried proxies remained.
- **The proxy is redacted before it reaches a log line.** A hundred-proxy pool
  carries a hundred passwords.

- **The keyless no-auth lane walks too.** The OpenCode free tier is keyless, so
  it takes the synthesized no-auth branch (`ConnectionID "default"`) rather than
  the multi-connection loop. The walk was wired only into the latter, so that
  lane called the forward once, classified the refusal, and returned — the
  operator's pool was never walked. Both paths now run it;
  `walkPoolOnFreeTierRefusalFor` takes the connection identity because this lane
  has no stored row to pass.

- **A dead proxy socket walks too.** The two failures look identical in the log —
  `Post ...: <error>` with status 0 — but only one is fixed by rotating. A
  transport failure is now treated as egress-owned after `proxyEgressIsDead`
  proves it, by dialling the proxy's own listen address with a 3s deadline: a
  socket that refuses that would refuse any target. Three dead sockets in a pool
  used to produce three instant 502s with the rest of the pool untried. The
  probe runs only on the failure path, so a successful request pays nothing, and
  a proxy that accepts the connection then fails (TLS, a rejected CONNECT, or
  the provider's own 5xx) is deliberately NOT reported dead — those are the
  upstream's answers through a working egress.

- **A 60s wall-clock budget bounds the walk**, so a pool of hundreds of dead
  proxies cannot hold a client request for minutes; the last upstream answer is
  returned when it expires.

Not addressed here: a `429` that names a long reset (`retry_after_s=7200` in the
report) is a per-egress window no walk can shorten. That is the per-provider proxy
cooldown's job. This change makes the router *use the pool it was given*; that one
makes it stop re-trying an egress that has said no.

Verified against a live gateway with a six-proxy SOCKS5 pool: one request produced
six attempts through six distinct proxies, where the same request previously made
one. Unit tests pin walking past a refused proxy, exhaustion reporting, plain
rotation when nothing is refused, per-pool scoping, and the happy path being
untouched (a successful first attempt returns immediately, so no extra request is
made).