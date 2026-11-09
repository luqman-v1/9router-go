#!/usr/bin/env bash
set -euo pipefail

# One-command release. Wraps the manual flow that bump-version.sh only prints:
#
#   ./scripts/release.sh 1.9.16-exp.3     # experimental (GitHub prerelease, docker :exp)
#   ./scripts/release.sh v1.9.16          # stable (also moves version.json / :latest)
#   ./scripts/release.sh --dry-run 1.9.16-exp.3
#   ./scripts/release.sh --yes 1.9.16
#
# Steps: bump-version.sh → changelog-merge (when fragments exist) → commit
# "chore: bump to <ver>" → tag v<ver> → push branch, then the tag. The tag is
# pushed last so release.yml only starts once the commit carrying VERSION is up.
#
# Options:
#   --dry-run  run the preflight checks and print the plan, change nothing
#   --yes      skip the interactive confirmations (non-interactive runs)

DRY_RUN=false
ASSUME_YES=false
VER=""

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=true ;;
    --yes|-y)  ASSUME_YES=true ;;
    -h|--help)
      sed -n '2,16p' "$0"; exit 0 ;;
    *)
      if [ -n "$VER" ]; then
        echo "ERROR: unexpected argument '$arg'" >&2
        exit 1
      fi
      VER="${arg#v}" ;;
  esac
done

if [ -z "$VER" ]; then
  echo "Usage: $0 [--dry-run] [--yes] <version>  e.g. $0 1.9.16-exp.3  |  $0 v1.9.16" >&2
  exit 1
fi

if ! [[ "$VER" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ ]]; then
  echo "Invalid version format: $VER (expected e.g. 1.9.16 or 1.9.16-exp.3)" >&2
  exit 1
fi

TAG="v$VER"
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "ERROR: $ROOT_DIR is not a git repository" >&2
  exit 1
fi

# --- Preflight (read-only) ---------------------------------------------------

if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  echo "ERROR: tag $TAG already exists. Bump to a new version instead." >&2
  exit 1
fi

# Only tracked-file changes block a release: pending .changes/ fragments and
# scratch work are untracked by design, and fragments are consumed here.
if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "ERROR: you have uncommitted changes to tracked files:" >&2
  git status --porcelain | grep -v '^??' >&2 || true
  echo "Commit or stash them first." >&2
  exit 1
fi

BRANCH="$(git branch --show-current)"
IS_PRERELEASE=false
CHANNEL="stable"
if [[ "$VER" == *-* ]]; then
  IS_PRERELEASE=true
  CHANNEL="experimental"
fi

echo "Release plan:"
echo "  version:  $VER ($CHANNEL channel)"
echo "  tag:      $TAG"
echo "  branch:   $BRANCH"
if [ "$IS_PRERELEASE" = true ]; then
  echo "  files:    VERSION, internal/updater/updater.go"
  echo "            (version.json untouched — keeps advertising the newest stable)"
else
  echo "  files:    VERSION, version.json, internal/updater/updater.go"
fi
echo "  changelog: $(compgen -G '.changes/*.md' >/dev/null && echo 'fold pending fragments' || echo 'no pending fragments, skipped')"
echo "  commit:    chore: bump to $VER"
echo "  push:      origin $BRANCH, then $TAG (triggers release.yml)"
echo ""

# A tag pushed from a non-main branch still fires release.yml, and the release
# history has carried experimental tags from integration branches before — so
# this warns rather than blocks, but it must be a deliberate choice.
if [ "$BRANCH" != "main" ] && [ "$ASSUME_YES" = false ] && [ "$DRY_RUN" = false ]; then
  echo "NOTE: you are on '$BRANCH', not 'main'. The tag will be cut from here."
  read -r -p "Continue? [y/N] " REPLY || REPLY=""
  if ! [[ "$REPLY" =~ ^[Yy]$ ]]; then
    echo "Aborted."
    exit 1
  fi
fi

if [ "$DRY_RUN" = true ]; then
  echo "Dry run — nothing was changed."
  exit 0
fi

# --- Execute ------------------------------------------------------------------

# 1. Version bump: VERSION, updater.go fallback, and version.json for stable.
#    bump-version.sh owns the sed/BSD-sed quirks and the prerelease guard.
./scripts/bump-version.sh "$VER"

# 2. Changelog: fold pending fragments under "## [$TAG]". Skipped when there is
#    nothing to fold — release.yml falls back to [Unreleased] or git log.
if compgen -G '.changes/*.md' >/dev/null; then
  make changelog-merge TAG="$TAG"
else
  echo "No pending .changes/ fragments — skipping changelog-merge."
fi

# 3. Commit exactly the release files. -A so cleared fragments stage as deletions.
git add -A -- VERSION version.json internal/updater/updater.go CHANGELOG.md .changes/
git commit -m "chore: bump to $VER"

# 4. Tag.
git tag "$TAG"

# 5. Push — branch first, tag last, so the workflow never sees the tag before
#    the commit that carries the matching VERSION.
if [ "$ASSUME_YES" = false ]; then
  read -r -p "Push $BRANCH and $TAG to origin? [y/N] " REPLY || REPLY=""
  if ! [[ "$REPLY" =~ ^[Yy]$ ]]; then
    echo "Stopped before push. Commit and tag are local:"
    echo "  git push origin $BRANCH && git push origin $TAG"
    exit 0
  fi
fi

git push origin "$BRANCH"
git push origin "$TAG"

echo ""
echo "Done. $TAG pushed — release.yml is running."
echo "  release:  https://github.com/luqman-v1/9router-go/releases/tag/$TAG"
echo "  actions:  https://github.com/luqman-v1/9router-go/actions"
