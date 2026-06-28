#!/usr/bin/env bash
# fetch-libs.sh — materialize the go-kuzu binding + prebuilt native libkuzu so the
# engine can be built. Idempotent. Used by install.sh and CI.
#
# Result layout (relative to repo root):
#   third_party/go-kuzu/                         writable copy of the go-kuzu module
#   third_party/go-kuzu/lib/dynamic/<plat>/libkuzu.so   native lib (cgo LDFLAGS target)
#   third_party/go-kuzu/kuzu.h                   header matching the native lib
#
# The whole third_party/ tree is gitignored — it is downloaded, never committed.
set -euo pipefail

KUZU_VERSION="${KUZU_VERSION:-0.11.2}"
GOKUZU_VERSION="${GOKUZU_VERSION:-v0.11.2}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEST="$REPO_ROOT/third_party/go-kuzu"

# --- detect platform → kuzu release asset name + cgo lib subdir ----------------
os="$(uname -s)"; arch="$(uname -m)"
case "$os" in
  Linux)  case "$arch" in
            x86_64|amd64)  asset="libkuzu-linux-x86_64.tar.gz";  libdir="linux-amd64" ;;
            aarch64|arm64) asset="libkuzu-linux-aarch64.tar.gz"; libdir="linux-arm64" ;;
            *) echo "fetch-libs: unsupported linux arch: $arch" >&2; exit 1 ;;
          esac ;;
  Darwin) asset="libkuzu-osx-universal.tar.gz"; libdir="darwin" ;;
  *) echo "fetch-libs: unsupported OS: $os" >&2; exit 1 ;;
esac

stamp="$DEST/.fetched-$KUZU_VERSION-$GOKUZU_VERSION-$libdir"
if [ -f "$stamp" ] && [ -f "$DEST/lib/dynamic/$libdir/libkuzu.so" ]; then
  echo "fetch-libs: already present ($KUZU_VERSION / $GOKUZU_VERSION / $libdir)"
  exit 0
fi

command -v go >/dev/null 2>&1 || { echo "fetch-libs: 'go' not found in PATH" >&2; exit 1; }

# --- 1. obtain the go-kuzu module source into a writable copy -------------------
echo "fetch-libs: downloading go-kuzu $GOKUZU_VERSION module source ..."
GOFLAGS="" go mod download "github.com/kuzudb/go-kuzu@$GOKUZU_VERSION"
SRC="$(go env GOMODCACHE)/github.com/kuzudb/go-kuzu@$GOKUZU_VERSION"
[ -d "$SRC" ] || { echo "fetch-libs: module cache missing at $SRC" >&2; exit 1; }

rm -rf "$DEST"
mkdir -p "$(dirname "$DEST")"
cp -r "$SRC" "$DEST"
chmod -R u+w "$DEST"

# --- 2. download prebuilt native lib + matching header -------------------------
url="https://github.com/kuzudb/kuzu/releases/download/v${KUZU_VERSION}/${asset}"
echo "fetch-libs: downloading native lib: $url"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/lib.tar.gz" "$url"
tar -xzf "$tmp/lib.tar.gz" -C "$tmp"

mkdir -p "$DEST/lib/dynamic/$libdir"
cp "$tmp/libkuzu.so" "$DEST/lib/dynamic/$libdir/libkuzu.so" 2>/dev/null \
  || cp "$tmp"/libkuzu.* "$DEST/lib/dynamic/$libdir/" 2>/dev/null
# header from the same release wins, so it matches the ABI of the .so
[ -f "$tmp/kuzu.h" ] && cp "$tmp/kuzu.h" "$DEST/kuzu.h"

touch "$stamp"
echo "fetch-libs: OK → $DEST/lib/dynamic/$libdir/"
