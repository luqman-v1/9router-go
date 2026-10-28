### fix(auth): reject spoofed forwarded headers unless the peer is a trusted proxy

`LoginClientIP` trusted `CF-Connecting-IP` and the leftmost `X-Forwarded-For`
entry whenever `TRUST_PROXY` or `TRUST_CLOUDFLARE` was set, without checking who
sent them. A request arriving directly from the internet with
`X-Forwarded-For: 1.2.3.4` was therefore bucketed under `1.2.3.4`, and an
attacker could rotate that header to give every login attempt its own rate-limit
bucket. The limiter still existed; it just stopped counting.

The forwarded headers are now read only when the TCP peer is inside a trusted
CIDR, and `X-Forwarded-For` is walked right to left — the last hop is the one
the peer itself appended — skipping hops that are also trusted. The first
untrusted address wins. A malformed hop fails closed to the socket address
rather than to the header value.

`CF-Connecting-IP` is additionally gated on the peer being inside Cloudflare's
published ranges, so a private or arbitrary peer can no longer speak for
Cloudflare. Both IPv4 and IPv6 ranges are listed, and the address is unmapped
before comparison so an IPv4-mapped IPv6 peer matches the IPv4 prefix.

New env knob: `TRUSTED_PROXIES` takes a comma-separated CIDR or single-IP list.
When it is set it replaces the defaults; `TRUST_PROXY=true` keeps working and
selects loopback plus the RFC1918/link-local ranges.

Verified with `TestLoginClientIPIgnoresSpoofedHeaders`, which fails on the
pre-fix code: an untrusted peer sending both `X-Forwarded-For` and
`x-9r-real-ip` must be bucketed by its socket address, and a non-Cloudflare peer
must not be believed when it sends `CF-Connecting-IP`.
