#!/usr/bin/env bash
# Install the no-AVX variant of 9router-go: a linux-amd64 build that starts on
# x86-64 CPUs without AVX/AVX2 (Celeron/Pentium N-series, older Atom, some
# VMs). The default asset is built with the same settings today; this name
# exists so the choice is explicit and scriptable.
#
#   curl -fsSL https://raw.githubusercontent.com/luqman-v1/9router-go/main/install-noavx.sh | bash
#
# Env overrides are the ones install.sh accepts: VERSION, BINDIR, REPO.
#
# This is a thin wrapper so the asset name, the sudo fallback and the
# post-install version check live in exactly one place.
set -euo pipefail

REPO="${REPO:-luqman-v1/9router-go}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || true)"

if [ -n "$here" ] && [ -f "$here/install.sh" ]; then
  VARIANT=noavx exec bash "$here/install.sh" "$@"
fi

# Piped straight from curl: there is no sibling install.sh to reuse, so fetch
# the published one. Process substitution keeps it in the same bash process,
# which matters because install.sh relies on bash-only constructs.
VARIANT=noavx exec bash <(curl -fsSL "https://raw.githubusercontent.com/${REPO}/main/install.sh") "$@"
