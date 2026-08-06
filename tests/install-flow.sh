#!/usr/bin/env bash
# tests/install-flow.sh — does it actually install, first try?
#
# The other two suites call the installer's functions. This one runs the whole script the
# way the SessionStart hook does — scrubbed environment, sandbox $HOME, a fake GitHub
# release served over `file://` — and checks what a user would see: the engine lands, the
# checksum is honoured, `kg` is on PATH, the store exists, and every way it can fail says
# so out loud instead of reporting `engine ready`.
#
# Nothing here reaches the network. `uname` is shadowed where a platform branch is under
# test, so the macOS and Windows paths are exercised from any machine.
#
# The fake plugin root deliberately has no `src/` and no `scripts/fetch-libs.sh`, so the
# source-build fallback fails immediately rather than spending a minute compiling Go on a
# host that happens to have a toolchain. Tests therefore assert the invariant a user cares
# about — "it told me it did not install" — not which of the three reasons it gave.
#
# Run:  bash tests/install-flow.sh [-v]
set -uo pipefail
. "$(dirname "${BASH_SOURCE[0]:-$0}")/lib.sh"

# This suite asks "did it install?", so it must start from an empty ~/.kgai — the stand-in
# engine the shared harness leaves there would answer the question before the test runs.
sandbox() { lib_sandbox; rm -f "$SB/.kgai/bin/kg"; }

# The platform the host really is, named the way install.sh names release assets.
host_release_os()   { uname -s | tr 'A-Z' 'a-z'; }
host_release_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  [ "$(host_release_os)" = darwin ] && echo x86_64 || echo x86_64 ;;
    aarch64|arm64) [ "$(host_release_os)" = darwin ] && echo arm64  || echo aarch64 ;;
    *) uname -m ;;
  esac
}

# ======================================================================================
# 1. The happy path
# ======================================================================================

t_fresh_install() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer
  assert_rc "the installer succeeds" "$RC" 0
  assert_has "it reports the engine ready" "$OUT" "engine ready"
  assert_hasnt "with no warning" "$OUT" "⚠"
  assert_exists "the engine is installed" "$SB/.kgai/bin/kg"
  [ -x "$SB/.kgai/bin/kg" ] || _fail "the engine is not executable"
  assert_exists "the native lib is installed" "$SB/.kgai/lib/$RELEASE_LIB_FILE"
  assert_exists "the fingerprint is recorded" "$SB/.kgai/.srcver"
  assert_file_has "the launcher is installed" "$SB/.local/bin/kg" "kgai launcher"
  assert_file_has "the PATH line is written" "$(expected_profile)" "added by kgai"
  assert_has "and the user is told how to use it" "$OUT" "/kgai:kg-ask"
}

# The whole point: a user who runs one session can then use kg in their own terminal. The
# shell must be started the way THIS platform's terminal starts it — a login shell on macOS
# and Git Bash (reads .bash_profile, where install.sh puts the line), a non-login
# interactive shell on Linux (reads .bashrc). Using -ic everywhere passed on Linux but
# failed on the macOS runner, because a non-login shell there never reads the .bash_profile
# the line correctly went into.
t_fresh_install_is_usable_in_a_new_terminal() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer
  local out flags
  case "$(uname -s)" in Darwin|MINGW*|MSYS*|CYGWIN*) flags="-lic" ;; *) flags="-ic" ;; esac
  out="$(env -i "HOME=$SB" "PATH=/usr/bin:/bin" "$BASH_BIN" $flags \
          'kg version' 2>/dev/null </dev/null | tail -n1)"
  assert_has "a fresh terminal runs kg through the launcher" "$out" '"ok":true'
}

t_second_run_is_quiet_and_changes_nothing() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer
  cp "$(expected_profile)" "$SB/bashrc-1"
  cp "$SB/.kgai/bin/kg" "$SB/engine-1"
  run_installer
  assert_rc "the second run succeeds" "$RC" 0
  assert_eq "and says nothing at all" "$OUT" ""
  assert_files_identical "the profile is unchanged" "$SB/bashrc-1" "$(expected_profile)"
  assert_files_identical "the engine is not re-downloaded" "$SB/engine-1" "$SB/.kgai/bin/kg"
}

# The engine is current, but the user deleted the launcher (or installed before it shipped).
t_repairs_a_missing_launcher() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer
  rm -f "$SB/.local/bin/kg"
  run_installer
  assert_file_has "the launcher is written again" "$SB/.local/bin/kg" "kgai launcher"
}

t_repairs_a_deleted_path_line() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer
  : > "$(expected_profile)"
  rm -f "$SB/.kgai/.path-ok"
  run_installer
  assert_file_has "the PATH line is written again" "$(expected_profile)" "added by kgai"
  assert_has "and reported" "$OUT" "on your PATH"
}

# A plugin update must reinstall the engine — the bug that left every Mac on the binary it
# first downloaded, because the fingerprint came out empty.
t_plugin_update_reinstalls() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  make_plugin_root "1.0.0"
  run_installer
  local first; first="$(cat "$SB/.kgai/.srcver")"
  assert_has "the fingerprint carries the plugin version" "$first" "1.0.0"
  make_plugin_root "1.1.0"
  run_installer
  assert_has "a new version reinstalls" "$OUT" "engine ready"
  assert_ne "and the fingerprint changes" "$(cat "$SB/.kgai/.srcver")" "$first"
}

t_fingerprint_is_never_empty() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  make_plugin_root "2.3.4"
  run_installer
  local v; v="$(cat "$SB/.kgai/.srcver")"
  assert_ne "the fingerprint is not empty" "$v" ""
  assert_has "it names the version" "$v" "2.3.4"
  # Three fields, always: an empty fingerprint would equal the empty file a previous run
  # wrote, and the "already current" check would then match forever.
  assert_eq "it has all three fields" "$(printf '%s' "$v" | tr -cd '|' | wc -c | tr -d ' ')" "2"
}

# ======================================================================================
# 2. Integrity of what is downloaded
# ======================================================================================

t_checksum_is_verified() {
  make_release "$(host_release_os)" "$(host_release_arch)" good
  run_installer
  assert_has "a matching checksum installs silently" "$OUT" "engine ready"
  assert_hasnt "with no complaint about verification" "$OUT" "checksum"
}

t_checksum_mismatch_is_refused() {
  make_release "$(host_release_os)" "$(host_release_arch)" bad
  run_installer
  assert_has "a mismatch is called out" "$OUT" "checksum MISMATCH"
  assert_absent "and the download is discarded" "$SB/.kgai/bin/kg"
  assert_no_match "leaving no partial file" "$SB/.kgai/bin/kg.new*"
  assert_has "the session is told the engine is missing" "$OUT" "ENGINE NOT INSTALLED"
  assert_hasnt "and never told it is ready" "$OUT" "engine ready"
}

t_missing_checksum_is_tolerated() {
  make_release "$(host_release_os)" "$(host_release_arch)" none
  run_installer
  assert_has "an unpublished checksum is noted" "$OUT" "no checksum published"
  assert_has "and the install proceeds" "$OUT" "engine ready"
}

t_truncated_download_is_refused() {
  make_release "$(host_release_os)" "$(host_release_arch)" good
  # Corrupt the asset after its checksum was computed — what a broken proxy delivers.
  printf 'garbage\n' > "$SB/release/kg-$(host_release_os)-$(host_release_arch)"
  run_installer
  assert_has "a corrupted asset is caught by the checksum" "$OUT" "checksum MISMATCH"
  assert_absent "and never installed" "$SB/.kgai/bin/kg"
}

t_unreachable_release_falls_back() {
  RELEASE_URL="file://$SB/no-such-release"
  run_installer
  assert_has "an unreachable release is reported" "$OUT" "prebuilt download failed"
  assert_has "and the session is told the engine is missing" "$OUT" "ENGINE NOT INSTALLED"
  assert_hasnt "never claiming readiness" "$OUT" "engine ready"
}

# Downloaded, checksum fine, and it still will not run here — the case that used to be
# announced as `engine ready` and then failed silently on every command.
t_engine_that_does_not_run() {
  make_release "$(host_release_os)" "$(host_release_arch)" good
  printf '#!/bin/sh\necho "dyld: symbol not found" >&2\nexit 1\n' \
    > "$SB/release/kg-$(host_release_os)-$(host_release_arch)"
  _sum "$SB/release/kg-$(host_release_os)-$(host_release_arch)" \
    > "$SB/release/kg-$(host_release_os)-$(host_release_arch).sha256"
  run_installer
  assert_has "the failure to run is reported" "$OUT" "does not run on this machine"
  assert_has "with the loader's own words" "$OUT" "dyld: symbol not found"
  assert_hasnt "and readiness is never claimed" "$OUT" "engine ready"
}

# ======================================================================================
# 3. An engine that stopped working between sessions
# ======================================================================================

t_installed_but_broken_engine() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer                                    # a good install first
  # Now it stops running: an OS upgrade, a deleted dylib, a moved home.
  printf '#!/bin/sh\nexit 3\n' > "$SB/.kgai/bin/kg"
  chmod +x "$SB/.kgai/bin/kg"
  run_installer
  assert_has "the dead engine is called out loudly" "$OUT" "ENGINE INSTALLED BUT NOT RUNNING"
  assert_has "with the repair" "$OUT" "rm -rf"
  assert_has "and a warning that the session will not work" "$OUT" "will NOT work"
  assert_hasnt "readiness is not claimed" "$OUT" "engine ready"
}

t_broken_engine_does_not_touch_the_profile() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  make_plugin_root "1.0.0"
  run_installer
  cp "$(expected_profile)" "$SB/bashrc-1"
  printf '#!/bin/sh\nexit 3\n' > "$SB/.kgai/bin/kg"; chmod +x "$SB/.kgai/bin/kg"
  run_installer
  assert_files_identical "a dead engine changes nothing else" "$SB/bashrc-1" "$(expected_profile)"
}

# ======================================================================================
# 4. Platforms
# ======================================================================================

t_macos_asset_names() {
  fake_uname Darwin arm64
  make_release darwin arm64
  run_installer
  assert_has "macOS installs from the darwin-arm64 asset" "$OUT" "prebuilt darwin-arm64"
  assert_exists "and gets the universal dylib" "$SB/.kgai/lib/libkuzu.dylib"
}

t_macos_intel_asset_names() {
  fake_uname Darwin x86_64
  make_release darwin x86_64
  run_installer
  assert_has "an Intel Mac installs from the darwin-x86_64 asset" "$OUT" "prebuilt darwin-x86_64"
  assert_exists "and the same universal dylib" "$SB/.kgai/lib/libkuzu.dylib"
}

t_linux_arm_asset_names() {
  fake_uname Linux aarch64
  make_release linux aarch64
  run_installer
  assert_has "arm64 Linux installs from the linux-aarch64 asset" "$OUT" "prebuilt linux-aarch64"
  assert_exists "and the .so" "$SB/.kgai/lib/libkuzu.so"
}

# Claude Code runs these hooks through Git Bash on Windows. There is no engine for it; the
# requirement is that it says so ACCURATELY — the message must point at WSL, not at the
# "install Go" / "github unreachable" that the build path emits when the refusal is placed
# after the toolchain checks (which is where it used to be, making the WSL message dead
# code). And it must not write a broken half-install first.
t_windows_is_refused_clearly() {
  fake_uname MINGW64_NT-10.0-22631 x86_64
  RELEASE_URL="file://$SB/no-such-release"
  run_installer
  assert_has "the session is told the engine is not installed" "$OUT" "ENGINE NOT INSTALLED"
  assert_has "the message names WSL, the actual remedy" "$OUT" "WSL"
  assert_hasnt "and does NOT misdirect to installing Go" "$OUT" "install Go"
  assert_hasnt "nor to a network problem" "$OUT" "github unreachable"
  assert_absent "and no engine is left behind" "$SB/.kgai/bin/kg"
  assert_hasnt "readiness is never claimed" "$OUT" "engine ready"
}

# Executing install.sh (not sourcing) with KGAI_INSTALL_LIB set must exit cleanly, not
# print a `return: can only ...` error and then run a full install anyway.
t_library_guard_when_executed() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer "KGAI_INSTALL_LIB=1"
  assert_hasnt "no return-outside-function error" "$ERR" "can only"
  assert_hasnt "and it did not proceed to install" "$OUT" "engine ready"
  assert_absent "no engine was installed" "$SB/.kgai/bin/kg"
}

# ======================================================================================
# 5. The store, and what the status line carries
# ======================================================================================

t_store_is_initialised_once() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer "KGTEST_INITIALIZED=false"
  assert_file_has "the engine was asked to init" "$SB/engine.log" "init"
  assert_count "exactly once" "$SB/engine.log" "init" 1
}

t_existing_store_is_left_alone() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer "KGTEST_INITIALIZED=true"
  assert_file_hasnt "an initialised store is not re-inited" "$SB/engine.log" "init"
}

t_conflicts_are_surfaced() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer "KGTEST_CONFLICTS=3"
  assert_has "unresolved conflicts are reported" "$OUT" "3 unresolved decision conflict"
  assert_has "with the command that resolves them" "$OUT" "/kgai:kg-conflicts"
}

t_autosync_failure_is_surfaced() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer
  mkdir -p "$SB/proj/.kgai/store"
  printf '{"ok": false, "detail": "expired sso"}\n' > "$SB/proj/.kgai/store/last-autosync.json"
  run_installer                       # the fast path, where the warning has to live too
  assert_has "a failing background sync is reported" "$OUT" "background team sync"
  assert_has "with what to run" "$OUT" "kg sync"
}

# Every engine call must happen in the project's root. Without that, a session in one
# repository reported another repository's conflict count.
t_engine_is_asked_in_the_project_root() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  mkdir -p "$SB/proj"
  run_installer "KGTEST_INITIALIZED=false" "KGTEST_CONFLICTS=1"
  # `version` is a pure smoke test and reads no store, so it may run anywhere. Every verb
  # that resolves a store must not.
  local line
  while IFS= read -r line; do
    case "$line" in
      version*) continue ;;
      *"cwd=$SB/proj") ;;
      *) _fail "a store-reading engine call ran outside the project root: $line" ;;
    esac
  done < "$SB/engine.log"
  assert_file_has "the conflicts check ran in the project" "$SB/engine.log" "conflicts cwd=$SB/proj"
  assert_file_has "so did the store check" "$SB/engine.log" "status cwd=$SB/proj"
}

t_status_line_is_one_line() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer "KGTEST_CONFLICTS=2"
  # SessionStart feeds stdout to the agent; more than one line has been mistaken for
  # tool output before.
  assert_eq "the status is a single line" "$(printf '%s' "$OUT" | wc -l | tr -d ' ')" "0"
}

t_nothing_is_written_to_stderr_on_success() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer
  assert_eq "a successful install is silent on stderr" "$ERR" ""
}

# ======================================================================================
# 6. The by-hand install
# ======================================================================================

# `curl … | bash` — BASH_SOURCE is unset there, and `set -u` used to abort on it.
t_piped_install() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer_piped
  assert_rc "a piped install succeeds" "$RC" 0
  assert_has "and reports the engine ready" "$OUT" "engine ready"
  assert_exists "the engine is installed" "$SB/.kgai/bin/kg"
}

t_piped_install_without_plugin_root() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  # No CLAUDE_PLUGIN_ROOT at all: the standalone case from the README.
  PLUGIN_ROOT=""
  OUT="$(env -i "HOME=$SB" "PATH=/usr/bin:/bin" "TMPDIR=$SB/tmp" \
    "KGAI_HOME=$SB/.kgai" "KGAI_USER_BIN=$SB/.local/bin" "KGAI_LOGIN_SHELL=$BASH_BIN" \
    "KG_RELEASE_BASE=$RELEASE_URL" \
    "$BASH_BIN" -c "$BASH_BIN < '$REPO/scripts/install.sh'" 2>"$SB/err")"
  assert_has "it still installs with no plugin root" "$OUT" "engine ready"
  assert_exists "and the engine is there" "$SB/.kgai/bin/kg"
}

t_kgai_home_override() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer "KGAI_HOME=$SB/elsewhere"
  assert_exists "KGAI_HOME is honoured" "$SB/elsewhere/bin/kg"
  assert_absent "and the default location stays empty" "$SB/.kgai/bin/kg"
}

t_user_bin_override() {
  make_release "$(host_release_os)" "$(host_release_arch)"
  run_installer "KGAI_USER_BIN=$SB/mybin"
  assert_file_has "KGAI_USER_BIN is honoured" "$SB/mybin/kg" "kgai launcher"
  assert_file_has "and it is what goes on PATH" "$(expected_profile)" "$SB/mybin"
}

# ======================================================================================

suite_header 'kgai — install.sh: the install itself'

section 'the happy path'
run 'a fresh install lands everything'               t_fresh_install
run 'and kg works in a new terminal'                 t_fresh_install_is_usable_in_a_new_terminal
run 'the second run is quiet and idempotent'         t_second_run_is_quiet_and_changes_nothing
run 'a deleted launcher is repaired'                 t_repairs_a_missing_launcher
run 'a deleted PATH line is repaired'                t_repairs_a_deleted_path_line
run 'a plugin update reinstalls the engine'          t_plugin_update_reinstalls
run 'the fingerprint is never empty'                 t_fingerprint_is_never_empty

section 'integrity of the download'
run 'a good checksum installs quietly'               t_checksum_is_verified
run 'a mismatched checksum is refused'               t_checksum_mismatch_is_refused
run 'a missing checksum is tolerated, with a note'   t_missing_checksum_is_tolerated
run 'a corrupted asset is caught'                    t_truncated_download_is_refused
run 'an unreachable release is reported'             t_unreachable_release_falls_back
run 'an engine that will not run is reported'        t_engine_that_does_not_run

section 'an engine that stopped working'
run 'a dead engine is called out, not "ready"'       t_installed_but_broken_engine
run 'and it changes nothing else'                    t_broken_engine_does_not_touch_the_profile

section 'platforms'
run 'macOS arm64 asset names'                        t_macos_asset_names
run 'macOS x86_64 asset names'                       t_macos_intel_asset_names
run 'Linux aarch64 asset names'                      t_linux_arm_asset_names
run 'Windows/Git Bash is refused clearly'            t_windows_is_refused_clearly
run 'library guard exits cleanly when executed'      t_library_guard_when_executed

section 'the store and the status line'
run 'an empty store is initialised once'             t_store_is_initialised_once
run 'an existing store is left alone'                t_existing_store_is_left_alone
run 'unresolved conflicts are surfaced'              t_conflicts_are_surfaced
run 'a failing background sync is surfaced'          t_autosync_failure_is_surfaced
run 'every engine call runs in the project root'      t_engine_is_asked_in_the_project_root
run 'the status line is one line'                    t_status_line_is_one_line
run 'success is silent on stderr'                    t_nothing_is_written_to_stderr_on_success

section 'the by-hand install'
run 'curl | bash installs'                           t_piped_install
run 'and works with no plugin root at all'           t_piped_install_without_plugin_root
run 'KGAI_HOME is honoured'                          t_kgai_home_override
run 'KGAI_USER_BIN is honoured'                      t_user_bin_override

summary
