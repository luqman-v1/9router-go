#!/usr/bin/env bash
# 9router-go single-line installer: detects OS/arch, downloads the latest
# release binary from GitHub, installs it, and prints next steps.
#
#   curl -fsSL https://raw.githubusercontent.com/luqman-v1/9router-go/main/install.sh | bash
#
# Env overrides: VERSION (e.g. v1.9.2, default: latest; "exp" installs the
# newest experimental pre-release), BINDIR (default: /usr/local/bin, falls
# back to ~/.local/bin).
set -euo pipefail

REPO="${REPO:-luqman-v1/9router-go}"
VERSION="${VERSION:-latest}"
BINDIR="${BINDIR:-/usr/local/bin}"

info()  { printf '\033[1;32m[9router-go]\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m[9router-go]\033[0m %s\n' "$*" >&2; }
fatal() { printf '\033[1;31m[9router-go]\033[0m %s\n' "$*" >&2; exit 1; }

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$OS" in
  linux|darwin) ;;
  *) fatal "unsupported OS: $OS (Linux and macOS only; Windows: download the .exe from https://github.com/$REPO/releases/latest)" ;;
esac
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) fatal "unsupported architecture: $ARCH (amd64/arm64 only)" ;;
esac

# VARIANT=noavx installs the baseline x86-64 build for CPUs without AVX/AVX2
# (Celeron/Pentium N-series, older Atom, some VMs). The default asset is built
# the same way today, but the explicit name is what install-noavx.sh points at.
VARIANT="${VARIANT:-}"
case "$VARIANT" in
  "") ;;
  noavx)
    [ "$ARCH" = "amd64" ] || fatal "VARIANT=noavx is only published for amd64"
    ;;
  *) fatal "unsupported VARIANT: $VARIANT (supported: noavx)" ;;
esac

ASSET="9router-go-${OS}-${ARCH}${VARIANT:+-${VARIANT}}"
if [ "$VERSION" = "latest" ]; then
  URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
elif [ "$VERSION" = "exp" ] || [ "$VERSION" = "experimental" ]; then
  # Experimental builds are GitHub pre-releases, which releases/latest skips.
  # The API orders by created_at, not published_at — an old draft published
  # late can come first — so pick the newest by publish date. No jq
  # dependency: expand JSON commas to newlines and scan key/value lines.
  # awk consumes the whole stream (no `exit`), because closing the pipe early
  # trips curl error 23 under `set -o pipefail`. ISO dates sort lexically.
  info "resolving newest experimental pre-release ..."
  TAG="$(curl -fsSL --retry 3 "https://api.github.com/repos/${REPO}/releases?per_page=100" \
    | tr ',' '\n' | awk -F'"' '
        /"tag_name":/        {tag=$4; pre=0}
        /"prerelease": *true/ {pre=1}
        /"published_at":/    {if (pre && $4 > best) {best=$4; besttag=tag}}
        END {print besttag}')"
  [ -n "$TAG" ] || fatal "no experimental pre-release found — check https://github.com/$REPO/releases"
  info "newest experimental release: $TAG"
  URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"
else
  URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
info "downloading $URL ..."
curl -fsSL --retry 3 -o "$TMP/9router-go" "$URL" \
  || fatal "download failed — check https://github.com/$REPO/releases"
chmod +x "$TMP/9router-go"

if [ -w "$BINDIR" ]; then
  mv "$TMP/9router-go" "$BINDIR/9router-go"
else
  warn "$BINDIR not writable, trying sudo (or set BINDIR=\$HOME/.local/bin)"
  sudo mv "$TMP/9router-go" "$BINDIR/9router-go"
fi
trap - EXIT

info "installed to $BINDIR/9router-go"
"$BINDIR/9router-go" version || true
echo
info "start it with:"
echo "       9router-go"
echo "Dashboard: http://localhost:20130 (defaults: port 20130, data ~/.9router)"
