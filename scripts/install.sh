#!/usr/bin/env bash
# install.sh — idempotently make the `kg` engine available in the stable kgai home
# ($KGAI_HOME, default ~/.kgai). Re-runnable on every SessionStart; it short-circuits
# when already up to date, so the cost is paid once per version.
#
# Strategy:
#   1. If a prebuilt release is configured ($KG_RELEASE_BASE), download kg + libkuzu.
#   2. Otherwise build from source (requires `go` and a C compiler).
#   3. Initialize the store on first run.
#
# Prints a short, AI-readable status line to stdout (SessionStart feeds it to Claude).
set -uo pipefail

ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
KGAI_HOME="${KGAI_HOME:-$HOME/.kgai}"
BIN="$KGAI_HOME/bin/kg"
LIBDIR="$KGAI_HOME/lib"
KUZU_VERSION="${KUZU_VERSION:-0.11.2}"
# Prefer prebuilt binaries from the repo's latest GitHub release (no Go/gcc needed). If a
# platform asset is missing (e.g. before the first release), the download fails and we fall
# back to building from source. Override or set empty to force the source build.
KG_RELEASE_BASE="${KG_RELEASE_BASE-https://github.com/kgaidev/kgai/releases/latest/download}"
mkdir -p "$KGAI_HOME/bin" "$LIBDIR"

status() { echo "kgai: $*"; }

# srcver fingerprints the engine source so a plugin update triggers a rebuild.
srcver() {
  { cat "$ROOT/src/go.mod" 2>/dev/null
    find "$ROOT/src" -name '*.go' -type f -exec sha256sum {} + 2>/dev/null | sha256sum
    echo "$KUZU_VERSION"; } | sha256sum | cut -d' ' -f1
}

ensure_store() {
  # The store is per-project (<project>/.kgai/store). Create it once per project.
  local proj cfg
  proj="$(git -C "${CLAUDE_PROJECT_DIR:-$PWD}" rev-parse --show-toplevel 2>/dev/null || echo "${CLAUDE_PROJECT_DIR:-$PWD}")"
  cfg="$proj/.kgai/store/kg.config.json"
  [ -f "$cfg" ] && return 0
  ( cd "$proj" && KGAI_HOME="$KGAI_HOME" LD_LIBRARY_PATH="$LIBDIR:${LD_LIBRARY_PATH:-}" "$BIN" init ) >/dev/null 2>&1 || true
}

report_ready() {
  ensure_store
  echo "$WANT" > "$KGAI_HOME/.srcver"
  # A compact status line, plus a heads-up if there are unresolved conflict branches.
  local extra=""
  local conf
  conf="$(KGAI_HOME="$KGAI_HOME" LD_LIBRARY_PATH="$LIBDIR:${LD_LIBRARY_PATH:-}" "$BIN" conflicts 2>/dev/null | grep -o '"count": *[0-9]*' | grep -o '[0-9]*' || true)"
  if [ -n "$conf" ] && [ "$conf" != "0" ]; then
    extra="⚠ $conf unresolved decision conflict(s) — run /kgai:kg-conflicts. "
  fi
  status "engine ready ($1). ${extra}Use /kgai:kg-ask before non-trivial changes; /kgai:kg-decision to record decisions."
}

WANT="$(srcver)"
HAVE="$(cat "$KGAI_HOME/.srcver" 2>/dev/null || true)"

# Already current → fast path.
if [ -x "$BIN" ] && [ "$WANT" = "$HAVE" ]; then
  ensure_store
  exit 0
fi

# ---- 1. prebuilt release ----------------------------------------------------
if [ -n "${KG_RELEASE_BASE:-}" ]; then
  os="$(uname -s | tr 'A-Z' 'a-z')"; arch="$(uname -m)"
  if [ "$os" = "darwin" ]; then
    # macOS: per-arch binary (arm64 | x86_64) + one universal dylib.
    case "$arch" in x86_64|amd64) arch=x86_64;; aarch64|arm64) arch=arm64;; esac
    lib_asset="libkuzu-darwin-universal.dylib"; lib_file="libkuzu.dylib"
  else
    case "$arch" in x86_64|amd64) arch=x86_64;; aarch64|arm64) arch=aarch64;; esac
    lib_asset="libkuzu-$os-$arch.so"; lib_file="libkuzu.so"
  fi
  if curl -fsSL -o "$KGAI_HOME/bin/kg.new" "$KG_RELEASE_BASE/kg-$os-$arch" 2>/dev/null \
     && curl -fsSL -o "$LIBDIR/$lib_file" "$KG_RELEASE_BASE/$lib_asset" 2>/dev/null; then
    mv "$KGAI_HOME/bin/kg.new" "$BIN"; chmod +x "$BIN"
    report_ready "prebuilt $os-$arch"
    exit 0
  fi
  status "prebuilt download failed, falling back to source build…"
fi

# ---- 2. build from source ---------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
  status "⚠️ ENGINE NOT INSTALLED — kgai will NOT work this session. Prebuilt download failed and the 'go' toolchain is missing for a source build. Fix: install Go (https://go.dev/dl) or check network access to github.com releases, then start a new session."
  exit 0
fi
if ! command -v gcc >/dev/null 2>&1 && ! command -v cc >/dev/null 2>&1; then
  status "⚠️ ENGINE NOT INSTALLED — kgai will NOT work this session. A C compiler (gcc/cc) is required to build the native graph lib. Fix: install Xcode CLT (macOS: xcode-select --install) or gcc, then start a new session."
  exit 0
fi

status "building engine from source (one-time, ~30s)…"
if ! bash "$ROOT/scripts/fetch-libs.sh" >&2; then
  status "⚠️ ENGINE NOT INSTALLED — kgai will NOT work this session. Could not fetch the native graph library (offline? github.com unreachable?). Fix connectivity and start a new session."
  exit 0
fi

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64|Linux/amd64)  libsub="linux-amd64" ;;
  Linux/aarch64|Linux/arm64) libsub="linux-arm64" ;;
  Darwin/*)                  libsub="darwin" ;;
  *) status "⚠️ ENGINE NOT INSTALLED — unsupported platform $(uname -s)/$(uname -m). Linux (x86_64/aarch64) and macOS are supported."; exit 0 ;;
esac

if ( cd "$ROOT/src" && CGO_ENABLED=1 go build \
        -ldflags="-extldflags '-Wl,-rpath,\$ORIGIN/../lib'" \
        -o "$BIN" . ) >&2; then
  cp "$ROOT/third_party/go-kuzu/lib/dynamic/$libsub"/libkuzu.* "$LIBDIR/" 2>/dev/null || true
  report_ready "built from source"
else
  status "⚠️ ENGINE NOT INSTALLED — source build failed (see log above)."
fi
