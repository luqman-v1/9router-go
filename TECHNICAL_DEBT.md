# Technical Debt

## Duplicated Helpers

**`getString`** — 3 copies of the same helper:

| File | Line |
|------|------|
| `internal/auth/token.go` | 145 |
| `internal/db/proxyPools.go` | 66 |
| `internal/providers/oauth.go` | 174 |

6 lines each, identical logic. Extract to shared `internal/handlerutil` when touching any of the three.

## Combo File Size

`internal/handlers/combo.go` — 834 lines, mixing:
- Fusion logic (judge prompts, panel collector)
- Capability detection
- Route parsing
- Tool history flattening

Split when next fusion tweak is needed.

## MITM DNS Fragility

`internal/mitm/dns.go` — `sudo sed` munges `/etc/hosts`. Resilient on Linux but macOS `sed -i ''` vs `sed -i` mismatch is a papercut. Guard when adding platform support.

## Remaining (wontfix now)

| Item | Why not |
|------|---------|
| `strings.Builder` in `accounts.go:214` loop (3 iters max) | Negligible |
