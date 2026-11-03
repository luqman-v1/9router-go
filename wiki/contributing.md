---
title: Contributing
order: 20
section: Contributing
since: 1.9.14
---

# Contributing

Issues and pull requests use templates, so pick the right one rather than opening
a blank report:

- **Bug report** — something behaves incorrectly. Include a reproduction,
  `9router-go version`, OS, and the log excerpt around the failure.
- **Feature request** — the problem you cannot solve today, the surface it
  touches, and whether upstream already has it.
- **Upstream parity** — a behaviour `decolua/9router` has that this gateway does
  not; link the upstream commit or PR.
- **Question** — configuration and usage help.

## Checks CI runs

```bash
go vet ./...
go test -count=1 ./...
make test-integration
cd web && bun test
make vet-svelte
```

## Before writing code

Read [`AGENTS.md`](https://github.com/luqman-v1/9router-go/blob/main/AGENTS.md).
Provider isolation and the Go/Svelte conventions there are enforced by review.

## Branches and PRs

Every PR targets `main`. Branch from a clean `origin/main`:

```bash
git fetch origin
git worktree add ../9router-go-<slug> -b fix/<slug> origin/main
```

## Changelog

Write a `.changes/<pr-number>-<slug>.md` fragment — never touch `CHANGELOG.md`.
`CHANGELOG.md` is written only by `make changelog-merge` at release time. The
dashboard's `GET /api/changelog` combines `CHANGELOG.md` with every fragment, so
your entry appears as soon as your PR merges.
