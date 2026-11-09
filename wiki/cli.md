---
title: CLI
order: 18
section: Using the Gateway
since: 1.9.14
---

# CLI

Every command comes from the single binary.

## Running the gateway

```bash
9router-go                  # foreground (port 20130, data ~/.9router)
9router-go --background     # detach and return; same as `9router-go start`
9router-go -d               # short alias
9router-go version          # version details, plus an update check
9router-go update           # check and self-update
curl http://localhost:20130/health
```

## Background daemon

```bash
9router-go start          # run in the background and return
9router-go status         # is it running? pid, dashboard, log
9router-go restart        # replace the running daemon
9router-go logs -n 100    # tail the run log (default 40 lines)
9router-go stop           # stop it
```

The detached process records itself in `DATA_DIR/run/gateway.pid` and writes its
output to `DATA_DIR/run/gateway.log`. A second `start` while one is running
refuses rather than fighting over the port. A daemon killed from Task Manager
leaves nothing behind: the stale pid file is cleaned on the next command.

`status`, `stop`, and `logs` resolve the port from the same config the server
binds — including a `.env` file — so they never probe the wrong address.

> Windows stops the process with `TerminateProcess` instead of `SIGTERM`, so
> `stop` skips the graceful drain. Use the dashboard's Shutdown button or
> `9router-go restart` when you want in-flight streams to finish first.

## Service (start on boot)

```bash
9router-go service install     # pick the host's native backend
9router-go service status      # which entry is in effect, and whether it is active
9router-go service start       # start the supervised service now
9router-go service restart
9router-go service stop        # stop now, stay enabled for boot
9router-go service uninstall   # remove the entry
```

Force a backend: `--backend systemd|launchd|openrc|runit|cron`.

| Backend | Scope | Needs root |
|---|---|---|
| systemd | user unit | no |
| launchd | user agent | no |
| OpenRC | system service | **yes** — use `sudo` or `--backend cron` |
| runit | `~/service` | no |
| cron `@reboot` | user crontab | no |
| Windows | scheduled task via `schtasks` | **yes** — Administrator console |

The Windows task runs as the current user at logon, so it reads that user's data
dir rather than `SYSTEM`'s.

## Interactive menu

Running `9router-go` with no arguments in a terminal opens a menu:

```text
1) Start the gateway
2) Start in the background
3) Show status
4) Stop
5) Show status
6) Run in the background with a tray icon (frees the terminal)
0) Exit
```

**Option 6 — tray.** The tray icon lives in a companion binary
(`cmd/9router-tray`), not the main one: a GUI toolkit cannot be linked into the
gateway without breaking the `CGO_ENABLED=0` cross-build for every target. Only
the *sibling* binary is tried, so a PATH lookup cannot pick up another install's
tray talking to a different gateway. Right-click the icon to open the dashboard
or quit.

```bash
# the tray ships as its own package
9router-tray --port 20130
```

## MITM proxy

Intercepts CLI tool traffic so it can be routed through the gateway:

```bash
9router-go mitm enable     # DNS redirect + TLS intercept on :443
9router-go mitm status
9router-go mitm disable    # stop the proxy and remove DNS entries
```

## Flags

There are no `--port` flags; use environment variables.

### Server

| Variable | Default | Effect |
|---|---|---|
| `PORT` | `20130` | HTTP port |
| `DATA_DIR` | `~/.9router` (`%APPDATA%/9router` on Windows) | Data root |
| `DB_PATH` | `$DATA_DIR/db/data.sqlite` | SQLite file |
| `HOST` / `BIND_ADDR` | all interfaces | Listener address |
| `INITIAL_PASSWORD` | unset (`123456` on localhost) | First dashboard password |
| `JWT_SECRET` | generated | Session signing secret — set it in production |
| `API_KEY_SECRET` | built-in default | API key hashing secret |
| `MACHINE_ID_SALT` | built-in default | Machine identity salt |
| `9ROUTER_BACKGROUND` | unset | Same as `--background` |
| `JWT_SECRET` | generated at `$DATA_DIR/jwt-secret` | Session signing secret — set it in production so a restart does not invalidate sessions |
| `AUTH_COOKIE_SECURE` | unset | Set the `Secure` flag on session cookies (behind TLS) |
| `FX_LOGGING` | unset | Log Fx dependency-injection events |
| `NINEROUTER_PEER_TOKEN` | unset | Peer token for internal routing |

### Token savers

| Variable | Default | Flag |
|---|---|---|
| `RTK_ENABLED` | `true` | `--rtk=false` |
| `CAVEMAN_ENABLED` | `false` | `--caveman=true` |
| `PONYTAIL_ENABLED` | `false` | `--ponytail=true` |
| `ADHD_ENABLED` | `false` | `--adhd=true` |
| `INJECTION_GUARD_DISABLED` | `false` | `--no-injection-guard` |
| `AUTO_UPDATE` | `false` | `--auto-update=true` |

A flag overrides the stored dashboard setting for that run only.

### Networking and trust

| Variable | Default | Effect |
|---|---|---|
| `HTTP_PROXY` / `HTTPS_PROXY` | Go defaults | Upstream egress proxy |
| `TRUST_PROXY` | unset | Trust loopback + RFC1918/link-local forwarded IPs |
| `TRUST_CLOUDFLARE` | unset | Trust Cloudflare forwarded IPs |
| `TRUSTED_PROXIES` | unset | CIDR/IP list that **replaces** the defaults |
| `PPROF_ENABLED` | `false` | Expose `/debug/pprof/*` |
| `GEMINI_WEB_ROTATION` | `on` | Renew the Gemini Web session cookie in the background |
| `FORCE_FALLBACK_ON_ALL_UNAVAILABLE` | unset | When every account of a provider is in cooldown, serve through whichever frees up first |
| `STRICT_SSL` | unset | Set true/1 to forbid the insecure-TLS retry |

### OAuth overrides

Built-in public client credentials exist for antigravity, gemini-cli, xai, codex,
iflow and others, so most operators never set these. Override per provider:

```bash
ANTIGRAVITY_OAUTH_CLIENT_ID=...
ANTIGRAVITY_OAUTH_CLIENT_SECRET=...
CODEX_OAUTH_CLIENT_ID=...
```

See `.env.example` for the full list.

## Verifying a release

Download from [GitHub Releases](https://github.com/luqman-v1/9router-go/releases/latest)
and verify against `SHA256SUMS.txt`.
