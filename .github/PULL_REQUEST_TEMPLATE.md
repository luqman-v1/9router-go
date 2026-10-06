name: Pull request
description: Send a change to 9router-go for review
title: ""
labels: []
body:
  - type: markdown
    attributes:
      value: |
        Thanks for contributing. Read the checklist at the bottom — most of this repo's CI failures are one of
        those gates, and a PR that fails them costs a round trip.

  - type: textarea
    id: summary
    attributes:
      label: What this changes
      description: One paragraph. State the user-visible effect, not the file list.
    validations:
      required: true

  - type: textarea
    id: issue
    attributes:
      label: Linked issue
      description: |
        Issue number this closes or advances. Put `Closes #<n>` in the **PR title** as well — GitHub only
        auto-closes from the title when the PR is squash-merged.
    validations:
      required: true

  - type: textarea
    id: type
    attributes:
      label: Type
      description: |
        Conventional type matching the branch: `fix/…`, `feature/…`, or `docs/…`.
        Allowed types here: `fix`, `feat`, `perf`, `refactor`, `test`, `ci`, `build`, `docs`, `chore`, `release`.
    validations:
      required: true

  - type: dropdown
    id: surface
    attributes:
      label: Surface
      options:
        - "Routing / combo fallback (internal/handlers/chat)"
        - "Proxy executor or SSE translation (internal/proxy, internal/translator)"
        - "Provider registry / model catalog (internal/providers)"
        - "Dashboard API (internal/handlers/dashboard)"
        - "Dashboard UI (web/src)"
        - "Build, packaging, or CI"
        - "Documentation only"
    validations:
      required: true

  - type: textarea
    id: verification
    attributes:
      label: How this was verified
      description: |
        Paste what you actually ran and what it printed. A claim without output is unverified.

        - `go vet ./...`
        - `go test -count=1 ./...`
        - `make test-integration` (real router, real DB, fake upstreams)
        - `cd web && bun test`
        - `make vet-svelte`
        - `make build`
      render: shell
    validations:
      required: true

  - type: textarea
    id: behavior
    attributes:
      label: Behaviour notes
      description: |
        For a bug fix: the failing case before, and the passing case after.
        For a contract change: the endpoint, SSE chunk shape, status code, or SQLite schema column that moves.
    validations:
      required: false

  - type: textarea
    id: breaking
    attributes:
      label: Breaking changes
      description: |
        Any client-visible break — `/v1/*` request or response fields, SSE framing, dashboard endpoints,
        database schema, CLI flags. Write "None" if there are none.
    validations:
      required: true

  - type: textarea
    id: changelog
    attributes:
      label: CHANGELOG entry
      description: |
        Added under `## [Unreleased]` in `CHANGELOG.md`? Newest entry first; bug fixes above features.
    validations:
      required: true

  - type: checkboxes
    id: checklist
    attributes:
      label: Checklist
      options:
        - label: Branched from an up-to-date `main` and is ≤ ~300 changed lines.
          required: true
        - label: Title is Conventional Commits with a scope, plus `Closes #<n>` when this closes an issue.
          required: true
        - label: `go vet ./...` and `go test -count=1 ./...` pass locally.
          required: true
        - label: Dashboard changes pass `cd web && bun test` and `make vet-svelte`.
        - label: Routing, auth, rotation, or usage changes have an integration case under `internal/integration/`.
        - label: New or changed wire behaviour is covered by a test that asserts the response a client sees.
        - label: No provider is aliased, hijacked, or switched by substring matching (`AGENTS.md` §3).
          required: true
        - label: No secrets, tokens, or machine-local paths in the diff.