BINARY_SUFFIX := $(if $(findstring Windows_NT,$(OS)),.exe,)
BINARY_NAME := 9router-go$(BINARY_SUFFIX)
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

.PHONY: build run dev version update test test-short test-integration vet vet-integration bench bench-go cross mitm-enable mitm-disable mitm-status docker docker-build clean help web-build web-dev

## web-build — build frontend static assets (Svelte/Vite) into web/dist
#
# Shell-agnostic on purpose: this recipe runs under /bin/sh (Git Bash, CI) or
# cmd.exe (a plain Windows prompt). A POSIX `[ ! -f x ]` guard dies under cmd
# with "! was unexpected at this time." because ! and [ are cmd metacharacters,
# so the whole existence+FORCE check is delegated to Bun, which is already a
# prerequisite of every path through this target.
web-build:
	@bun -e "import {existsSync} from 'fs'; import {spawnSync} from 'child_process'; const force = process.env.FORCE === '1'; if (force || !existsSync('web/dist/index.html')) { console.log('Building web SPA assets...'); spawnSync('bun', ['install','--frozen-lockfile'], {cwd:'web', stdio:'inherit'}); process.exit(spawnSync('bun', ['run','build'], {cwd:'web', stdio:'inherit'}).status ?? 1); }"

## build — compile binary with version embedding
build: web-build
	go build -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) ./cmd/9router-go/

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

## test — run all unit tests
test:
	go test ./... -v

## test-short — run tests (quiet)
test-short:
	go test ./...

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

cross: web-build
	@for pair in $(CROSS_TARGETS); do \
	  os=`echo $$pair | cut -d- -f1`; arch=`echo $$pair | cut -d- -f2`; \
	  out=$(BINARY_NAME)-$$pair; \
	  if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
	  $(MAKE) --no-print-directory cross-one CROSS_OS=$$os CROSS_ARCH=$$arch OUT=$$out || exit 1; \
	done
	@ls -lh $(BINARY_NAME)-*
	@(sha256sum $(BINARY_NAME)-* || shasum -a 256 $(BINARY_NAME)-*) > SHA256SUMS.txt
	@cat SHA256SUMS.txt

.PHONY: cross-one
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
