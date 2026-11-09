---
title: Installation
order: 2
section: Getting Started
since: 1.9.14
---

# Installation

## One line

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/luqman-v1/9router-go/main/install.sh | bash
```

```powershell
# Windows (PowerShell) — writes to %LOCALAPPDATA% + user PATH, no admin needed
irm https://raw.githubusercontent.com/luqman-v1/9router-go/main/install.ps1 | iex
```

## Defaults

No flags required:

| | Default |
|---|---|
| Port | `20130` |
| Data | `~/.9router` (`%APPDATA%/9router` on Windows) |
| Dashboard | `http://localhost:20130` |
| Initial password | `123456` (localhost only) |

## Running

```bash
9router-go              # run in the foreground
curl http://localhost:20130/health
./9router-go version
```

Override only when you need something different (`PORT`, `DATA_DIR`/`DB_PATH` — there are no `--port` flags):

```bash
PORT=20129 ./9router-go
DATA_DIR=/srv/9router ./9router-go
DB_PATH=/srv/9router/data.sqlite ./9router-go
HOST=127.0.0.1 ./9router-go
```

## Background mode

```bash
9router-go start          # same as: 9router-go --background   (or -d)
9router-go status         # pid, dashboard, log
9router-go restart
9router-go logs -n 100
9router-go stop
```

The detached process records itself in `DATA_DIR/run/gateway.pid` and writes its
output to `DATA_DIR/run/gateway.log`. A second `start` while one is running
refuses instead of fighting over the port.

> Windows stops the process with `TerminateProcess` instead of `SIGTERM`, so
> `stop` skips the graceful drain; use the dashboard's Shutdown button or
> `9router-go restart` when you want in-flight streams to finish first.

## Start on boot

```bash
9router-go service install     # systemd user unit, launchd agent, OpenRC/runit service, cron @reboot, or Windows logon task
9router-go service status
9router-go service start
9router-go service restart
9router-go service stop
9router-go service uninstall
```

`install` picks the native backend for the host. systemd, launchd, and cron all
install into the **user** scope and need no root; runit's `~/service` path too.
Two exceptions: OpenRC is a system service and needs root (run with `sudo`, or
pick `--backend cron`), and on Windows install registers a scheduled task via
`schtasks`, which requires an Administrator console. Force a backend with
`--backend systemd|launchd|openrc|runit|cron`.

## Docker

```bash
docker run -d --name 9router-go --restart unless-stopped \
  -p 20130:20130 -v "$HOME/.9router:/data" \
  -e PORT=20130 -e DATA_DIR=/data \
  -e INITIAL_PASSWORD=your-secure-password \
  luqmenul/9router-go:latest
```

Experimental builds are marked **Pre-release** and never offered to a normal
install; Docker Hub tags them `exp` (`luqmenul/9router-go:exp`), never `latest`.

## Building from source

Prerequisites: Go 1.27 and Bun 1.x (the dashboard is embedded into the binary, so build web first).

```bash
git clone https://github.com/luqman-v1/9router-go.git
cd 9router-go
make web-build   # bun install --frozen-lockfile && bun run build
make build       # embeds VERSION into the Go binary
```

## First login

- **Localhost (`localhost` / `127.0.0.1`)**: the initial compatibility password is
  `123456`. Once logged in, change it in **Settings → Profile**.
- **Remote / VPS / Docker / LAN**: for security (preventing public takeover of
  fresh installs with known defaults), remote access blocks the default `123456`
  password. Either:
  1. Set `INITIAL_PASSWORD` on launch (recommended):
     ```bash
     INITIAL_PASSWORD="your-secure-password" ./9router-go
     ```
  2. Or access via SSH port-forwarding first:
     ```bash
     ssh -L 20130:127.0.0.1:20130 user@remote-host
     # open http://localhost:20130, log in with 123456, then change the password
     ```
