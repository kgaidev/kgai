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

# BASH_SOURCE is unset when the script is piped in (`curl … | bash`, the by-hand install),
# and `set -u` would abort on it — fall back to $0 so a standalone run still works.
ROOT="${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." 2>/dev/null && pwd)}"
KGAI_HOME="${KGAI_HOME:-$HOME/.kgai}"
BIN="$KGAI_HOME/bin/kg"
LIBDIR="$KGAI_HOME/lib"
# Where `kg` becomes runnable from a plain terminal. The plugin's own bin/ shim is only
# on the PATH Claude Code hands its Bash tool; outside Claude Code nothing resolves.
USER_BIN="${KGAI_USER_BIN:-$HOME/.local/bin}"
KUZU_VERSION="${KUZU_VERSION:-0.11.2}"
PLUGIN_VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$ROOT/.claude-plugin/plugin.json" 2>/dev/null | head -n1)"
PLUGIN_VERSION="${PLUGIN_VERSION:-dev}"
# Prefer prebuilt binaries from the repo's latest GitHub release (no Go/gcc needed). If a
# platform asset is missing (e.g. before the first release), the download fails and we fall
# back to building from source. Override or set empty to force the source build.
KG_RELEASE_BASE="${KG_RELEASE_BASE-https://github.com/kgaidev/kgai/releases/latest/download}"
mkdir -p "$KGAI_HOME/bin" "$LIBDIR"

status() { echo "kgai: $*"; }

# sha256 of stdin. macOS ships shasum, not sha256sum — every hash goes through here so a
# missing GNU coreutils never silently yields an empty digest.
hash_stdin() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1; then openssl dgst -sha256 | awk '{print $NF}'
  fi
}

sha256_of() { hash_stdin < "$1"; }

# verify_asset <file> <asset-name> — checks a downloaded file against the release's
# published <asset-name>.sha256. A missing checksum asset (releases before checksums
# shipped) or a machine without a sha256 tool skips verification with a note; a
# MISMATCH fails hard (corrupted or tampered download).
verify_asset() {
  local file="$1" asset="$2" want have
  want="$(curl -fsSL "$KG_RELEASE_BASE/$asset.sha256" 2>/dev/null | awk '{print $1}')"
  if [ -z "$want" ]; then
    status "no checksum published for $asset — skipping verification"
    return 0
  fi
  have="$(sha256_of "$file")"
  if [ -z "$have" ]; then
    status "no sha256 tool on this machine — skipping verification"
    return 0
  fi
  if [ "$want" != "$have" ]; then
    status "⚠️ checksum MISMATCH for $asset — discarding download"
    return 1
  fi
}

# srcver fingerprints what the engine was installed FROM, so a plugin update reinstalls it.
# The plugin version alone covers released installs (each release ships its own binaries);
# the source hash additionally catches edits in a dev checkout.
#
# The version is always part of it, and the hash falls back to "nohash": an empty
# fingerprint would equal the empty file the previous run wrote, so the "already current"
# check below would match forever and the engine would never update again. That is exactly
# what happened on macOS, where the old sha256sum-only hash produced nothing.
srcver() {
  local h
  h="$( { cat "$ROOT/src/go.mod" 2>/dev/null
          find "$ROOT/src" -name '*.go' -type f 2>/dev/null | LC_ALL=C sort |
            while IFS= read -r f; do cat "$f"; done
        } | hash_stdin )"
  printf '%s|%s|%s\n' "$PLUGIN_VERSION" "$KUZU_VERSION" "${h:-nohash}"
}

project_root() {
  # Must agree with ProjectRoot() in src/internal/store/store.go, or this script looks
  # for the store somewhere the engine never puts it. A LINKED worktree resolves to the
  # main worktree (its common dir is <main>/.git), so all worktrees share one store.
  # A submodule keeps its own root: its common dir is <super>/.git/modules/... and does
  # not end in /.git. --path-format needs git 2.31+; older git just keeps the top level.
  local start top common
  start="${CLAUDE_PROJECT_DIR:-$PWD}"
  top="$(git -C "$start" rev-parse --show-toplevel 2>/dev/null)"
  [ -n "$top" ] || { printf '%s\n' "$start"; return; }
  common="$(git -C "$start" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)"
  case "$common" in
    */.git) printf '%s\n' "${common%/.git}" ;;
    *)      printf '%s\n' "$top" ;;
  esac
}

# ---- make `kg` runnable from any terminal ------------------------------------
# The plugin ships bin/kg, but that shim is only on the PATH Claude Code hands its Bash
# tool. The README and the CLI's own output tell people to run `kg init`, `kg sync`,
# `kg context` in their own terminal, where nothing resolved it — so the engine also gets
# a launcher in the standard per-user bin dir, plus (only when that dir is not on PATH,
# which is the macOS default) one marked line in the shell profile that puts it there.
KGAI_MARK="# added by kgai (Claude Code plugin) — puts the kg CLI on PATH"
PATH_NOTE=""
PATH_RC=""

write_launcher() {
  local dest="$USER_BIN/kg"
  mkdir -p "$USER_BIN" 2>/dev/null || return 1
  # Never clobber a different tool's binary that happens to be called kg. Distinct from
  # a write failure (return 1) so the status line can say which one actually happened.
  if [ -e "$dest" ] && ! grep -q 'kgai launcher' "$dest" 2>/dev/null; then
    return 2
  fi
  # A launcher script, not a symlink: the macOS rpath is @loader_path/../lib, resolved
  # against the path the binary was started from. Through a symlink in ~/.local/bin that
  # would look for the native lib in ~/.local/lib and fail to load.
  cat > "$dest.new" <<'LAUNCHER'
#!/usr/bin/env bash
# kgai launcher — installed by the kgai Claude Code plugin (scripts/install.sh).
# Runs the engine from its stable home ($KGAI_HOME, default ~/.kgai) so `kg` works in any
# terminal, not just inside Claude Code. Safe to delete — the plugin writes it again at
# the next session start.
set -euo pipefail
KGAI_HOME="${KGAI_HOME:-$HOME/.kgai}"
BIN="$KGAI_HOME/bin/kg"
if [ ! -x "$BIN" ]; then
  echo "kg: engine not installed at $BIN. Start a Claude Code session with the kgai plugin enabled, or install by hand: https://github.com/kgaidev/kgai#install-the-cli-by-hand" >&2
  exit 127
fi
export LD_LIBRARY_PATH="$KGAI_HOME/lib:${LD_LIBRARY_PATH:-}"
export DYLD_LIBRARY_PATH="$KGAI_HOME/lib:${DYLD_LIBRARY_PATH:-}"
exec "$BIN" "$@"
LAUNCHER
  [ -f "$dest.new" ] || return 1
  if [ -f "$dest" ] && cmp -s "$dest.new" "$dest"; then rm -f "$dest.new"; return 0; fi
  chmod +x "$dest.new" && mv "$dest.new" "$dest"
}

# The shell that OWNS the user's terminals. $SHELL is merely whatever spawned this hook:
# Claude Code can be started from a bash session, from a GUI app, or from a launcher that
# sets it to something else entirely, so on a Mac whose Terminal windows are zsh it often
# reads /bin/bash — and the PATH line then lands in .bash_profile, which zsh never reads.
# The passwd entry is what Terminal actually opens, so it wins where it can be read.
login_shell() {
  local s=""
  case "$(uname -s)" in
    # Directory Service, not /etc/passwd: a normal macOS account has no passwd entry.
    Darwin) s="$(dscl . -read "/Users/$(id -un)" UserShell 2>/dev/null | awk 'NR==1{print $2}')" ;;
    *)      s="$(getent passwd "$(id -un)" 2>/dev/null | cut -d: -f7)" ;;
  esac
  [ -n "$s" ] || s="$(awk -F: -v u="$(id -un)" '$1==u{print $7}' /etc/passwd 2>/dev/null | head -n1)"
  printf '%s\n' "${s:-${SHELL:-sh}}"
}

shell_family() {
  case "$(basename "$(login_shell)")" in
    zsh) echo zsh ;; bash) echo bash ;; fish) echo fish ;; *) echo sh ;;
  esac
}

# Profile files a fresh terminal of that shell reads. Order is "where we would add it"
# first. A macOS Terminal tab is a LOGIN shell (bash reads .bash_profile, not .bashrc);
# a Linux terminal window is not (bash reads .bashrc).
rc_files() {
  case "$(shell_family)" in
    zsh)  printf '%s\n' "$HOME/.zshrc" "$HOME/.zprofile" "$HOME/.zshenv" "$HOME/.zlogin" ;;
    bash) if [ "$(uname -s)" = "Darwin" ]
          then printf '%s\n' "$HOME/.bash_profile" "$HOME/.bashrc" "$HOME/.bash_login" "$HOME/.profile"
          else printf '%s\n' "$HOME/.bashrc" "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; fi ;;
    fish) printf '%s\n' "$HOME/.config/fish/config.fish" ;;
    *)    printf '%s\n' "$HOME/.profile" ;;
  esac
}

# Will a NEW terminal find $USER_BIN? This process's own PATH cannot answer that: Claude
# Code hands hooks an environment of its own making — on macOS a GUI-launched app inherits
# launchd's PATH, and the CLI's installer puts `claude` in ~/.local/bin and exports it for
# its children. Either way $USER_BIN can be on PATH here while the user's Terminal has
# never heard of it, and the old check then skipped the profile line — the exact reason
# `kg` stayed command-not-found on a Mac. So ask the files that build a terminal's PATH:
# the system-wide configuration plus the login shell's own profiles.
path_covered() {
  local rel="${USER_BIN#"$HOME"/}" f
  # Read the candidates instead of splitting them on whitespace — a home directory with a
  # space in it is legal on macOS.
  while IFS= read -r f; do
    [ -f "$f" ] || continue
    grep -qF "$USER_BIN" "$f" 2>/dev/null && return 0
    [ "$rel" = "$USER_BIN" ] && continue   # $USER_BIN lives outside $HOME
    # People write it unexpanded far more often than not.
    grep -qF "\$HOME/$rel" "$f" 2>/dev/null && return 0
    grep -qF "~/$rel" "$f" 2>/dev/null && return 0
  done < <(printf '%s\n' /etc/paths /etc/paths.d/* /etc/profile /etc/zprofile \
                         /etc/zshrc /etc/profile.d/* /etc/environment; rc_files)
  return 1
}

# Appends one marked PATH line to the file the user's login shell reads. Returns 0 only
# when it actually added it, so the status line mentions it exactly once.
ensure_path_entry() {
  path_covered && return 1
  local rc line
  rc="$(rc_files | head -n1)"
  case "$(shell_family)" in
    fish) line="set -gx PATH $USER_BIN \$PATH" ;;
    *)    line="export PATH=\"$USER_BIN:\$PATH\"" ;;
  esac
  mkdir -p "$(dirname "$rc")" 2>/dev/null || return 1
  printf '\n%s\n%s\n' "$KGAI_MARK" "$line" >> "$rc" 2>/dev/null || return 1
  PATH_RC="$rc"
  return 0
}

ensure_on_path() {
  PATH_NOTE=""
  write_launcher; local w=$?
  if [ "$w" = 2 ]; then
    PATH_NOTE="note: kept the existing \`kg\` in $USER_BIN (not ours) — the plugin's engine is at $BIN. "
    return 0
  elif [ "$w" != 0 ]; then
    PATH_NOTE="note: could not write $USER_BIN/kg (no permission?) — inside Claude Code \`kg\` still works; in your own terminal run $BIN. "
    return 0
  fi
  if ensure_path_entry; then
    PATH_NOTE="\`kg\` is now on your PATH via $USER_BIN (added to $PATH_RC) — open a new terminal to use it there. "
  fi
}

ensure_store() {
  # Create the store once, if the engine says there isn't one. ASK the engine rather
  # than testing <project>/.kgai/store: the store may be configured elsewhere entirely
  # (the `store` setting, KGAI_STORE — several repos sharing one graph), and a path
  # guessed here would re-init on every session and miss the shared store completely.
  local proj
  proj="$(project_root)"
  ( cd "$proj" && KGAI_HOME="$KGAI_HOME" \
      LD_LIBRARY_PATH="$LIBDIR:${LD_LIBRARY_PATH:-}" \
      DYLD_LIBRARY_PATH="$LIBDIR:${DYLD_LIBRARY_PATH:-}" "$BIN" status 2>/dev/null |
      grep -q '"initialized": *false' ) || return 0
  ( cd "$proj" && KGAI_HOME="$KGAI_HOME" \
      LD_LIBRARY_PATH="$LIBDIR:${LD_LIBRARY_PATH:-}" \
      DYLD_LIBRARY_PATH="$LIBDIR:${DYLD_LIBRARY_PATH:-}" "$BIN" init ) >/dev/null 2>&1 || true
}

# An installed file is not a working engine. `kg version` is the cheapest command that
# still loads the native graph library, so it proves the whole chain: right architecture,
# dylib found next to the binary, allowed to run by the OS. Without this the installer
# announced "engine ready" for a binary that died on its first call, and every kg command
# for the rest of the session failed silently behind `|| true`.
ENGINE_ERR=""
engine_works() {
  local out rc
  out="$(KGAI_HOME="$KGAI_HOME" LD_LIBRARY_PATH="$LIBDIR:${LD_LIBRARY_PATH:-}" \
           DYLD_LIBRARY_PATH="$LIBDIR:${DYLD_LIBRARY_PATH:-}" "$BIN" version 2>&1)"
  rc=$?
  if [ "$rc" = 0 ]; then ENGINE_ERR=""; return 0; fi
  # One line, first non-empty: dyld and the loader are verbose, the status line is not.
  # A signal kill (9/SIGKILL → 137) prints nothing we can capture; on macOS that is
  # almost always the OS refusing a binary whose signature it does not accept.
  ENGINE_ERR="$(printf '%s' "$out" | grep -v '^[[:space:]]*$' | head -n1)"
  [ -n "$ENGINE_ERR" ] || ENGINE_ERR="exited with code $rc"
  return 1
}

report_ready() {
  ensure_on_path
  ensure_store
  echo "$WANT" > "$KGAI_HOME/.srcver"
  # A compact status line, plus a heads-up if there are unresolved conflict branches.
  local extra="$PATH_NOTE"
  local conf
  conf="$(KGAI_HOME="$KGAI_HOME" LD_LIBRARY_PATH="$LIBDIR:${LD_LIBRARY_PATH:-}" "$BIN" conflicts 2>/dev/null | grep -o '"count": *[0-9]*' | grep -o '[0-9]*' || true)"
  if [ -n "$conf" ] && [ "$conf" != "0" ]; then
    extra="${extra}⚠ $conf unresolved decision conflict(s) — run /kgai:kg-conflicts. "
  fi
  # Background auto-sync runs silently; a persistent failure surfaces here, once
  # per session, instead of nagging on every turn. Soft failures (expired SSO,
  # offline) report ok:true with a detail, so check for either.
  local lastsync
  lastsync="$(project_root)/.kgai/store/last-autosync.json"
  if [ -f "$lastsync" ] && grep -qE '"ok": *false|"detail":' "$lastsync" 2>/dev/null; then
    extra="${extra}⚠ background team sync did not sync on its last attempt — tell the user to run \`kg sync\` to see why. "
  fi
  status "engine ready ($1). ${extra}Use /kgai:kg-ask before non-trivial changes; /kgai:kg-decision to record decisions."
}

WANT="$(srcver)"
HAVE="$(cat "$KGAI_HOME/.srcver" 2>/dev/null || true)"

# Already current → fast path. The launcher check runs here too (a few stat calls): the
# engine can be up to date while the user's PATH entry is missing — deleted, new machine,
# or installed before the launcher shipped.
if [ -x "$BIN" ] && [ "$WANT" = "$HAVE" ]; then
  # An engine that stopped running (deleted dylib, OS upgrade, moved home) would otherwise
  # keep passing this check forever while every command silently failed. Say so, loudly,
  # and point at the one-line repair — reinstalling on its own would re-download the same
  # bytes that already do not run here.
  if ! engine_works; then
    status "⚠️ ENGINE INSTALLED BUT NOT RUNNING — kgai will NOT work this session ($ENGINE_ERR). Fix: rm -rf \"$KGAI_HOME\" and start a new session to reinstall it."
    exit 0
  fi
  ensure_on_path
  ensure_store
  [ -n "$PATH_NOTE" ] && status "$PATH_NOTE"
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
     && curl -fsSL -o "$LIBDIR/$lib_file.new" "$KG_RELEASE_BASE/$lib_asset" 2>/dev/null \
     && verify_asset "$KGAI_HOME/bin/kg.new" "kg-$os-$arch" \
     && verify_asset "$LIBDIR/$lib_file.new" "$lib_asset"; then
    mv "$KGAI_HOME/bin/kg.new" "$BIN"; chmod +x "$BIN"
    mv "$LIBDIR/$lib_file.new" "$LIBDIR/$lib_file"
    if engine_works; then
      report_ready "prebuilt $os-$arch"
      exit 0
    fi
    # Downloaded fine, checksum matched, still will not run here: a source build is the
    # one remaining self-heal (it produces a binary for THIS machine), so try it.
    status "prebuilt $os-$arch does not run on this machine ($ENGINE_ERR) — trying a source build…"
  else
    rm -f "$KGAI_HOME/bin/kg.new" "$LIBDIR/$lib_file.new"
    status "prebuilt download failed, falling back to source build…"
  fi
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
  Linux/x86_64|Linux/amd64)  libsub="linux-amd64"; rpath='$ORIGIN/../lib' ;;
  Linux/aarch64|Linux/arm64) libsub="linux-arm64"; rpath='$ORIGIN/../lib' ;;
  # dyld has no $ORIGIN — it spells the binary's own directory @loader_path. Building a
  # macOS binary with $ORIGIN yields an engine that cannot load libkuzu.dylib at all.
  Darwin/*)                  libsub="darwin";      rpath='@loader_path/../lib' ;;
  *) status "⚠️ ENGINE NOT INSTALLED — unsupported platform $(uname -s)/$(uname -m). Linux (x86_64/aarch64) and macOS are supported."; exit 0 ;;
esac

if ( cd "$ROOT/src" && CGO_ENABLED=1 go build \
        -ldflags="-X main.version=$PLUGIN_VERSION -extldflags '-Wl,-rpath,$rpath'" \
        -o "$BIN" . ) >&2; then
  cp "$ROOT/third_party/go-kuzu/lib/dynamic/$libsub"/libkuzu.* "$LIBDIR/" 2>/dev/null || true
  if engine_works; then
    report_ready "built from source"
  else
    status "⚠️ ENGINE NOT INSTALLED — it built, but does not run here ($ENGINE_ERR). kgai will NOT work this session."
  fi
else
  status "⚠️ ENGINE NOT INSTALLED — source build failed (see log above)."
fi
