### feat(proxy): park a refused proxy per provider instead of re-dialling it next request

An OpenCode free-tier `429 FreeUsageLimitError` is the gate's verdict on one
egress IP, not on the account or the model. The router acted on that correctly
for the length of one request — `excludedPools` drops the pool for the turn — and
then forgot: the map is local to `handleAccountFallback`, so the next request
walked back into the proxy that had just been refused and spent another attempt
relearning it. With a twenty-proxy pool behind one pool name, nothing in the logs
said which egress had served a turn either.

- **Cooldowns are keyed by (provider, proxy), never by the proxy alone.** The
  same proxy stays available to every other provider, which never received the
  verdict. A proxy-only key would silently shrink the pool for providers that
  did not refuse it, and the symptom would be traffic leaving from the host's
  real IP with no error to explain why. The proxy is identified by its
  configured URL rather than its observed exit IP: the URL is what the operator
  assigned, and two proxies in one pool were measured exiting via addresses
  other than their hostnames, so the observed IP is both unstable and
  surprising as a key.
- **The wait comes from upstream, not from the router.** `Retry-After` is
  plumbed end to end (e27ca52d), so the parked duration is the window the gate
  named; a refusal with no stated window parks nothing, since inventing a
  duration would drop an egress from rotation on a guess. A second refusal
  extends the window rather than shortening it, so concurrent requests cannot
  race the deadline backwards.
- **OpenCode gets its own ceiling (`openCodeProxyMaxCooldown`).** The generic
  `maxResetCooldown` caps *account* cooldowns and is shared by every provider,
  so raising it there would hold rate-limited accounts out of rotation for hours
  on providers whose windows are seconds. e27ca52d left the 2h account clamp in
  place and recorded the mismatch — OpenCode's real free-tier reset is ~21.5h —
  as cross-provider policy rather than an OpenCode fix. This is that fix, on the
  egress dimension where it belongs: it parks a proxy, not an account, so no
  other provider's behaviour moves.
- **A fully parked pool still serves traffic.** With every URL cooling the
  resolver falls back to ordinary rotation. Failing the turn would refuse
  service, and falling back to the direct client would publish the host's real
  IP — both worse than trying a proxy the gate may have reopened.
- **The egress label names the proxy.** `resolveEgress` now carries the
  redacted proxy URL, so a usage row reads `Rot20 (socks5://***@103.17.36.55:30259)`
  instead of just `Rot20`. Credentials are stripped before the label is built:
  a pool of twenty proxies carries twenty passwords, and this string lands on
  every usage row and every fallback log line.
- **Recording is opt-in per provider.** Only the OpenCode lanes record today —
  its gate is the one measured to answer 429 for a spent egress with an
  hour-scale window — while the map itself is provider-keyed so a later provider
  needs no migration.

Verified against a live gateway with a twenty-proxy SOCKS5 round-robin pool: nine
proxies were parked for 6h with credentials redacted, and none was drawn again
afterwards (zero violations across the following requests). Unit tests pin the
provider keying, alias canonicalisation, expiry, extend-never-shorten, the
bounding of the map, and the three resolver behaviours (skip for the refusing
provider, still offer to others, degrade rather than fail when all are parked).