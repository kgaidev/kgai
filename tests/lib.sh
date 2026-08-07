#!/usr/bin/env bash
# tests/lib.sh — shared harness for the installer test suites.
#
# Two ways to drive scripts/install.sh:
#   load()            — source it with KGAI_INSTALL_LIB=1 and call its functions directly.
#                       Fast, and the only way to reach the branches that depend on the
#                       platform or the login shell.
#   run_installer()   — run it as a script, the way the SessionStart hook does, in a
#                       scrubbed environment against a fake release served over file://.
#                       Proves the whole thing installs, not just that its pieces work.
#
# Nothing here touches the network, the real $HOME, or the real /etc. Every path the
# installer can write to lives under a per-test sandbox.
#
# Written for bash 3.2: no associative arrays, no mapfile, no ${x^^}. macOS ships 3.2 and
# that is the platform most of these bugs came from.

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASH_BIN="${BASH:-$(command -v bash)}"
VERBOSE=0
[ "${1:-}" = "-v" ] && VERBOSE=1

# fake_shell below speaks the probe's @KGPB@/@KGPE@ sentinels as literals. If install.sh
# ever changed that spelling, every probe test would silently start proving nothing — so
# drift fails the whole suite loudly, before a single test runs.
grep -q '@KGPB@' "$REPO/scripts/install.sh" && grep -q '@KGPE@' "$REPO/scripts/install.sh" ||
  { echo "tests/lib.sh: install.sh no longer speaks the @KGPB@/@KGPE@ sentinels fake_shell answers with" >&2; exit 1; }

# Strip a trailing slash off TMPDIR before composing the path: macOS sets TMPDIR to
# `/var/folders/.../T/` (with the slash), so `${TMPDIR}/kgai-tests` would contain `//`.
# A sandbox path with `//` then fails to equal what `cd … && pwd` reports (the kernel
# collapses the `//`), which is exactly what made the project-root assertions fail on the
# macOS CI runner while passing on Linux.
_TMPBASE="${TMPDIR:-/tmp}"; _TMPBASE="${_TMPBASE%/}"
TMPROOT="$(mktemp -d "$_TMPBASE/kgai-tests.XXXXXX")"
trap 'rm -rf "$TMPROOT"' EXIT

T=0; PASSED=0; FAILED=0; SKIPPED=0
red()    { printf '\033[31m%s\033[0m' "$1"; }
green()  { printf '\033[32m%s\033[0m' "$1"; }
yellow() { printf '\033[33m%s\033[0m' "$1"; }

# Two things a test can need that the host may not provide. Checked once, up front; the
# tests that depend on them report as skipped rather than as failures. Git Bash cannot
# make real symlinks without Developer Mode, and a container running as root walks
# straight through a chmod 555.
CAN_SYMLINK=no
( cd "$TMPROOT" && : > target && ln -s target link 2>/dev/null && [ -L link ] ) && CAN_SYMLINK=yes
rm -f "$TMPROOT/target" "$TMPROOT/link"
CAN_DENY_WRITE=no
mkdir -p "$TMPROOT/ro" && chmod 555 "$TMPROOT/ro" 2>/dev/null
( : > "$TMPROOT/ro/probe" ) 2>/dev/null || CAN_DENY_WRITE=yes
chmod 755 "$TMPROOT/ro" 2>/dev/null; rm -rf "$TMPROOT/ro"

# ---- assertions (run inside a test's subshell; failures land in $FAILFILE) -------------

_fail() { printf '%s\n' "$*" >> "$FAILFILE"; }
# Not applicable on this host. Recorded separately so a skip can never read as a pass.
skip()  { printf '%s\n' "$*" > "$SKIPFILE"; }

need_symlinks()   { [ "$CAN_SYMLINK" = yes ]    || { skip "no real symlinks on this filesystem"; return 1; }; }
need_write_deny() { [ "$CAN_DENY_WRITE" = yes ] || { skip "chmod does not deny writes here (root?)"; return 1; }; }

assert_eq()    { [ "$2" = "$3" ] || _fail "$1: expected [$3], got [$2]"; }
assert_ne()    { [ "$2" != "$3" ] || _fail "$1: expected anything but [$3]"; }
assert_rc()    { [ "$2" = "$3" ] || _fail "$1: expected exit $3, got $2"; }
assert_true()  { if [ "$2" != 0 ]; then _fail "$1: expected true, was false"; fi; }
assert_false() { if [ "$2" = 0 ];  then _fail "$1: expected false, was true"; fi; }
assert_has()   { case "$2" in *"$3"*) ;; *) _fail "$1: [$2] does not contain [$3]" ;; esac; }
assert_hasnt() { case "$2" in *"$3"*) _fail "$1: [$2] unexpectedly contains [$3]" ;; esac; }

assert_file_has() { # desc file needle
  if [ ! -f "$2" ]; then _fail "$1: $2 does not exist"; return; fi
  grep -qF -- "$3" "$2" || _fail "$1: $2 does not contain [$3]"
}
assert_file_hasnt() { # desc file needle
  [ -f "$2" ] || return 0
  grep -qF -- "$3" "$2" && _fail "$1: $2 unexpectedly contains [$3]"
  return 0
}
assert_exists() { [ -e "$2" ] || [ -L "$2" ] || _fail "$1: $2 does not exist"; }
assert_absent() { if [ -e "$2" ] || [ -L "$2" ]; then _fail "$1: $2 should not exist"; fi; }

# No file matching a glob — for temp files whose names carry a pid.
assert_no_match() { # desc glob
  local f
  for f in $2; do
    [ -e "$f" ] && _fail "$1: $f should not exist"
  done
  return 0
}

assert_count() { # desc file needle n
  # grep -c prints 0 AND exits 1 when it matches nothing, so `|| echo 0` would append a
  # second line rather than supply a default.
  local n=0
  [ -f "$2" ] && n="$(grep -cF -- "$3" "$2" 2>/dev/null | head -n1)"
  [ "${n:-0}" = "$4" ] || _fail "$1: expected $4 occurrence(s) of [$3] in $2, found ${n:-0}"
}

assert_files_identical() { # desc file-a file-b
  cmp -s "$2" "$3" || _fail "$1: $2 and $3 differ"
}

# The safety invariant for a profile the installer appends to: whatever was there before is
# still there, byte for byte, at the front of the file. Nothing rewritten, nothing dropped.
assert_prefix_preserved() { # desc original-copy file
  local n
  n="$(wc -c < "$2" | tr -d ' ')"
  if [ ! -f "$3" ]; then _fail "$1: $3 does not exist"; return; fi
  head -c "$n" "$3" > "$SB/.prefix.$$" 2>/dev/null
  cmp -s "$2" "$SB/.prefix.$$" || _fail "$1: the original bytes of $3 were modified"
  rm -f "$SB/.prefix.$$"
}

assert_mode() { # desc file expected-octal
  local m
  m="$(ls -l "$2" 2>/dev/null | cut -c1-10)"
  [ -n "$m" ] || { _fail "$1: $2 does not exist"; return; }
  # Compare the rwx string rather than a stat format string: BSD and GNU stat disagree on
  # every flag, ls does not.
  [ "$m" = "$3" ] || _fail "$1: mode of $2 is $m, expected $3"
}

# ---- sandbox --------------------------------------------------------------------------

# A fresh $HOME, a fresh $KGAI_HOME, and a stand-in engine. A suite that measures whether
# anything got INSTALLED overrides sandbox() to drop that stand-in.
lib_sandbox() {
  SB="$TMPROOT/sb$T"
  rm -rf "$SB"; mkdir -p "$SB/etc" "$SB/.kgai/bin" "$SB/tmp" "$SB/proj"
  export HOME="$SB"
  # Pinned into the sandbox: the probe and the engine wrapper mktemp scratch files into
  # ${TMPDIR:-/tmp}, and load-mode tests used to drop those in the developer's real one.
  export TMPDIR="$SB/tmp"
  export KGAI_HOME="$SB/.kgai"
  export KGAI_USER_BIN="$SB/.local/bin"
  export CLAUDE_PLUGIN_ROOT="$REPO"
  export KGAI_INSTALL_LIB=1
  # Pinned, not inherited. Otherwise the developer's own login shell decides which profile
  # rc_files looks at, and a suite that is green on a bash machine fails on a zsh one for
  # reasons that have nothing to do with the code under test.
  export KGAI_LOGIN_SHELL=/bin/bash
  printf 'fake engine\n' > "$SB/.kgai/bin/kg"
  chmod +x "$SB/.kgai/bin/kg"
  FAKEBIN=""
}

sandbox() { lib_sandbox; }

# Source the installer as a library, then cut its last tie to the host: system_path_files
# points at the sandbox, so a /etc/paths.d entry on the developer's machine cannot decide
# a test.
load() {
  # shellcheck source=../scripts/install.sh
  . "$REPO/scripts/install.sh"
  system_path_files() { printf '%s\n' "$HOME/etc/paths" "$HOME/etc/profile"; }
}

# A shell that reports whatever PATH the test wants, so the probe's three answers
# (reachable / not reachable / cannot tell) are all reproducible. Named `bash` so
# shell_family classifies it as bash.
fake_shell() { # $1 = PATH it reports, or NOISE-ONLY to answer nothing usable
  mkdir -p "$SB/fakebin"
  if [ "$1" = "NOISE-ONLY" ]; then
    printf '#!/bin/sh\necho "no sentinels here"\n' > "$SB/fakebin/bash"
  else
    printf '#!/bin/sh\nprintf "%%s" "welcome to your shell"\nprintf "%%s" "@KGPB@%s@KGPE@"\n' \
      "$1" > "$SB/fakebin/bash"
  fi
  chmod +x "$SB/fakebin/bash"
  export KGAI_LOGIN_SHELL="$SB/fakebin/bash"
}

# The real bash, so a test can check what an actual terminal resolves.
real_bash() { export KGAI_LOGIN_SHELL="$BASH_BIN"; }

# Does a terminal on THIS host open a login shell? One mirror of install.sh's
# terminal_is_login (kept in a single place here — this list used to be pasted into every
# suite, and adding a platform meant five coordinated edits). The flow suites run the real
# script against the real `uname`, so they must expect the same answer the code computes.
host_terminal_is_login() {
  case "$(uname -s)" in Darwin|MINGW*|MSYS*|CYGWIN*) return 0 ;; *) return 1 ;; esac
}

# Flags that start $BASH_BIN the way this platform's terminal would.
host_shell_flags() { if host_terminal_is_login; then echo "-lic"; else echo "-ic"; fi; }

# The profile file install.sh writes to on THIS host, for a fresh sandbox whose login shell
# is bash. A flow test must expect the same file the code picks: .bashrc on Linux (a
# terminal there is not a login shell), .bash_profile where the terminal opens a login
# shell (macOS; Git Bash back when a windows job existed). Hard-coding .bashrc made the
# flow suite fail on the macos-14 CI runner — a platform this release targets.
expected_profile() {
  if host_terminal_is_login
  then printf '%s\n' "$SB/.bash_profile"
  else printf '%s\n' "$SB/.bashrc"
  fi
}

# ---- driving the installer as a script ------------------------------------------------

# Shadow `uname` on the PATH the installer gets, so the platform branches — asset names,
# rpath flavour, the Windows refusal — can be exercised from any machine.
fake_uname() { # $1 = kernel name (uname -s), $2 = machine (uname -m)
  mkdir -p "$SB/fakebin"
  printf '#!/bin/sh\ncase "$1" in\n  -m) echo "%s" ;;\n  *)  echo "%s" ;;\nesac\n' \
    "$2" "$1" > "$SB/fakebin/uname"
  chmod +x "$SB/fakebin/uname"
  FAKEBIN="$SB/fakebin"
}

# A release directory the installer can download from with curl file://. Assets are named
# exactly as install.sh expects for the given platform, and each gets its real sha256
# unless the caller asks for a wrong or missing one.
#
#   make_release <os> <arch> [checksum-mode]
#       checksum-mode: good (default) | bad | none
#   The engine it installs is a shell script that answers `version`, `status`, `conflicts`
#   and `config`, and records every invocation in $SB/engine.log.
make_release() {
  local os="$1" arch="$2" mode="${3:-good}" rel="$SB/release" libasset libfile
  mkdir -p "$rel"
  if [ "$os" = "darwin" ]; then
    libasset="libkuzu-darwin-universal.dylib"; libfile="libkuzu.dylib"
  else
    libasset="libkuzu-$os-$arch.so"; libfile="libkuzu.so"
  fi
  RELEASE_LIB_FILE="$libfile"
  cat > "$rel/kg-$os-$arch" <<ENGINE
#!/bin/sh
echo "\$1 cwd=\$PWD" >> "$SB/engine.log"
case "\$1" in
  version)   echo '{"ok":true,"version":"9.9.9"}' ;;
  status)    echo "{\"initialized\": \${KGTEST_INITIALIZED:-true}}" ;;
  conflicts) echo "{\"count\": \${KGTEST_CONFLICTS:-0}}" ;;
  # Shaped like the real engine's, not merely keyed like it. \`kg config\` pretty-prints
  # with sorted keys (encoding/json + MarshalIndent), so the output is MULTI-LINE and the
  # per-layer \"pending_approval\": true lands EARLIER in the stream than the top-level
  # \"pending_approval\": \"<path>\" — \"layers\" sorts before \"pending_approval\". Picking
  # the quoted one out of that stream is exactly what install.sh's pattern is for, so a
  # single-line fake carrying only the string tested the key and not the guard.
  config)    p="\${KGTEST_PENDING:-}"; d="\${KGTEST_DISMISSED:-}"
             printf '{\n'
             [ -n "\$d" ] && printf '  "dismissed": "%s",\n' "\$d"
             printf '  "layers": [\n    {\n      "layer": "project",\n'
             printf '      "path": "%s",\n      "exists": %s,\n' "\$p\$d" \
                    "\$( [ -n "\$p\$d" ] && echo true || echo false )"
             [ -n "\$p" ] && printf '      "pending_approval": true,\n'
             [ -n "\$d" ] && printf '      "dismissed": true,\n'
             printf '      "settings": {}\n    }\n  ],\n  "ok": true,\n'
             [ -n "\$p" ] && printf '  "pending_approval": "%s",\n' "\$p"
             printf '  "store_root": "%s/.kgai/store"\n}\n' "\$PWD" ;;
  init)      mkdir -p "\$PWD/.kgai/store" ;;
  *)         echo '{}' ;;
esac
ENGINE
  chmod +x "$rel/kg-$os-$arch"
  printf 'not a real dylib\n' > "$rel/$libasset"
  case "$mode" in
    good) _sum "$rel/kg-$os-$arch" > "$rel/kg-$os-$arch.sha256"
          _sum "$rel/$libasset"    > "$rel/$libasset.sha256" ;;
    bad)  printf '%s  x\n' "0000000000000000000000000000000000000000000000000000000000000000" \
            > "$rel/kg-$os-$arch.sha256"
          _sum "$rel/$libasset" > "$rel/$libasset.sha256" ;;
    none) ;;
  esac
  RELEASE_URL="file://$rel"
}

_sum() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"
  else shasum -a 256 "$1"; fi
}

# A plugin root the test controls: install.sh reads only .claude-plugin/plugin.json from
# it, plus src/ and scripts/fetch-libs.sh for a source build. Leaving those out makes the
# source-build fallback fail immediately instead of spending a minute compiling Go on a
# machine that happens to have a toolchain.
make_plugin_root() { # $1 = version string
  mkdir -p "$SB/plugin/.claude-plugin"
  printf '{"name":"kgai","version":"%s"}\n' "$1" > "$SB/plugin/.claude-plugin/plugin.json"
  PLUGIN_ROOT="$SB/plugin"
}

# Run install.sh as the hook does. Extra K=V assignments are passed as arguments and win
# over the defaults. Sets OUT (stdout), ERR (stderr) and RC.
run_installer() {
  mkdir -p "$SB/tmp" "$SB/proj"
  [ -n "${PLUGIN_ROOT:-}" ] || make_plugin_root "9.9.9"
  OUT="$(env -i \
    "HOME=$SB" \
    "PATH=${FAKEBIN:+$FAKEBIN:}/usr/local/bin:/usr/bin:/bin" \
    "TMPDIR=$SB/tmp" \
    "KGAI_HOME=$SB/.kgai" \
    "KGAI_USER_BIN=$SB/.local/bin" \
    "KGAI_LOGIN_SHELL=$BASH_BIN" \
    "KGAI_SYSTEM_PATH_FILES=$SB/etc/paths" \
    "CLAUDE_PLUGIN_ROOT=$PLUGIN_ROOT" \
    "CLAUDE_PROJECT_DIR=$SB/proj" \
    "KG_RELEASE_BASE=${RELEASE_URL:-}" \
    "$@" \
    "$BASH_BIN" "$REPO/scripts/install.sh" 2>"$SB/installer.err")"
  RC=$?
  ERR="$(cat "$SB/installer.err" 2>/dev/null)"
}

# Same, but piped in on stdin — the `curl … | bash` install documented in the README,
# where BASH_SOURCE is unset and `set -u` used to be the thing that broke.
run_installer_piped() {
  mkdir -p "$SB/tmp" "$SB/proj"
  [ -n "${PLUGIN_ROOT:-}" ] || make_plugin_root "9.9.9"
  OUT="$(env -i \
    "HOME=$SB" \
    "PATH=${FAKEBIN:+$FAKEBIN:}/usr/local/bin:/usr/bin:/bin" \
    "TMPDIR=$SB/tmp" \
    "KGAI_HOME=$SB/.kgai" \
    "KGAI_USER_BIN=$SB/.local/bin" \
    "KGAI_LOGIN_SHELL=$BASH_BIN" \
    "KGAI_SYSTEM_PATH_FILES=$SB/etc/paths" \
    "CLAUDE_PLUGIN_ROOT=$PLUGIN_ROOT" \
    "CLAUDE_PROJECT_DIR=$SB/proj" \
    "KG_RELEASE_BASE=${RELEASE_URL:-}" \
    "$@" \
    "$BASH_BIN" -c "$BASH_BIN < '$REPO/scripts/install.sh'" 2>"$SB/installer.err")"
  RC=$?
  ERR="$(cat "$SB/installer.err" 2>/dev/null)"
}

# ---- runner ---------------------------------------------------------------------------

run() { # $1 = name, $2 = function
  T=$((T + 1))
  FAILFILE="$TMPROOT/fail.$T"; : > "$FAILFILE"
  SKIPFILE="$TMPROOT/skip.$T"; rm -f "$SKIPFILE"
  local out="$TMPROOT/out.$T" done="$TMPROOT/done.$T"
  ( sandbox; "$2"; : > "$done" ) >"$out" 2>&1
  # A body that died halfway would otherwise leave an empty failure file and read green.
  [ -f "$done" ] || _fail "test body did not finish (crashed?)"
  if [ -s "$FAILFILE" ]; then
    FAILED=$((FAILED + 1)); printf '  %s %s\n' "$(red FAIL)" "$1"
    sed 's/^/         /' "$FAILFILE"
    [ -s "$out" ] && sed 's/^/         | /' "$out"
  elif [ -f "$SKIPFILE" ]; then
    SKIPPED=$((SKIPPED + 1))
    printf '  %s %s — %s\n' "$(yellow skip)" "$1" "$(cat "$SKIPFILE")"
  else
    PASSED=$((PASSED + 1))
    printf '  %s   %s\n' "$(green ok)" "$1"
    [ "$VERBOSE" = 1 ] && [ -s "$out" ] && sed 's/^/         | /' "$out"
  fi
  return 0
}

section() { printf '\n%s\n' "$1"; }

suite_header() {
  printf '%s — %s, bash %s\n' "$1" "$(uname -s)" "${BASH_VERSION%%(*}"
}

summary() {
  if [ "$SKIPPED" = 0 ]
  then printf '\n%s passed, %s failed, %s total\n' "$PASSED" "$FAILED" "$T"
  else printf '\n%s passed, %s failed, %s skipped, %s total\n' "$PASSED" "$FAILED" "$SKIPPED" "$T"
  fi
  [ "$FAILED" = 0 ] || return 1
}
