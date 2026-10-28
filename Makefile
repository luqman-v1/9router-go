BINARY_SUFFIX := $(if $(findstring Windows_NT,$(OS)),.exe,)
BINARY_NAME := 9router-go$(BINARY_SUFFIX)
# A bare `make` builds the binary and stops there — it never runs the server.
# `run`/`dev` are explicit, separate goals on purpose.
.DEFAULT_GOAL := build
# Central version — single source: VERSION file, fallback to version.json, then git.
#
# Read through $(file <VERSION) rather than $(shell cat VERSION 2>/dev/null):
# under cmd.exe the POSIX /dev/null redirect does not exist, the shell call
# failed, and VERSION silently expanded to empty — which embedded an empty
# -X CurrentVersion into the binary.
VERSION ?= $(strip $(file <VERSION))
ifeq ($(VERSION),)
VERSION := $(strip $(shell cat version.json 2>/dev/null | grep -o '"latestVersion": *"[^"]*"' | cut -d'"' -f4))
endif
ifeq ($(VERSION),)
VERSION := $(strip $(shell git describe --tags --always 2>/dev/null))
endif
ifeq ($(VERSION),)
VERSION := 1.0.0
endif
# On Windows (cmd.exe), invoking `./$(BINARY_NAME)` fails with "'.' is not recognized".
# On macOS/Linux, `.` is not on PATH, so a bare command fails with "command not found".
ifeq ($(findstring Windows_NT,$(OS)),Windows_NT)
BINARY := $(BINARY_NAME)
else
BINARY := ./$(BINARY_NAME)
endif

# DATA_DIR stays empty unless the caller overrides it, so the binary applies its
# own per-platform default (%APPDATA%\9router on Windows, ~/.9router elsewhere).
# Do NOT default to $(HOME)/.9router: GNU Make on native Windows expands $(HOME)
# to empty, which resolved to a literal "/.9router" -> C:/Program Files/Git/.9router,
# so every launch wrote a throwaway DB and 401'd on every dashboard call.
DATA_DIR ?=
RTK ?=
CAVEMAN ?=
PONYTAIL ?=
AUTO_UPDATE ?= false

# Double quotes, not single: cmd.exe treats ' as a literal character, so the
# single-quoted form passed a quoted symbol name straight to the linker.
LDFLAGS := -s -w -X "9router/proxy/internal/updater.CurrentVersion=$(VERSION)"

.PHONY: build run dev version update test test-short test-live test-integration vet vet-integration bench bench-go cross mitm-enable mitm-disable mitm-status docker docker-build clean help web-build web-dev changelog-merge

## web-build — build frontend static assets (Svelte/Vite) into web/dist
#
# The freshness decision lives in web/scripts/web-build.ts, which fingerprints
# every frontend build input (web/src, web/public, package.json, bun.lock, the
# tsconfigs, vite.config.ts) and rebuilds when that fingerprint changes. The
# old guard — rebuild only when web/dist/index.html was missing — skipped every
# rebuild on a machine that already had a dist, so pulling main embedded a stale
# SPA in the binary until someone remembered FORCE=1.
#
# Shell-agnostic on purpose: this recipe runs under /bin/sh (Git Bash, CI) or
# cmd.exe (a plain Windows prompt). A POSIX `[ -nt x ]` comparison dies under
# cmd and is unreliable across filesystems anyway, so it is delegated to Bun,
# which is already a prerequisite of every path through this target.
web-build:
	@bun web/scripts/web-build.ts

## build — compile binary with version embedding
build: web-build
	go build -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/9router-go/
	go build -ldflags="$(LDFLAGS)" -o opencode-go$(BINARY_SUFFIX) ./cmd/9router-go/

# `PORT=20130 ./9router-go` is POSIX env-assignment prefix syntax. cmd.exe does
# not parse it: it tried to execute a program literally named "PORT" and failed
# with "'PORT' is not recognized as an internal or external command". GNU Make's
# export directive is portable, so use it and drop the prefix entirely.
#
# Guard on origin so a bare `make run` does not shadow the config with the
# Makefile default: a `?=` default has origin "file", while a value coming from
# the environment or the command line has origin "environment"/"command line".
# Anything the operator set still reaches the binary; only the unset case is
# skipped, leaving the binary to read .env (viper) on its own.
ifneq ($(origin PORT),file)
export PORT
endif
ifneq ($(origin DATA_DIR),file)
export DATA_DIR
endif
ifneq ($(origin RTK),file)
export RTK
endif
ifneq ($(origin CAVEMAN),file)
export CAVEMAN
endif
ifneq ($(origin PONYTAIL),file)
export PONYTAIL
endif

## run — start proxy (PORT=20130)
run: build
	$(BINARY) $(if $(RTK),--rtk=$(RTK)) $(if $(CAVEMAN),--caveman=$(CAVEMAN)) $(if $(PONYTAIL),--ponytail=$(PONYTAIL)) --auto-update=$(AUTO_UPDATE)

## dev — start with go run (auto-rebuild)
dev:
	go run -ldflags="$(LDFLAGS)" ./cmd/9router-go/ $(if $(RTK),--rtk=$(RTK)) $(if $(CAVEMAN),--caveman=$(CAVEMAN)) $(if $(PONYTAIL),--ponytail=$(PONYTAIL)) --auto-update=$(AUTO_UPDATE)

## web-dev — Vite dev server (HMR) on :5173, API proxied to Go :20130. FE changes hot-reload without rebuilding the binary.
web-dev:
	cd web && bun run dev

## version — display binary version info
version: build
	$(BINARY) version

## update — check and install binary self-update
update: build
	$(BINARY) update

# changelog-merge — fold the pending .changes/ fragments into CHANGELOG.md
#
# Release-time only. CHANGELOG.md is written by exactly this step and by nothing
# else, which is the whole point: while every PR edited the top of
# `## [Unreleased]`, any two PRs landing in the same window collided there, and
# GitHub's web merge ignores .gitattributes merge drivers so no setting could
# have merged them. A per-PR fragment is a unique file and cannot collide.
#
# Run it once per release, after `scripts/bump-version.sh` has established the
# tag. TAG is required — without it the fragments would be filed under a heading
# release.yml's awk cannot find, and the release page would fall back to raw
# commit subjects.
## changelog-merge — file .changes/ fragments into CHANGELOG.md under TAG
changelog-merge:
	@test -n "$(TAG)" || { echo "TAG is required: make changelog-merge TAG=v1.9.11"; exit 1; }
	go run ./cmd/changelog-merge --tag $(TAG)

## test — run all unit tests
test:
	go test ./... -v

## test-short — run tests (quiet)
test-short:
	go test ./...

# test-race runs the same command as CI's Race detector job. -race needs cgo,
# so it stays out of `test` and `test-short`: a toolchain without a C compiler
# would otherwise fail there with a confusing build error rather than a clear
# "this target needs cgo". The live upstream tests are opt-in and stay skipped.
## test-race — run all unit tests under the race detector
test-race:
	go test -race -count=1 -timeout 10m ./...

# Live upstream tests hit real providers with your own credentials from
# ~/.9router/db/data.sqlite. They are skipped everywhere else on purpose: the
# free-tier models these cover (oc/space-bunny-free, muse-spark-*-contributor-free)
# are rate limited per IP and shared with other users, so a run that passes today
# can fail tomorrow for reasons unrelated to this gateway. CI never sets the
# variable, which keeps `go test ./...` hermetic.
#
# Any status that means "not right now" upstream (429/403/402/502/503/504) is
# reported as a skip carrying the provider's body, not a failure. A 400 or 500
# still fails, because those are this gateway's fault.
#
# Verify a provider before trusting it: an expired OAuth token or a connection
# left inactive will make every test skip for the wrong reason.
## test-live — run the live upstream tests against real providers (local only)
test-live:
	9ROUTER_LIVE_TESTS=1 go test -count=1 -v ./internal/handlers/chat/ -run 'Live|MuseSpark'

## vet-svelte — svelte-check ratchet: blocks unresolved identifiers, pins type debt
vet-svelte:
	cd web && bun install --frozen-lockfile && bun run ratchet:svelte

# The integration suite imports internal/app -> internal/handlers -> web, and
# web/embed.go embeds web/dist at compile time. web-build is a no-op once the
# SPA has been built, so this costs nothing on a warm tree.
## test-integration — run the feature-level integration suite (real router, real DB, fake upstreams)
test-integration: web-build
	go test -tags=integration -race -count=1 ./internal/integration/... -v

## vet-integration — vet the integration suite, which the untagged `vet` target skips
vet-integration: web-build
	go vet -tags=integration ./internal/integration/...

## vet — run go vet static analysis
vet:
	go vet ./...

## bench — run bash comparison benchmark
bench: build
	bash benchmark/run_comparison.sh

## bench-go — run native Go high-throughput benchmark
bench-go:
	go run ./benchmark/runner.go

## cross — cross-compile Linux/macOS/Windows release binaries
#
# `GOOS=linux GOARCH=amd64 go build` is a POSIX env-assignment prefix, and
# cmd.exe chokes on it the same way it choked on `PORT=20130 ./9router-go`.
# Instead the per-target OS/ARCH go through `export`, which is a Make directive
# and therefore identical under sh and cmd. The export is scoped to cross-one
# so an empty GOARCH can never leak into the normal build target.
CROSS_TARGETS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64

ifneq ($(filter cross-one,$(MAKECMDGOALS)),)
export GOOS := $(CROSS_OS)
export GOARCH := $(CROSS_ARCH)
endif

CROSS_BIN := 9router-go

cross-linux-amd64:
	@$(MAKE) --no-print-directory cross-one CROSS_OS=linux CROSS_ARCH=amd64 OUT=$(CROSS_BIN)-linux-amd64

cross-linux-arm64:
	@$(MAKE) --no-print-directory cross-one CROSS_OS=linux CROSS_ARCH=arm64 OUT=$(CROSS_BIN)-linux-arm64

cross-darwin-amd64:
	@$(MAKE) --no-print-directory cross-one CROSS_OS=darwin CROSS_ARCH=amd64 OUT=$(CROSS_BIN)-darwin-amd64

cross-darwin-arm64:
	@$(MAKE) --no-print-directory cross-one CROSS_OS=darwin CROSS_ARCH=arm64 OUT=$(CROSS_BIN)-darwin-arm64

cross-windows-amd64:
	@$(MAKE) --no-print-directory cross-one CROSS_OS=windows CROSS_ARCH=amd64 OUT=$(CROSS_BIN)-windows-amd64.exe

CROSS_BUILD_TARGETS := cross-linux-amd64 cross-linux-arm64 cross-darwin-amd64 cross-darwin-arm64 cross-windows-amd64

cross: web-build
	@$(MAKE) -j $(CROSS_BUILD_TARGETS)
	@ls -lh $(CROSS_BIN)-*
	@(sha256sum $(CROSS_BIN)-* || shasum -a 256 $(CROSS_BIN)-*) > SHA256SUMS.txt
	@cat SHA256SUMS.txt
.PHONY: cross-one $(CROSS_BUILD_TARGETS)
cross-one:
	go build -ldflags="$(LDFLAGS)" -o $(OUT) ./cmd/9router-go/
## mitm-enable — start MITM proxy
mitm-enable: build
	$(BINARY) mitm enable

## mitm-disable — stop MITM proxy
mitm-disable: build
	$(BINARY) mitm disable

## mitm-status — check MITM proxy status
mitm-status: build
	$(BINARY) mitm status

## docker — docker compose up
docker:
	docker compose up -d

## docker-build — build Docker image only
docker-build:
	docker build -t $(BINARY_NAME) .

## clean — remove build artifacts
clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-*
	rm -rf web/dist

## help — show targets
help:
	@echo "9router-go — Makefile targets:"
	@grep -E '^## ' Makefile | sed 's/## /  make /' | sed 's/ — /  /'
	@echo ""
	@echo "Options:"
	@echo "  make run PORT=3000 VERSION=1.1.0"
	@echo "  make run DATA_DIR=/path/to/data  # optional; default is per-platform"
	@echo "  make run CAVEMAN=true PONYTAIL=true AUTO_UPDATE=true"
	@echo "  make run RTK=false"
