### Qoder catalog & model-name compatibility

- Friendly model names now resolve on the Qoder chat path: a deterministic
  alias table (`qwen3.8-flash` → `qfmodel`, `glm-5.2` → `gm51model`,
  `kimi-k2.7` → `kmodel`, …) covers the window before the first catalog fetch,
  and once the live `model/list` snapshot lands its sanitized `display_name`
  ids resolve first — the account's own catalog outranks the static table.
  Resolution is case-insensitive, strips `qoder/`/`qd/` qualifiers, and the
  built payload stamps the raw key everywhere Qoder routes by it
  (`model_config.key`, `chat_context.extra.modelConfig.key`, `X-Model-Key`).
  Unknown ids still pass through to the catalog lookup. Local compatibility
  fix, not an upstream `decolua/9router` port.
- The dashboard's Qoder model list (`GET /api/providers/{id}/models`) now
  advertises each enabled key under its sanitized `display_name` as a second
  id, so `qd/qfmodel` and `qd/qwen3.8-flash` import as one model's two names.
  Names are weighed in raw-key order, never override a live key or an earlier
  name, and unusable names (punctuation, non-ASCII, over-long, key-equal)
  advertise only the key.
- A successful Qoder/Qoder CN device login now fills the live model catalog
  immediately (bounded, best-effort, rides the device flow's own transport),
  so the first chat request after login resolves from a warm snapshot instead
  of paying for the fetch. A catalog failure never fails the login.
- Failed catalog fetches for a credential with no live snapshot now hold
  non-forced fetches off for 30 s, so an unreachable catalog no longer makes
  every request queue behind a fresh fetch and its timeout. Forced fetches
  (chat's last resort, the post-login fill) ignore the window, and a failed
  refresh never empties a landed snapshot. The 1-hour per-credential TTL is
  deliberately kept: it matches upstream's `CACHE_TTL_MS` (AGENTS.md §1), and
  chat already force-refreshes when a model is missing, so a tighter snapshot
  loop would only multiply upstream GETs.
- Fixed a provider-isolation bug in the catalog fetch: the region string was
  handed to the provider-keyed endpoint table, so a Qoder CN catalog fetch
  silently fell through to the intl gateway (`api3.qoder.sh`). CN credentials
  now reach `gateway.qoder.com.cn`. (The same pattern in the PAT-exchange path
  of `qoder.go` is owned by the auth-parity work and left untouched.)
- Verified: new regression tests fail on `origin/main` and pass with the fix;
  `gofmt`, `go vet`, `go test` clean on `internal/proxy/executor`,
  `internal/handlers/oauth`, `internal/handlers/dashboard`.