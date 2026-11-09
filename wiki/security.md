---
title: Security
order: 8
section: Using the Gateway
since: 1.9.14
---

# Security

9router-go holds provider credentials, client API keys, and the prompts that pass
through it. This page covers every control that protects them.

## Dashboard authentication

| Access | Requirement |
|---|---|
| Public | `/health`, dashboard HTML/assets, `/login`, OAuth callbacks |
| Proxy routes (`/v1/*`, `/api/chat`) | Active client API key (`Authorization: Bearer` / `X-API-Key`) |
| Dashboard APIs (`/api/*`) | Session cookie, CLI token, or API key |
| Destructive operations (shutdown, update, DB import, vault rotation) | Session or CLI token — never a client API key |
| Console log APIs | Full dashboard session or local CLI token; client API keys are rejected |

**First login.** On localhost the initial password is `123456`; change it in
Settings → Profile. On a remote host the default password is blocked (CVE-2026-56679
— a fresh install with a known default can be taken over publicly), so set
`INITIAL_PASSWORD` or connect through an SSH port-forward first.

## Client IP and rate-limit bucketing

A forwarded client-IP header is only read when the TCP peer is inside the trusted
set. Otherwise a client could rotate `X-Forwarded-For` and give every login
attempt its own rate-limit bucket.

| Variable | Effect |
|---|---|
| `TRUST_PROXY=true` | Trusts loopback and the RFC1918/link-local ranges |
| `TRUST_CLOUDFLARE=true` | Trusts Cloudflare's forwarded headers |
| `TRUSTED_PROXIES` | Comma-separated CIDRs or IPs that **replace** the defaults |

Behind an untrusted proxy, every request is bucketed by the proxy's own socket
address, so all its users share one login bucket: five failed attempts lock them
all out. `TRUSTED_PROXIES` separates them again.

## Credential vault

Provider credentials are sealed with AES-256-GCM envelope encryption
(`internal/vault/vault.go`). The master key is the root of trust — losing it
loses every stored credential, so the dashboard exposes status and rotation
through `/api/vault/status` and `/api/vault/rotate` rather than leaving it to a
file edit.

Credentials are decrypted in memory only when a dispatch needs them; the database
never holds a plaintext copy after migration.

## API key hashing

Client API keys are stored as Argon2id verifiers with a SHA-256 lookup index
(`internal/keikey/apikey.go`). The plaintext key is shown once at creation.
An auth cache keeps the 64 MiB-per-verification cost off the request path, so a
busy gateway does not spend its CPU re-verifying the same key.

## Guardrails

PII and prompt-injection detection runs before a request leaves and on the
response stream (`internal/guardrails/`). Layers resolve global → provider →
model → chain → API key, most-specific wins, and actions are
`log_only` / `warn` / `mask` / `block`. Policy CRUD and the audit log live behind
`/api/guardrails/policies` and `/api/guardrails/logs`.

> **Guardrails and Persona pull in opposite directions.** Persona can instruct a
> model to ignore its own rules; guardrails inspect what leaves your gateway for
> exactly that. Turning guardrails off does not stop the provider from seeing
> the same request — it only stops *you* from seeing it first. If an account
> matters, read the warning on the
> [Advanced Features](advanced-features.html) page before using Persona to
> bypass a model's limits.

## SSRF protection

Requests the gateway makes on your behalf — provider validation, proxy tests,
web fetch — are checked against SSRF rules before dispatch
(`internal/handlerutil/ssrf.go`), so a custom node cannot be pointed at a
link-local or metadata address and have the gateway fetch it.

## Netguard and fetch gate

`internal/netguard` and `internal/fetchgate` gate outbound fetches, so a
provider URL or a model-catalog sync cannot reach an address it should not.

## Session and CLI tokens

Dashboard sessions live in `internal/auth/session.go`; the local CLI token that
lets `9router-go` talk to its own daemon lives in `internal/auth/clitoken.go`.
The CLI token is what makes destructive operations work from a terminal without a
browser session.

## Further reading

- Security panel UI: `web/src/components/security/SecurityPanel.svelte`
- Vault internals: `internal/vault/vault.go`, `internal/db/vault.go`
- Guardrails: `internal/guardrails/`
