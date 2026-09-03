#!/usr/bin/env bash
set -euo pipefail

# Central version bump — single source: VERSION file
# Usage: ./scripts/bump-version.sh 1.8.9
# Updates: VERSION, version.json, Dockerfile (fallback), internal/updater/updater.go (fallback)

if [ $# -ne 1 ]; then
  echo "Usage: $0 <new-version>  e.g. $0 1.8.9"
  exit 1
fi

NEW_VER="$1"
if ! [[ "$NEW_VER" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.]+)?$ ]]; then
  echo "Invalid version format: $NEW_VER (expected e.g. 1.8.9)"
  exit 1
fi

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

echo "Bumping version to $NEW_VER ..."

# 1. VERSION file (single source)
echo -n "$NEW_VER" > VERSION
echo "  → VERSION"

# 2. version.json
if command -v python3 >/dev/null 2>&1; then
  python3 -c "
import json, pathlib
p = pathlib.Path('version.json')
data = json.loads(p.read_text())
data['latestVersion'] = '$NEW_VER'
# keep releaseNotes generic, user can edit
p.write_text(json.dumps(data, indent=2) + '\n')
"
else
  # fallback sed
  sed -i.bak "s/\"latestVersion\": *\"[^\"]*\"/\"latestVersion\": \"$NEW_VER\"/" version.json
  rm -f version.json.bak
fi
echo "  → version.json"

# 3. internal/updater/updater.go fallback
# Update the default var CurrentVersion = "x.y.z"
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
echo "Done. Version is now $NEW_VER (single source: VERSION file)"
echo "Next: git add VERSION version.json internal/updater/updater.go && git commit -m \"chore: bump version to $NEW_VER\" && git tag v$NEW_VER"
