#!/usr/bin/env bash
# tests/install-rc-safety.sh — what the installer is allowed to do to a shell profile.
#
# This script edits a file that the user's login shell executes, unattended, at every
# session start. That makes three properties non-negotiable, and each of them had already
# been violated in a way reproduced here before it was fixed:
#
#   1. Nothing that was in the file may change. Only appending is allowed, and what is
#      appended must be removable, leaving the original bytes intact.
#   2. The directory name goes in as DATA. Unquoted it was code: a path with a space
#      became two broken PATH entries, and a path with a quote in it became commands the
#      user's shell ran at every login.
#   3. One block, ever. Every Claude Code window runs this script; eight of them starting
#      together appended eight copies of the same two lines and nothing ever removed them.
#
# Run:  bash tests/install-rc-safety.sh [-v]
set -uo pipefail
. "$(dirname "${BASH_SOURCE[0]:-$0}")/lib.sh"

# A profile with the awkward things real profiles have in them: blank lines, comments,
# non-ASCII, quotes, a trailing-whitespace line.
realistic_profile() {
  cat <<'RC'
# ~/.bashrc — mine
export EDITOR=vim

# fungovalo to už v roce 2019 — ponechat
alias ll='ls -la'
if [ -n "$PS1" ]; then
  PS1='\u@\h:\w\$ '
fi
export GREETING="say \"hi\""
RC
}

snapshot() { cp "$1" "$SB/original"; }

# ======================================================================================
# 1. Appending only
# ======================================================================================

t_preserves_existing_content() {
  load; host_os() { echo Linux; }
  realistic_profile > "$HOME/.bashrc"; snapshot "$HOME/.bashrc"
  ensure_path_entry; assert_rc "the line is added" $? 0
  assert_prefix_preserved "nothing existing is touched" "$SB/original" "$HOME/.bashrc"
  assert_file_has "the user's own settings survive" "$HOME/.bashrc" "alias ll="
  assert_file_has "including quoted ones" "$HOME/.bashrc" 'say \"hi\"'
}

t_no_trailing_newline_profile() {
  load; host_os() { echo Linux; }
  printf 'export EDITOR=vim' > "$HOME/.bashrc"      # no final newline
  snapshot "$HOME/.bashrc"
  ensure_path_entry
  assert_prefix_preserved "a file with no final newline keeps its bytes" \
    "$SB/original" "$HOME/.bashrc"
  # The appended block must still be a line of its own, not glued to their last one.
  assert_file_hasnt "the last line is not glued to ours" "$HOME/.bashrc" 'vim#'
  assert_file_has "and our line is intact" "$HOME/.bashrc" 'export PATH='
}

t_crlf_profile() {
  load; host_os() { echo Linux; }
  printf 'export EDITOR=vim\r\nalias ll=ls\r\n' > "$HOME/.bashrc"
  snapshot "$HOME/.bashrc"
  ensure_path_entry; assert_rc "a CRLF profile is appended to" $? 0
  assert_prefix_preserved "CRLF bytes are not rewritten" "$SB/original" "$HOME/.bashrc"
}

t_binary_ish_profile() {
  load; host_os() { echo Linux; }
  # Not valid UTF-8. The installer must not care what is in there.
  printf 'export A=1\n\303\050\277\n' > "$HOME/.bashrc"
  snapshot "$HOME/.bashrc"
  ensure_path_entry
  assert_prefix_preserved "invalid UTF-8 is left alone" "$SB/original" "$HOME/.bashrc"
}

t_block_is_removable() {
  load; host_os() { echo Linux; }
  realistic_profile > "$HOME/.bashrc"; snapshot "$HOME/.bashrc"
  ensure_path_entry
  # The documented removal: drop the marked line and the one after it.
  grep -v -F "$KGAI_MARK" "$HOME/.bashrc" | grep -v -F "$KGAI_USER_BIN" > "$SB/stripped"
  # What is left differs from the original only by the blank separator line.
  sed -e '$ { /^$/d; }' "$SB/stripped" > "$SB/stripped2"
  assert_files_identical "removing the block restores the profile" \
    "$SB/original" "$SB/stripped2"
}

t_block_is_three_lines() {
  load; host_os() { echo Linux; }
  : > "$HOME/.bashrc"
  ensure_path_entry
  assert_eq "the block is a blank line, a marker and one export" \
    "$(wc -l < "$HOME/.bashrc" | tr -d ' ')" "3"
}

t_preserves_mode() {
  load; host_os() { echo Linux; }
  realistic_profile > "$HOME/.bashrc"; chmod 600 "$HOME/.bashrc"
  ensure_path_entry
  assert_mode "an appended profile keeps its permissions" "$HOME/.bashrc" "-rw-------"
}

# A profile symlinked into a dotfiles repository is the normal setup for anyone who keeps
# theirs in git. Appending must follow the link and land in the repo, never replace the
# link with a regular file.
t_symlinked_profile() {
  need_symlinks || return 0
  load; host_os() { echo Linux; }
  mkdir -p "$SB/dotfiles"
  realistic_profile > "$SB/dotfiles/bashrc"
  ln -s "$SB/dotfiles/bashrc" "$HOME/.bashrc"
  ensure_path_entry; assert_rc "a symlinked profile is written" $? 0
  [ -L "$HOME/.bashrc" ] || _fail "the symlink was replaced by a regular file"
  assert_file_has "the line landed in the dotfiles repo" \
    "$SB/dotfiles/bashrc" "$KGAI_MARK"
}

t_readonly_profile_is_not_corrupted() {
  need_write_deny || return 0
  load; host_os() { echo Linux; }
  realistic_profile > "$HOME/.bashrc"; snapshot "$HOME/.bashrc"
  chmod 444 "$HOME/.bashrc"
  ensure_path_entry; assert_rc "a read-only profile is reported, not forced" $? 1
  chmod 644 "$HOME/.bashrc"
  assert_files_identical "and left byte for byte identical" "$SB/original" "$HOME/.bashrc"
}

t_profile_is_a_directory() {
  load; host_os() { echo Linux; }
  mkdir -p "$HOME/.bashrc"
  ensure_path_entry; assert_rc "a directory where the profile should be does not crash" $? 1
}

# Switching shells must not rewrite the old shell's profile.
t_switching_shells() {
  load; host_os() { echo Linux; }
  export KGAI_LOGIN_SHELL=/bin/bash
  ensure_path_entry
  cp "$HOME/.bashrc" "$SB/bashrc-after-first"
  export KGAI_LOGIN_SHELL=/bin/zsh
  ensure_path_entry; assert_rc "the new shell's profile gets its own line" $? 0
  assert_file_has "zsh is wired" "$HOME/.zshrc" "$KGAI_MARK"
  assert_files_identical "bash's profile is untouched" \
    "$SB/bashrc-after-first" "$HOME/.bashrc"
}

# ======================================================================================
# 2. The path is data, not code
# ======================================================================================

# Each payload is a legal directory name that used to end up executing, or breaking, when
# it was interpolated into the profile bare. For every one: sourcing the resulting profile
# must put exactly that directory at the front of PATH, and must run nothing.
t_quoting_is_safe() {
  load; host_os() { echo Linux; }
  local sentinel="$SB/EXECUTED" n=0 p first sb TAB
  # An ABSOLUTE path: the injected line clobbers PATH before its own payload runs, so a
  # bare `touch` would fail for the wrong reason and the sentinel would never fire.
  local touch_abs; touch_abs="$(command -v touch)"
  TAB="$(printf '\t')"
  # An indexed array, NOT a `$(cat <<EOF)` list: a here-doc that contains an unbalanced
  # quote, nested inside a command substitution, does not even PARSE under bash 3.2 (macOS
  # /bin/bash) — so the whole suite exited with zero tests on the very platform it targets.
  # The last two elements are the real injection attempts (they try to close the
  # installer's own quote and run a command); the rest are legal names that used to break
  # the written line rather than exploit it. The tab is a real tab, not a literal `\t`.
  local payloads=(
    'with space'
    "with'quote"
    'with"doublequote'
    'with$DOLLAR'
    'with`backtick`'
    'with;semicolon'
    'with&&and'
    'with*glob'
    'with|pipe'
    'with>redirect'
    'with(paren)'
    'with#hash'
    'with\backslash'
    "with${TAB}tab"
    "x\";$touch_abs $sentinel;\""
    "x';$touch_abs $sentinel;'"
  )
  # A probe that sources a profile in an empty environment and reports what PATH begins
  # with — the only question that matters about the line we wrote.
  cat > "$SB/probe.sh" <<'PROBE'
PATH=/usr/bin:/bin
. "$1" >/dev/null 2>&1
printf '%s' "${PATH%%:*}"
PROBE
  for p in "${payloads[@]}"; do
    n=$((n + 1))
    sb="$SB/case$n"
    mkdir -p "$sb"
    HOME="$sb"; USER_BIN="$sb/$p"; PATH_RC=""
    rm -f "$sentinel" "$KGAI_HOME/.path-ok"
    ensure_path_entry >/dev/null 2>&1
    if [ -z "$PATH_RC" ]; then
      _fail "case $n ($p): nothing was written for a writable name"
      continue
    fi
    first="$(env -i "$BASH_BIN" "$SB/probe.sh" "$PATH_RC" 2>/dev/null)"
    assert_eq "case $n ($p): PATH starts with exactly the directory" "$first" "$USER_BIN"
    if [ -e "$sentinel" ]; then
      _fail "case $n ($p): sourcing the profile EXECUTED the payload"
    fi
  done
  assert_eq "every payload was exercised" "$n" "16"
}

# The same, for fish syntax — where an unquoted space is a list separator rather than a
# syntax error, so the breakage is silent.
t_fish_quoting() {
  load; export KGAI_LOGIN_SHELL=/usr/bin/fish
  USER_BIN="$SB/My Tools/bin"
  assert_eq "a space is quoted for fish" "$(path_line)" \
    "set -gx PATH '$SB/My Tools/bin' \$PATH"
  USER_BIN="$SB/it's/bin"
  assert_eq "a quote is escaped for fish" "$(path_line)" \
    "set -gx PATH '$SB/it\\'s/bin' \$PATH"
  USER_BIN="$SB/back\\slash/bin"
  assert_has "a backslash is escaped for fish" "$(path_line)" "back\\\\slash"
}

t_newline_in_path_is_refused() {
  load; host_os() { echo Linux; }
  USER_BIN="$SB/two
lines"
  ensure_path_entry; assert_rc "a newline in the directory name is refused" $? 1
  assert_absent "and nothing is written" "$HOME/.bashrc"
}

t_written_line_is_reparseable() {
  load; host_os() { echo Linux; }
  ensure_path_entry
  # The profile must remain valid shell. A syntax error here breaks every login.
  "$BASH_BIN" -n "$HOME/.bashrc" 2>"$SB/synerr" || _fail "the profile no longer parses"
  [ -s "$SB/synerr" ] && _fail "bash -n complained: $(cat "$SB/synerr")"
  return 0
}

# ======================================================================================
# 3. One block, ever
# ======================================================================================

t_concurrent_sessions_write_once() {
  load; host_os() { echo Linux; }
  : > "$HOME/.bashrc"
  local i
  for i in 1 2 3 4 5 6 7 8; do
    ( . "$REPO/scripts/install.sh"
      system_path_files() { printf '%s\n' "$HOME/etc/paths"; }
      host_os() { echo Linux; }
      ensure_path_entry >/dev/null 2>&1 ) &
  done
  wait 2>/dev/null
  assert_count "eight sessions at once add one block" "$HOME/.bashrc" "$KGAI_MARK" 1
}

# The hard case the two tests above miss on their own: a lock left behind by a killed
# session (stale) AND fresh sessions starting at the same time. The naive stale-break lets
# two reclaimers each rebuild-and-hold the lock, so both append — reproduced before the
# break-lock serialised them. Many rounds, because the race is timing-dependent.
t_concurrent_sessions_with_stale_lock() {
  load; host_os() { echo Linux; }
  touch -t 200001010000 "$KGAI_HOME" 2>/dev/null ||
    { skip "touch -t unavailable"; return 0; }
  local round i
  for round in 1 2 3 4 5 6 7 8 9 10; do
    : > "$HOME/.bashrc"
    rm -rf "$KGAI_HOME/.rc.lock"
    mkdir -p "$KGAI_HOME/.rc.lock"; printf '99999\n' > "$KGAI_HOME/.rc.lock/pid"
    touch -t 200001010000 "$KGAI_HOME/.rc.lock" 2>/dev/null   # aged: a killed session's lock
    for i in 1 2 3 4 5 6 7 8; do
      ( . "$REPO/scripts/install.sh"
        system_path_files() { printf '%s\n' "$HOME/etc/paths"; }
        host_os() { echo Linux; }
        ensure_path_entry >/dev/null 2>&1 ) &
    done
    wait 2>/dev/null
    assert_count "round $round: stale lock + 8 sessions → one block" "$HOME/.bashrc" "$KGAI_MARK" 1
  done
}

t_concurrent_launchers_write_once() {
  load
  # Separate PROCESSES, not `( … ) &` subshells: in a subshell `$$` stays the parent's pid,
  # so eight subshells would have shared one temp name and never exercised the per-process
  # isolation this test is about. Each SessionStart is its own `bash install.sh` process, so
  # model that — every racer gets a distinct pid (and mktemp makes the temp unique anyway).
  local i
  for i in 1 2 3 4 5 6 7 8; do
    env HOME="$HOME" KGAI_HOME="$KGAI_HOME" KGAI_USER_BIN="$KGAI_USER_BIN" \
        KGAI_INSTALL_LIB=1 CLAUDE_PLUGIN_ROOT="$REPO" \
        "$BASH_BIN" -c '. "$CLAUDE_PLUGIN_ROOT/scripts/install.sh"; write_launcher' \
        >/dev/null 2>&1 &
  done
  wait 2>/dev/null
  assert_file_has "the launcher is whole" "$KGAI_USER_BIN/kg" "exec \"\$BIN\""
  assert_no_match "no temp files survive the race" "$KGAI_USER_BIN/kg.new.*"
}

t_held_lock_defers() {
  load; host_os() { echo Linux; }
  mkdir -p "$KGAI_HOME/.rc.lock"          # another session is mid-write
  ensure_path_entry; assert_rc "a held lock defers instead of duplicating" $? 1
  assert_absent "nothing is written" "$HOME/.bashrc"
}

t_stale_lock_is_broken() {
  load; host_os() { echo Linux; }
  mkdir -p "$KGAI_HOME/.rc.lock"
  # A session killed mid-write must not block the fix forever.
  touch -t 200001010000 "$KGAI_HOME/.rc.lock" 2>/dev/null ||
    { skip "touch -t unavailable"; return 0; }
  ensure_path_entry; assert_rc "a stale lock is broken" $? 0
  assert_file_has "and the line is written" "$HOME/.bashrc" "$KGAI_MARK"
}

t_lock_is_released() {
  load; host_os() { echo Linux; }
  ensure_path_entry
  assert_absent "the lock does not leak" "$KGAI_HOME/.rc.lock"
}

t_lock_released_after_failure() {
  need_write_deny || return 0
  load; host_os() { echo Linux; }
  : > "$HOME/.bashrc"; chmod 444 "$HOME/.bashrc"
  ensure_path_entry
  chmod 644 "$HOME/.bashrc"
  assert_absent "a failed write still releases the lock" "$KGAI_HOME/.rc.lock"
}

t_repeated_runs_never_grow() {
  load; host_os() { echo Linux; }
  realistic_profile > "$HOME/.bashrc"
  local i
  for i in 1 2 3 4 5; do ensure_path_entry >/dev/null 2>&1; done
  assert_count "five sequential runs add one block" "$HOME/.bashrc" "$KGAI_MARK" 1
  assert_count "and one export" "$HOME/.bashrc" "export PATH=" 1
}

# A user who deletes our line should get it back — but exactly one copy of it.
t_relapse_after_manual_removal() {
  load; host_os() { echo Linux; }
  ensure_path_entry
  grep -v -F "$KGAI_MARK" "$HOME/.bashrc" | grep -v -F "$KGAI_USER_BIN" > "$SB/x"
  mv "$SB/x" "$HOME/.bashrc"
  rm -f "$KGAI_HOME/.path-ok"
  ensure_path_entry; assert_rc "a removed line is written again" $? 0
  assert_count "once" "$HOME/.bashrc" "$KGAI_MARK" 1
}

# ======================================================================================
# 4. Blast radius
# ======================================================================================

# The installer must write inside $HOME and $KGAI_HOME and nowhere else.
t_writes_nothing_outside_home() {
  load; host_os() { echo Linux; }
  local guard="$TMPROOT/guard$T"
  mkdir -p "$guard"; : > "$guard/canary"
  cp "$guard/canary" "$SB/canary-original"
  ensure_on_path >/dev/null 2>&1
  assert_files_identical "an unrelated directory is untouched" \
    "$SB/canary-original" "$guard/canary"
  # Everything created must be under $HOME.
  local stray
  stray="$(find "$guard" -newer "$SB/canary-original" -type f 2>/dev/null | head -n1)"
  assert_eq "nothing was created outside the sandbox home" "$stray" ""
}

t_engine_is_never_rewritten() {
  load
  cp "$KGAI_HOME/bin/kg" "$SB/engine-original"
  ensure_on_path >/dev/null 2>&1
  assert_files_identical "wiring PATH does not touch the engine" \
    "$SB/engine-original" "$KGAI_HOME/bin/kg"
}

t_foreign_kg_is_never_rewritten() {
  load; host_os() { echo Linux; }
  mkdir -p "$KGAI_USER_BIN"
  printf '#!/bin/sh\necho some other tool\n' > "$KGAI_USER_BIN/kg"
  chmod +x "$KGAI_USER_BIN/kg"
  cp "$KGAI_USER_BIN/kg" "$SB/foreign-original"
  ensure_on_path >/dev/null 2>&1
  assert_files_identical "someone else's kg is byte-identical afterwards" \
    "$SB/foreign-original" "$KGAI_USER_BIN/kg"
}

t_stamp_write_failure_is_survivable() {
  load; host_os() { echo Linux; }
  # Genuine coverage + a reachable shell → the code path that WRITES the stamp. Make only
  # the stamp write fail, by putting a directory where the stamp file goes; the lock (also
  # under $KGAI_HOME) must still work, so this exercises the stamp-write failure itself
  # rather than failing earlier at the lock (the reason the old chmod-555 version passed).
  fake_shell "/usr/bin:$KGAI_USER_BIN:/bin"
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  mkdir -p "$KGAI_HOME/.path-ok"        # a dir, so `printf > .path-ok` cannot succeed
  ensure_path_entry
  assert_rc "a failed stamp write still returns cleanly (covered → no append)" $? 1
  assert_count "and nothing is appended" "$HOME/.bashrc" "$KGAI_MARK" 0
}

# ======================================================================================

suite_header 'kgai — install.sh: shell-profile safety'

section 'appending only'
run 'existing content is preserved'                  t_preserves_existing_content
run 'a profile with no final newline'                t_no_trailing_newline_profile
run 'a CRLF profile'                                 t_crlf_profile
run 'a profile that is not valid UTF-8'              t_binary_ish_profile
run 'the block can be removed again'                 t_block_is_removable
run 'the block is exactly three lines'               t_block_is_three_lines
run 'file permissions are preserved'                 t_preserves_mode
run 'a symlinked profile is followed, not replaced'  t_symlinked_profile
run 'a read-only profile is left identical'          t_readonly_profile_is_not_corrupted
run 'a directory in place of the profile'            t_profile_is_a_directory
run 'switching shells leaves the old profile alone'  t_switching_shells

section 'the path is data, not code'
run '16 hostile directory names stay inert'          t_quoting_is_safe
run 'fish quoting: space, quote, backslash'          t_fish_quoting
run 'a newline in the directory name is refused'     t_newline_in_path_is_refused
run 'the profile still parses afterwards'            t_written_line_is_reparseable

section 'one block, ever'
run 'eight concurrent sessions write one block'      t_concurrent_sessions_write_once
run 'stale lock + concurrency still writes one'      t_concurrent_sessions_with_stale_lock
run 'eight concurrent launcher writes are whole'     t_concurrent_launchers_write_once
run 'a held lock defers'                             t_held_lock_defers
run 'a stale lock is broken'                         t_stale_lock_is_broken
run 'the lock is released'                           t_lock_is_released
run 'the lock is released after a failure'           t_lock_released_after_failure
run 'five sequential runs never grow the file'       t_repeated_runs_never_grow
run 'a manually removed line comes back once'        t_relapse_after_manual_removal

section 'blast radius'
run 'nothing is written outside HOME'                t_writes_nothing_outside_home
run 'the engine is never rewritten'                  t_engine_is_never_rewritten
run "a foreign kg is never rewritten"                t_foreign_kg_is_never_rewritten
run 'an unwritable KGAI_HOME is survivable'          t_stamp_write_failure_is_survivable

summary
