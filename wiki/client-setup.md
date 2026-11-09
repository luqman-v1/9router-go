---
title: Client Setup
order: 3
section: Getting Started
since: 1.9.14
---

# Client Setup

## From a CLI tool

```
Claude Code / Codex / OpenClaw / Cursor / Cline Settings:
  Endpoint: http://localhost:20130/v1
  API Key:  [Settings → API Keys in the dashboard]
  Model:    kr/claude-sonnet-4.5
```

For Claude Messages clients: `ANTHROPIC_BASE_URL=http://localhost:20130/v1`.

## curl example

```bash
curl http://localhost:20130/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer sk-your-api-key' \
  -d '{"model":"ag/gemini-3.8-flash-high","messages":[{"role":"user","content":"Hello"}],"stream":true}'
```

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `20130` | HTTP port |
| `HOST` / `BIND_ADDR` | all interfaces | Listener address |
| `DATA_DIR` | `~/.9router` (`%APPDATA%/9router` on Windows) | Data root |
| `DB_PATH` | `$DATA_DIR/db/data.sqlite` | SQLite file |
| `INITIAL_PASSWORD` | unset (fallback `123456` locally) | First dashboard password |
| `RTK_ENABLED` | `true` | RTK input compression |
| `CAVEMAN_ENABLED` / `PONYTAIL_ENABLED` | `false` | Output style savers |
| `AUTO_UPDATE` | `false` | Background self-update |
| `HTTP_PROXY` / `HTTPS_PROXY` | Go defaults | Upstream egress proxy |
| `PPROF_ENABLED` | `false` | Expose `/debug/pprof/*` |
| `TRUST_PROXY` / `TRUST_CLOUDFLARE` | unset | Trust forwarded client-IP headers |
| `TRUSTED_PROXIES` | unset | CIDR list of trusted reverse proxies |
| `GEMINI_WEB_ROTATION` | `on` | Renew the Gemini Web session cookie in the background |

`.env.example` documents the security-sensitive subset and OAuth overrides.

## Authentication

- **Public**: `/health`, dashboard HTML/assets, `/login`, OAuth callbacks.
- **Proxy routes** require an active client API key (`Authorization: Bearer` / `X-API-Key`).
- **Dashboard APIs** require a session cookie, CLI token, or API key; destructive
  operations (shutdown, update, DB import) require a session or CLI token.
- The API server defaults to all interfaces — bind to localhost or protect the
  port outside trusted machines.

## Trusting a proxy

A forwarded client-IP header is only read when the TCP peer is inside the trusted
set — otherwise a client could rotate `X-Forwarded-For` and give every login
attempt its own rate-limit bucket.
`TRUST_PROXY=true` trusts loopback and the RFC1918/link-local ranges;
`TRUSTED_PROXIES` takes a comma-separated list of CIDRs or single IPs and
**replaces** those defaults.

Behind a proxy that is *not* in the trusted set, every request is bucketed by the
proxy's own socket address, so all users of that proxy share one login bucket:
five failed attempts lock them all out.

## Gemini Web session renewal

The `gemini-web` provider reaches `gemini.google.com` with a session cookie, and
Google rotates the session's timestamp cookie (`__Secure-1PSIDTS`) every few
minutes. The router renews it in the background, so a signed-in account keeps
reaching the models it pays for instead of falling back to one model after a few
minutes.

There are three ways to get a session, and the dashboard offers two of them as a
button:

1. **A throwaway browser** — the safest. The button opens a browser with a
   profile of its own, waits for the Google sign-in, reads the session out of it,
   then closes the browser and deletes the profile.
2. **A browser that is already signed in** — the convenient one. The button opens
   the browser's own cookie database, takes the key that seals it from the
   platform's secret store, and unseals the cookies. **A browser that is still
   open keeps rotating the same session** and the router renews it from the other
   side, and the two cancel each other out — which signs the account out. Close
   the browser first, or use the throwaway browser above.
3. **A cookie you paste** — the fallback. Copy the cookie header from an
   Incognito window that you close right after, then paste it into the connection
   form.

Set `GEMINI_WEB_ROTATION=off` to run the router without renewing anything at
all — the right choice when the credential belongs to a browser you want to keep
using.
