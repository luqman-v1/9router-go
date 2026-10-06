#!/usr/bin/env bash
set -euo pipefail

# Central version bump — single source: VERSION file
#
# Usage: ./scripts/bump-version.sh 1.8.9            (stable release)
#        ./scripts/bump-version.sh 1.10.0-exp.1     (experimental release)
#
# Updates: VERSION, version.json, internal/updater/updater.go (fallback).
# Dockerfile and Makefile read VERSION at build time, so they need no edit.

if [ $# -ne 1 ]; then
  echo "Usage: $0 <new-version>  e.g. $0 1.8.9  |  $0 1.10.0-exp.1"
  exit 1
fi

NEW_VER="$1"
if ! [[ "$NEW_VER" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ ]]; then
  echo "Invalid version format: $NEW_VER (expected e.g. 1.8.9 or 1.10.0-exp.1)"
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# A prerelease must never reach version.json: that manifest is polled by
# `9router-go update` on the stable channel, so naming an experimental build
# there offers it to every install. The release workflow refuses to run when it
# sees the two out of step; the point here is to keep them in step by default.
IS_PRERELEASE=false
if [[ "$NEW_VER" == *-* ]]; then
  IS_PRERELEASE=true
fi

echo "Bumping version to $NEW_VER ..."

# 1. VERSION file (single source)
echo -n "$NEW_VER" > VERSION
echo "  → VERSION"

# 2. version.json — the stable manifest. Left untouched for an experimental
#    bump, so it keeps advertising the newest stable release.
if [ "$IS_PRERELEASE" = true ]; then
  STABLE="$(grep -o '"latestVersion": *"[^"]*"' version.json | cut -d'"' -f4)"
  echo "  → version.json untouched (experimental build; manifest still advertises $STABLE)"
else
  # sed, not python3. `command -v python3` succeeds on Windows for the
  # Microsoft Store app-execution stub, which then exits non-zero without
  # running anything — and under `set -e` that aborted the bump after VERSION
  # had already been rewritten, leaving the manifest on the old version and the
  # release gate rejecting the tag. This path also has to work under BSD sed
  # (macOS), which rejects `sed -i` without an argument, hence the .bak dance.
  sed -i.bak "s/\"latestVersion\": *\"[^\"]*\"/\"latestVersion\": \"$NEW_VER\"/" version.json
  rm -f version.json.bak
  if ! grep -q "\"latestVersion\": *\"$NEW_VER\"" version.json; then
    echo "ERROR: version.json still does not advertise $NEW_VER:" >&2
    cat version.json >&2
    exit 1
  fi
  echo "  → version.json"
fi

# 3. internal/updater/updater.go fallback
# The updater compares this against the manifest, so an experimental build must
# report its own version here — otherwise it offers itself as an update, which
# is exactly the install you just made by hand.
if grep -q 'var CurrentVersion = "' internal/updater/updater.go; then
  # Use a temp file for BSD sed compatibility
  sed -i.bak "s/var CurrentVersion = \".*\"/var CurrentVersion = \"$NEW_VER\"/" internal/updater/updater.go
  rm -f internal/updater/updater.go.bak
  echo "  → internal/updater/updater.go"
fi

# 4. Dockerfile fallback (optional, now reads VERSION, but keep comment in sync)
if grep -q 'ARG VERSION' Dockerfile; then
  echo "  → Dockerfile (reads VERSION at build, no hardcoded fallback needed)"
fi

echo ""
if [ "$IS_PRERELEASE" = true ]; then
  echo "Done. $NEW_VER is an EXPERIMENTAL build."
  echo "  channel:   experimental (GitHub prerelease, docker :exp)"
  echo "  next:      git add VERSION internal/updater/updater.go"
  echo "             git commit -m \"chore: bump to $NEW_VER\""
  echo "             git tag v$NEW_VER"
  echo ""
  echo "When it graduates, bump the stable version too — that is what moves"
  echo "version.json, :latest and the updater channel:"
  echo "  ./scripts/bump-version.sh ${NEW_VER%%-*}"
else
  echo "Done. Version is now $NEW_VER (single source: VERSION file)"
  echo "Next: git add VERSION version.json internal/updater/updater.go && git commit -m \"chore: bump version to $NEW_VER\" && git tag v$NEW_VER"
fi