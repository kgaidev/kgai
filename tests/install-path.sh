#!/usr/bin/env bash
# tests/install-path.sh — the PATH wiring in scripts/install.sh, exercised against a
# sandbox $HOME.
#
# The installer's job outside Claude Code is two independent things: a launcher at
# $USER_BIN/kg, and $USER_BIN on the PATH a terminal the user opens themselves builds.
# Both used to be decided by reading shell profiles as plain text, and every failure was
# reported as `engine ready` — so the whole class of bug was invisible. These tests exist
# because that silence is only fixable if something else does the looking.
#
# scripts/install.sh sourced with KGAI_INSTALL_LIB=1 defines its functions and stops
# before it would download or build anything, so nothing here touches the network, the
# real $HOME, or the real /etc (system_path_files is redirected into the sandbox).
#
# Run:  bash tests/install-path.sh [-v]        (exit 0 = all green)
#
# Written for bash 3.2: no associative arrays, no mapfile, no ${x^^} — macOS ships 3.2 and
# that is the platform most of these bugs came from.
set -uo pipefail
. "$(dirname "${BASH_SOURCE[0]:-$0}")/lib.sh"

# ======================================================================================
# A. Reading a profile: what counts as "$USER_BIN is already handled"
# ======================================================================================

t_scan_empty() {
  load; : > "$HOME/.bashrc"
  path_covered; assert_false "empty rc is not coverage" $?
}

t_scan_plain_export() {
  load; printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  path_covered; assert_true "a live export counts" $?
}

# The reported bug. uv, pipx and pip all leave a line like this behind.
t_scan_commented_out() {
  load
  printf '# uv installer added this once:\n# export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  path_covered; assert_false "a commented mention is NOT coverage" $?
}

t_scan_commented_no_space() {
  load; printf '#export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  path_covered; assert_false "#export with no space is NOT coverage" $?
}

t_scan_commented_indented() {
  load; printf '\t   # export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  path_covered; assert_false "an indented comment is NOT coverage" $?
}

t_scan_warning_note() {
  load
  printf '# WARNING: The script kg is installed in %s which is not on PATH.\n' \
    "'$HOME/.local/bin'" > "$HOME/.bashrc"
  path_covered; assert_false "pip's not-on-PATH warning is NOT coverage" $?
}

t_scan_trailing_comment() {
  load; printf 'export PATH="$HOME/.local/bin:$PATH"  # added by me\n' > "$HOME/.bashrc"
  path_covered; assert_true "a live line with a trailing comment counts" $?
}

t_scan_tilde() {
  load; printf 'export PATH="~/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  path_covered; assert_true "the ~/ spelling counts" $?
}

t_scan_home_braces() {
  load; printf 'export PATH="${HOME}/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  path_covered; assert_true "the \${HOME}/ spelling counts" $?
}

t_scan_absolute() {
  load; printf 'export PATH="%s:$PATH"\n' "$KGAI_USER_BIN" > "$HOME/.bashrc"
  path_covered; assert_true "the fully expanded spelling counts" $?
}

t_scan_system_file() {
  load; printf '/usr/bin\n%s\n' "$KGAI_USER_BIN" > "$HOME/etc/paths"
  path_covered; assert_true "a system path file counts" $?
}

t_scan_system_file_commented() {
  load; printf '/usr/bin\n#%s\n' "$KGAI_USER_BIN" > "$HOME/etc/paths"
  path_covered; assert_false "a commented system entry is NOT coverage" $?
}

# rc_files is per shell family: a line only this user's zsh would read says nothing about
# the bash terminal they actually open.
t_scan_other_shells_rc() {
  load; export KGAI_LOGIN_SHELL=/bin/bash
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.zshrc"
  path_covered; assert_false "a zsh-only rc does not cover a bash terminal" $?
}

t_scan_no_trailing_newline() {
  load; printf 'export PATH="$HOME/.local/bin:$PATH"' > "$HOME/.bashrc"   # no \n
  path_covered; assert_true "a last line without a newline is still read" $?
}

t_scan_home_with_space() {
  load
  local spaced="$SB/My Home"; mkdir -p "$spaced"
  HOME="$spaced"; USER_BIN="$spaced/.local/bin"
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$spaced/.bashrc"
  path_covered; assert_true "a home directory with a space is handled" $?
}

# With $USER_BIN outside $HOME the short spellings mean something else entirely.
t_scan_user_bin_outside_home() {
  load; USER_BIN="/opt/kgbin"
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  path_covered; assert_false "~/.local/bin does not cover /opt/kgbin" $?
  printf 'export PATH="/opt/kgbin:$PATH"\n' >> "$HOME/.bashrc"
  path_covered; assert_true "the absolute spelling does" $?
}

# ======================================================================================
# B. Choosing the file to write to
# ======================================================================================

t_target_zsh() {
  load; export KGAI_LOGIN_SHELL=/bin/zsh
  assert_eq "zsh writes to .zshrc" "$(rc_target)" "$HOME/.zshrc"
}

t_target_bash_darwin_fresh() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Darwin; }
  assert_eq "macOS bash with no profile creates .bash_profile" \
    "$(rc_target)" "$HOME/.bash_profile"
}

# bash reads exactly ONE of .bash_profile / .bash_login / .profile. Creating
# .bash_profile next to an existing .profile silently stops .profile being read at all.
t_target_bash_darwin_only_profile() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Darwin; }
  : > "$HOME/.profile"
  assert_eq "an existing .profile is used, not shadowed" "$(rc_target)" "$HOME/.profile"
}

t_target_bash_darwin_existing() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Darwin; }
  : > "$HOME/.bash_profile"; : > "$HOME/.profile"
  assert_eq ".bash_profile wins when it exists" "$(rc_target)" "$HOME/.bash_profile"
}

t_target_bash_linux() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  : > "$HOME/.profile"
  assert_eq "a Linux terminal is not a login shell — .bashrc" "$(rc_target)" "$HOME/.bashrc"
}

t_target_bash_gitbash() {
  load; export KGAI_LOGIN_SHELL=/usr/bin/bash; host_os() { echo MINGW64_NT-10.0-22631; }
  assert_true "Git Bash opens a login shell" "$(terminal_is_login; echo $?)"
  assert_eq "Git Bash writes to .bash_profile" "$(rc_target)" "$HOME/.bash_profile"
}

t_target_fish() {
  load; export KGAI_LOGIN_SHELL=/usr/bin/fish
  assert_eq "fish writes to config.fish" "$(rc_target)" "$HOME/.config/fish/config.fish"
  # Quoted: fish treats a space as a list separator, so an unquoted path with a space in
  # it used to become two broken PATH entries.
  assert_eq "fish gets fish syntax, quoted" "$(path_line)" \
    "set -gx PATH '$KGAI_USER_BIN' \$PATH"
}

t_target_unknown_shell() {
  load; export KGAI_LOGIN_SHELL=/bin/ksh
  assert_eq "an unknown shell falls back to .profile" "$(rc_target)" "$HOME/.profile"
}

t_login_shell_override() {
  load; export KGAI_LOGIN_SHELL=/opt/weird/fish
  assert_eq "KGAI_LOGIN_SHELL wins over the passwd entry" "$(shell_family)" "fish"
}

# ======================================================================================
# C. Writing the PATH entry
# ======================================================================================

t_entry_clean() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  ensure_path_entry; assert_rc "a clean home gets the line" $? 0
  assert_file_has "the marker is written" "$HOME/.bashrc" "$KGAI_MARK"
  assert_file_has "the export is written" "$HOME/.bashrc" "$KGAI_USER_BIN"
  assert_eq "PATH_RC names the file written" "$PATH_RC" "$HOME/.bashrc"
}

t_entry_idempotent() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  ensure_path_entry; assert_rc "first run appends" $? 0
  ensure_path_entry; assert_rc "second run does not" $? 1
  ensure_path_entry; assert_rc "third run does not either" $? 1
  assert_count "exactly one marked block" "$HOME/.bashrc" "$KGAI_MARK" 1
}

t_entry_commented_mention() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  printf '# export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  ensure_path_entry; assert_rc "a commented mention does not block the write" $? 0
  assert_file_has "the line is there now" "$HOME/.bashrc" "$KGAI_MARK"
}

t_entry_genuinely_covered() {
  load; host_os() { echo Linux; }
  fake_shell "/usr/bin:$KGAI_USER_BIN:/bin"
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  ensure_path_entry; assert_rc "no line when it is genuinely covered" $? 1
  assert_count "nothing was appended" "$HOME/.bashrc" "$KGAI_MARK" 0
  assert_exists "the confirmation is remembered" "$KGAI_HOME/.path-ok"
  assert_eq "the stamp records which dir was confirmed" \
    "$(cat "$KGAI_HOME/.path-ok")" "$KGAI_USER_BIN"
}

# A stamp is only allowed to skip the expensive probe — and only while the coverage that
# earned it is STILL in the profile. Here the covering line is present, so a stamp means
# "don't bother re-running the shell": no probe, no append.
t_entry_stamp_skips_probe_when_covered() {
  load; host_os() { echo Linux; }
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  printf '%s\n' "$KGAI_USER_BIN" > "$KGAI_HOME/.path-ok"
  # A shell that would report the opposite — with the stamp present it must not be consulted.
  fake_shell "/usr/bin:/bin"
  ensure_path_entry; assert_rc "a stamp skips the probe while coverage holds" $? 1
  assert_count "and nothing is appended" "$HOME/.bashrc" "$KGAI_MARK" 0
}

# The bug the stamp introduced: coverage came from an external line (uv/pipx/user export),
# the stamp was written, then that line was removed. The stamp must NOT keep the installer
# from re-adding the line — otherwise the session reports success while kg is unreachable,
# exactly the silent failure this release exists to kill.
t_entry_stamp_does_not_survive_lost_coverage() {
  load; host_os() { echo Linux; }
  # No covering line anywhere, but a stamp that still names this dir.
  : > "$HOME/.bashrc"
  printf '%s\n' "$KGAI_USER_BIN" > "$KGAI_HOME/.path-ok"
  fake_shell "/usr/bin:/bin"                        # a real shell: NOT reachable
  ensure_path_entry; assert_rc "lost coverage falls through to the append" $? 0
  assert_file_has "the line is written despite the stamp" "$HOME/.bashrc" "$KGAI_MARK"
  assert_absent "and the stale stamp is dropped" "$KGAI_HOME/.path-ok"
}

t_entry_stamp_other_dir() {
  load; host_os() { echo Linux; }
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  printf '%s\n' "/somewhere/else" > "$KGAI_HOME/.path-ok"
  fake_shell "/usr/bin:/bin"
  # Covered (per the scan) but the stamp names a different dir → probe runs, is unreachable,
  # line is appended.
  ensure_path_entry; assert_rc "a stamp for another dir does not count" $? 0
}

# The scan can read a file but not run it. A mention inside a branch that never fires
# looks identical to a working entry until a real shell is asked.
t_entry_conditional_mention() {
  load; host_os() { echo Linux; }
  fake_shell "/usr/bin:/bin"                       # the shell does NOT have it
  printf 'if false; then export PATH="$HOME/.local/bin:$PATH"; fi\n' > "$HOME/.bashrc"
  ensure_path_entry; assert_rc "the probe overrules the scan" $? 0
  assert_file_has "the line is written after all" "$HOME/.bashrc" "$KGAI_MARK"
  assert_absent "nothing is stamped as confirmed" "$KGAI_HOME/.path-ok"
}

t_entry_probe_unavailable() {
  load; host_os() { echo Linux; }
  # Named bash so the family (and therefore the profile) is the usual one; it simply is
  # not there to be run.
  export KGAI_LOGIN_SHELL="$SB/gone/bash"
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  ensure_path_entry; assert_rc "with no probe the scan is trusted" $? 1
  assert_count "and nothing is appended" "$HOME/.bashrc" "$KGAI_MARK" 0
}

t_entry_probe_says_nothing() {
  load; host_os() { echo Linux; }
  fake_shell NOISE-ONLY                            # runs, but answers nothing usable
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  ensure_path_entry; assert_rc "an unparseable probe is not a verdict" $? 1
}

# A write FAILURE is rc 2, distinct from rc 1's "nothing to do" — folding them together
# is how a read-only profile meant a bare "engine ready" with kg unreachable, for good.
t_entry_unwritable_rc() {
  need_write_deny || return 0
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  : > "$HOME/.bashrc"; chmod 444 "$HOME/.bashrc"
  ensure_path_entry; assert_rc "an unwritable profile is a write failure, not silence" $? 2
  chmod 644 "$HOME/.bashrc"
}

# The scan must not leak bash's own Permission denied onto stderr at every session start
# when a profile in the search list exists but cannot be read (root-owned 600 files under
# /etc/profile.d are routine on managed machines).
t_scan_unreadable_file_is_quiet() {
  need_write_deny || return 0
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  printf 'export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.profile"
  chmod 000 "$HOME/.profile"
  local rc
  path_covered 2>"$SB/scan-err"; rc=$?
  chmod 644 "$HOME/.profile"
  assert_rc "an unreadable profile cannot vouch for coverage" "$rc" 1
  assert_eq "and is skipped in silence" "$(cat "$SB/scan-err" 2>/dev/null)" ""
}

t_entry_fish_creates_dir() {
  load; export KGAI_LOGIN_SHELL=/usr/bin/fish
  ensure_path_entry; assert_rc "fish gets its line" $? 0
  assert_file_has "config.fish holds fish syntax" \
    "$HOME/.config/fish/config.fish" "set -gx PATH"
}

t_entry_respects_user_bin_override() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  USER_BIN="$SB/custom/bin"
  ensure_path_entry
  assert_file_has "KGAI_USER_BIN is what gets exported" "$HOME/.bashrc" "$SB/custom/bin"
}

# ======================================================================================
# D. The launcher, and telling our own leftovers from a stranger's tool
# ======================================================================================

t_launcher_clean() {
  load
  write_launcher; assert_rc "a clean install writes the launcher" $? 0
  assert_exists "the launcher is there" "$KGAI_USER_BIN/kg"
  [ -x "$KGAI_USER_BIN/kg" ] || _fail "the launcher is not executable"
  assert_file_has "it identifies itself" "$KGAI_USER_BIN/kg" "kgai launcher"
  assert_no_match "no temp file is left behind" "$KGAI_USER_BIN/kg.new.*"
}

t_launcher_rerun_keeps_inode() {
  load
  write_launcher
  local before after
  before="$(ls -i "$KGAI_USER_BIN/kg" | awk '{print $1}')"
  write_launcher; assert_rc "a rerun succeeds" $? 0
  after="$(ls -i "$KGAI_USER_BIN/kg" | awk '{print $1}')"
  assert_eq "an unchanged launcher is not rewritten" "$after" "$before"
}

# The shape a pre-1.4.0 by-hand install leaves behind. It points at our own engine, but a
# grep for "kgai launcher" over a 14 MB binary does not match — and treating it as a
# stranger's tool is what made the installer back off permanently on upgraded machines.
t_launcher_adopts_symlink() {
  need_symlinks || return 0
  load
  mkdir -p "$KGAI_USER_BIN"; ln -s "$KGAI_HOME/bin/kg" "$KGAI_USER_BIN/kg"
  write_launcher; assert_rc "our own symlink is adopted" $? 0
  [ -L "$KGAI_USER_BIN/kg" ] && _fail "it is still a symlink"
  assert_file_has "it is the launcher now" "$KGAI_USER_BIN/kg" "kgai launcher"
  assert_file_has "the engine itself is untouched" "$KGAI_HOME/bin/kg" "fake engine"
}

t_launcher_adopts_relative_symlink() {
  need_symlinks || return 0
  load
  mkdir -p "$KGAI_USER_BIN"
  ln -s "../../.kgai/bin/kg" "$KGAI_USER_BIN/kg"
  write_launcher; assert_rc "a relative link into KGAI_HOME is adopted" $? 0
  assert_file_has "it is the launcher now" "$KGAI_USER_BIN/kg" "kgai launcher"
}

t_launcher_adopts_broken_symlink() {
  need_symlinks || return 0
  load
  mkdir -p "$KGAI_USER_BIN"; ln -s "$KGAI_HOME/bin/gone" "$KGAI_USER_BIN/kg"
  write_launcher; assert_rc "a dangling link into KGAI_HOME is adopted" $? 0
  assert_file_has "it is the launcher now" "$KGAI_USER_BIN/kg" "kgai launcher"
}

t_launcher_adopts_engine_copy() {
  load
  mkdir -p "$KGAI_USER_BIN"; cp "$KGAI_HOME/bin/kg" "$KGAI_USER_BIN/kg"
  write_launcher; assert_rc "a copy of our engine is adopted" $? 0
  assert_file_has "it is the launcher now" "$KGAI_USER_BIN/kg" "kgai launcher"
}

t_launcher_keeps_foreign() {
  load
  mkdir -p "$KGAI_USER_BIN"
  printf '#!/bin/sh\necho some other tool\n' > "$KGAI_USER_BIN/kg"
  chmod +x "$KGAI_USER_BIN/kg"
  write_launcher; assert_rc "a stranger's kg is refused" $? 2
  assert_file_has "and left exactly as it was" "$KGAI_USER_BIN/kg" "some other tool"
  assert_no_match "no temp file is left behind" "$KGAI_USER_BIN/kg.new.*"
}

# The real pre-1.4.0 shape is a RELATIVE, and often now DANGLING, link into $KGAI_HOME
# (`../../.kgai/bin/kg`, engine long since replaced). The byte-compare fallback cannot save
# this case — the target does not exist — so adoption here proves the `..` in the target is
# actually canonicalised, which the raw `$KGAI_HOME/*` glob never did (the branch was dead
# and every upgraded machine's own link was refused as a stranger's).
t_launcher_adopts_relative_dangling_symlink() {
  need_symlinks || return 0
  load
  rm -f "$KGAI_HOME/bin/kg"                          # no engine → no cmp fallback to mask it
  mkdir -p "$KGAI_USER_BIN"
  ln -s "../../.kgai/bin/kg" "$KGAI_USER_BIN/kg"
  write_launcher; assert_rc "a relative dangling link into KGAI_HOME is adopted" $? 0
  assert_file_has "it is the launcher now" "$KGAI_USER_BIN/kg" "kgai launcher"
}

# A link whose target only TEXTUALLY starts under $KGAI_HOME but escapes it via `..` is not
# ours — canonicalisation must see through the `..` and refuse it, leaving it untouched.
t_launcher_keeps_escaping_symlink() {
  need_symlinks || return 0
  load
  mkdir -p "$KGAI_USER_BIN" "$SB/outside"
  printf 'not ours\n' > "$SB/outside/victim"
  ln -s "$KGAI_HOME/../outside/victim" "$KGAI_USER_BIN/kg"
  write_launcher; assert_rc "a link escaping KGAI_HOME via .. is refused" $? 2
  [ -L "$KGAI_USER_BIN/kg" ] || _fail "the escaping link was replaced"
  assert_file_has "the victim file is untouched" "$SB/outside/victim" "not ours"
}

t_launcher_keeps_foreign_symlink() {
  need_symlinks || return 0
  load
  mkdir -p "$KGAI_USER_BIN" "$SB/otherpkg"
  printf 'not ours\n' > "$SB/otherpkg/kg"
  ln -s "$SB/otherpkg/kg" "$KGAI_USER_BIN/kg"
  write_launcher; assert_rc "a link to someone else's tool is refused" $? 2
  [ -L "$KGAI_USER_BIN/kg" ] || _fail "the link was replaced"
}

# The launcher is a shell script, and a script other users cannot READ is one they cannot
# run: mktemp creates the temp 0600, and a bare `chmod +x` published 0711 on a shared box.
t_launcher_is_world_runnable() {
  load
  write_launcher; assert_rc "the launcher is written" $? 0
  assert_mode "and is readable to every user who may execute it" \
    "$KGAI_USER_BIN/kg" "-rwxr-xr-x"
}

# KGAI_USER_BIN pointed at the engine's own directory makes the launcher's destination THE
# ENGINE — which the ownership checks all call "ours" (identical bytes), so the launcher
# overwrote the engine with a script that execs itself forever, and every session after
# that hung on the first kg call. It is a documented knob, so it will be set.
t_launcher_never_overwrites_engine() {
  load
  USER_BIN="$KGAI_HOME/bin"
  write_launcher; assert_rc "USER_BIN at the engine dir is a clean no-op" $? 0
  assert_file_has "the engine survives, byte for byte" "$KGAI_HOME/bin/kg" "fake engine"
  assert_file_hasnt "and is not the launcher" "$KGAI_HOME/bin/kg" "kgai launcher"
}

# Ownership is claimed by the launcher's opening line, not by the words appearing
# anywhere: a wrapper script that merely MENTIONS the kgai launcher is somebody's file.
t_launcher_keeps_mere_mention() {
  load
  mkdir -p "$KGAI_USER_BIN"
  printf '#!/bin/sh\n# wraps the kgai launcher, but is not it\necho wrapper\n' > "$KGAI_USER_BIN/kg"
  chmod +x "$KGAI_USER_BIN/kg"
  write_launcher; assert_rc "a mere mention is not ownership" $? 2
  assert_file_has "and the file is left alone" "$KGAI_USER_BIN/kg" "echo wrapper"
}

# A body write that FAILS (disk full, quota) must publish nothing: the old check only
# asked whether the temp existed, and a truncated launcher went into PATH executable.
t_launcher_failed_write_publishes_nothing() {
  load
  cat() { return 1; }                    # the body write fails, whatever the reason
  write_launcher; local rc=$?
  unset -f cat
  assert_rc "a failed body write is a write failure" "$rc" 1
  assert_absent "nothing was published" "$KGAI_USER_BIN/kg"
  assert_no_match "and no temp survives" "$KGAI_USER_BIN/kg.new.*"
}

t_launcher_unwritable_dir() {
  need_write_deny || return 0
  load
  mkdir -p "$SB/locked"; chmod 555 "$SB/locked"
  USER_BIN="$SB/locked/bin"
  write_launcher; assert_rc "an uncreatable bin dir is a write failure, not a refusal" $? 1
  chmod 755 "$SB/locked"
}

t_launcher_unwritable_file() {
  need_write_deny || return 0
  load
  mkdir -p "$KGAI_USER_BIN"; chmod 555 "$KGAI_USER_BIN"
  write_launcher; assert_rc "an unwritable bin dir is a write failure" $? 1
  chmod 755 "$KGAI_USER_BIN"
  assert_no_match "no temp file survives the failure" "$KGAI_USER_BIN/kg.new.*"
}

# ======================================================================================
# E. The two halves together — and what the user is told
# ======================================================================================

t_onpath_clean() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  ensure_on_path
  assert_file_has "the launcher exists" "$KGAI_USER_BIN/kg" "kgai launcher"
  assert_file_has "the profile line exists" "$HOME/.bashrc" "$KGAI_MARK"
  assert_has "the note reports success" "$PATH_NOTE" "is now on your PATH"
  assert_has "and hands over a line for the open terminal" "$PATH_NOTE" "export PATH="
}

# Cause 2: the launcher's outcome must not decide whether PATH gets wired.
t_onpath_foreign_kg_still_wires_path() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  mkdir -p "$KGAI_USER_BIN"
  printf '#!/bin/sh\necho some other tool\n' > "$KGAI_USER_BIN/kg"; chmod +x "$KGAI_USER_BIN/kg"
  ensure_on_path
  assert_file_has "the PATH line is written anyway" "$HOME/.bashrc" "$KGAI_MARK"
  assert_has "the note explains the collision" "$PATH_NOTE" "is not ours"
  assert_hasnt "and does not claim kg works" "$PATH_NOTE" "is now on your PATH"
}

t_onpath_our_symlink_still_wires_path() {
  need_symlinks || return 0
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  mkdir -p "$KGAI_USER_BIN"; ln -s "$KGAI_HOME/bin/kg" "$KGAI_USER_BIN/kg"
  ensure_on_path
  assert_file_has "the launcher replaced the symlink" "$KGAI_USER_BIN/kg" "kgai launcher"
  assert_file_has "and the PATH line is written" "$HOME/.bashrc" "$KGAI_MARK"
  assert_has "reported as success" "$PATH_NOTE" "is now on your PATH"
}

t_onpath_commented_mention() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  printf '# export PATH="$HOME/.local/bin:$PATH"\n' > "$HOME/.bashrc"
  ensure_on_path
  assert_file_has "a commented mention no longer suppresses the fix" \
    "$HOME/.bashrc" "$KGAI_MARK"
}

t_onpath_launcher_unwritable() {
  need_write_deny || return 0
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  mkdir -p "$KGAI_USER_BIN"; chmod 555 "$KGAI_USER_BIN"
  ensure_on_path
  chmod 755 "$KGAI_USER_BIN"
  assert_has "the failure is a warning, not a note" "$PATH_NOTE" "⚠"
  assert_has "it says what could not be done" "$PATH_NOTE" "could not write"
  assert_file_has "the PATH line is still written" "$HOME/.bashrc" "$KGAI_MARK"
}

# Never report success while `kg` is unreachable — the property whose absence made the
# whole bug silent for a release.
t_onpath_warns_when_still_unreachable() {
  load; host_os() { echo Linux; }
  fake_shell "/usr/bin:/bin"          # a shell that will never see $USER_BIN
  ensure_on_path
  assert_has "the note is a warning" "$PATH_NOTE" "⚠"
  assert_has "it says kg is not reachable" "$PATH_NOTE" "NOT on your terminal's PATH"
  assert_hasnt "it does not also claim success" "$PATH_NOTE" "is now on your PATH"
}

t_onpath_quiet_on_second_run() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  ensure_on_path
  ensure_on_path
  assert_eq "a settled machine says nothing" "$PATH_NOTE" ""
  assert_count "and the profile still has one block" "$HOME/.bashrc" "$KGAI_MARK" 1
}

t_onpath_probe_unavailable_is_not_a_warning() {
  load; host_os() { echo Linux; }
  export KGAI_LOGIN_SHELL="$SB/gone/bash"
  ensure_on_path
  assert_file_has "the line is still written" "$HOME/.bashrc" "$KGAI_MARK"
  assert_hasnt "not knowing is not a failure" "$PATH_NOTE" "⚠"
}

# When the line was appended but no probe could confirm it, the note must say exactly
# that — the probe's contract forbids dressing "could not find out" up as success, and
# the success wording is what it used to get.
t_onpath_append_unverifiable_is_neutral() {
  load; host_os() { echo Linux; }
  export KGAI_LOGIN_SHELL="$SB/unprobeable-shell"   # unknown shell: never probed
  ensure_on_path
  assert_file_has "the line is written" "$HOME/.profile" "$KGAI_MARK"
  assert_has "the note admits it could not verify" "$PATH_NOTE" "could not be verified"
  assert_hasnt "it does not claim new terminals have it" "$PATH_NOTE" "new terminals have it"
  assert_hasnt "and does not warn either" "$PATH_NOTE" "⚠"
}

# A profile that refuses the write is WARNED about, with the line handed over — the
# failure used to fold into "nothing to do" and the session said nothing at all, which is
# the exact silence the README promises the status line breaks.
t_onpath_readonly_profile_warns() {
  need_write_deny || return 0
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  : > "$HOME/.bashrc"; chmod 444 "$HOME/.bashrc"
  ensure_on_path 2>"$SB/on-err"
  chmod 644 "$HOME/.bashrc"
  assert_has "an unwritable profile is warned about" "$PATH_NOTE" "⚠"
  assert_has "naming what could not be done" "$PATH_NOTE" "could not add"
  assert_has "and handing over the line to add by hand" "$PATH_NOTE" "export PATH="
  assert_eq "with no raw bash error leaking to stderr" "$(cat "$SB/on-err" 2>/dev/null)" ""
}

# $KGAI_HOME so broken that even the lock cannot be created is the same class of failure:
# needed, not done, so it must be said.
t_onpath_unwritable_kgai_home_warns() {
  need_write_deny || return 0
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  chmod 555 "$KGAI_HOME"
  ensure_on_path
  chmod 755 "$KGAI_HOME"
  assert_has "an unwritable KGAI_HOME cannot fail in silence" "$PATH_NOTE" "⚠"
  assert_has "and still hands over the line" "$PATH_NOTE" "export PATH="
}

# The status is one LINE — that is the contract with SessionStart — and $USER_BIN is data
# that may legally contain a newline. It must be flattened at the note boundary.
t_note_is_one_line_despite_hostile_user_bin() {
  load; export KGAI_LOGIN_SHELL=/bin/bash; host_os() { echo Linux; }
  USER_BIN="$SB/two
lines/bin"
  mkdir -p "$USER_BIN"
  printf '#!/bin/sh\necho other tool\n' > "$USER_BIN/kg"; chmod +x "$USER_BIN/kg"
  ensure_on_path
  assert_has "the collision is still reported" "$PATH_NOTE" "not ours"
  assert_eq "in exactly one line" "$(printf '%s' "$PATH_NOTE" | wc -l | tr -d ' ')" "0"
}

# ======================================================================================
# F. End to end: a real shell, a real launcher
# ======================================================================================

# The whole point of the feature, checked the only way that means anything.
t_e2e_real_shell_resolves_kg() {
  load; real_bash
  ensure_on_path
  local resolved
  if terminal_is_login
  then resolved="$("$KGAI_LOGIN_SHELL" -lic 'command -v kg' 2>/dev/null </dev/null | tail -n1)"
  else resolved="$("$KGAI_LOGIN_SHELL" -ic  'command -v kg' 2>/dev/null </dev/null | tail -n1)"
  fi
  assert_eq "a fresh terminal resolves kg" "$resolved" "$KGAI_USER_BIN/kg"
  assert_hasnt "and the user was not warned" "$PATH_NOTE" "⚠"
}

t_e2e_commented_mention_resolves_kg() {
  load; real_bash
  printf '# uv installer added this once:\n# export PATH="$HOME/.local/bin:$PATH"\n' \
    > "$(rc_target)"
  ensure_on_path
  local resolved
  if terminal_is_login
  then resolved="$("$KGAI_LOGIN_SHELL" -lic 'command -v kg' 2>/dev/null </dev/null | tail -n1)"
  else resolved="$("$KGAI_LOGIN_SHELL" -ic  'command -v kg' 2>/dev/null </dev/null | tail -n1)"
  fi
  assert_eq "the reported bug, end to end" "$resolved" "$KGAI_USER_BIN/kg"
}

t_e2e_launcher_runs_the_engine() {
  load
  # A stand-in engine that proves the launcher execs it and forwards arguments.
  printf '#!/bin/sh\nprintf "ENGINE:%%s\\n" "$*"\n' > "$KGAI_HOME/bin/kg"
  chmod +x "$KGAI_HOME/bin/kg"
  write_launcher
  local out; out="$("$KGAI_USER_BIN/kg" ask "why" 2>&1)"
  assert_eq "arguments reach the engine unchanged" "$out" "ENGINE:ask why"
}

t_e2e_launcher_without_engine() {
  load
  write_launcher
  rm -f "$KGAI_HOME/bin/kg"
  local out rc
  out="$("$KGAI_USER_BIN/kg" version 2>&1)"; rc=$?
  assert_rc "a missing engine exits 127" "$rc" 127
  assert_has "and says where it looked" "$out" "$KGAI_HOME/bin/kg"
}

t_e2e_launcher_finds_engine_via_kgai_home() {
  load
  write_launcher
  local alt="$SB/elsewhere"
  mkdir -p "$alt/bin"
  printf '#!/bin/sh\necho MOVED\n' > "$alt/bin/kg"; chmod +x "$alt/bin/kg"
  local out; out="$(KGAI_HOME="$alt" "$KGAI_USER_BIN/kg" 2>&1)"
  assert_eq "the launcher honours KGAI_HOME" "$out" "MOVED"
}

# ======================================================================================
# G. The script itself
# ======================================================================================

t_script_parses() {
  bash -n "$REPO/scripts/install.sh" 2>"$SB/err" || _fail "install.sh does not parse"
  [ -s "$SB/err" ] && _fail "bash -n printed: $(cat "$SB/err")"
}

t_lib_mode_is_inert() {
  load
  # Sourcing must define functions and do nothing else the user would notice.
  assert_absent "no profile is touched on load" "$HOME/.bashrc"
  assert_absent "no launcher is written on load" "$KGAI_USER_BIN/kg"
  assert_absent "no store is created on load" "$HOME/.kgai/store"
  assert_eq "the note starts empty" "$PATH_NOTE" ""
}

t_probe_survives_a_noisy_profile() {
  load; real_bash; host_os() { echo Linux; }
  # A profile that prints banners, as Ubuntu's system one does.
  printf 'echo "=== MOTD ==="\nexport PATH="%s:$PATH"\necho "have a nice day"\n' \
    "$KGAI_USER_BIN" > "$HOME/.bashrc"
  path_reachable
  assert_rc "sentinels survive a chatty profile" $? 0
}

t_probe_reports_unreachable() {
  load; real_bash; host_os() { echo Linux; }
  printf 'echo hello\n' > "$HOME/.bashrc"
  path_reachable
  assert_rc "an absent dir is reported as absent" $? 1
}

t_probe_gives_up_cleanly() {
  load; export KGAI_LOGIN_SHELL="$SB/definitely-not-a-shell"
  path_reachable
  assert_rc "no shell to ask is its own answer" $? 2
}

# The probe EXECUTES the login shell, and its path is environment-derived — so a relative
# value must never be run as-is: it would resolve against the hook's cwd, the project
# directory, where anything checked out can name itself `bash`. The probe re-resolves the
# NAME on PATH instead.
t_probe_never_execs_relative_shell() {
  load; host_os() { echo Linux; }
  mkdir -p "$SB/proj/tools"
  printf '#!/bin/sh\n: > "%s/CANARY"\nprintf "%%s" "@KGPB@/x@KGPE@"\n' "$SB" \
    > "$SB/proj/tools/bash"
  chmod +x "$SB/proj/tools/bash"
  cd "$SB/proj"
  export KGAI_LOGIN_SHELL="tools/bash"
  probe_login_path >/dev/null 2>&1
  assert_absent "a relative login shell is never executed" "$SB/CANARY"
}

# And a shell that is not a KNOWN shell is not probed at all — "run it and see" is not an
# acceptable answer for an arbitrary absolute path the environment handed over.
t_probe_refuses_unknown_shell() {
  load; host_os() { echo Linux; }
  printf '#!/bin/sh\n: > "%s/CANARY"\n' "$SB" > "$SB/mystery-shell"
  chmod +x "$SB/mystery-shell"
  export KGAI_LOGIN_SHELL="$SB/mystery-shell"
  path_reachable
  assert_rc "an unknown shell means cannot-probe" $? 2
  assert_absent "and it was never executed" "$SB/CANARY"
}

# A strict POSIX `sleep` rejects 0.1 — instantly, exit non-zero — and polling with it
# would burn the probe's whole budget in milliseconds, killing shells that were about to
# answer. The tick has to degrade to whole seconds, not to zero.
t_probe_ticks_survive_posix_sleep() {
  load_with_strict_sleep() {
    mkdir -p "$SB/strictbin"
    cat > "$SB/strictbin/sleep" <<'S'
#!/bin/sh
case "$1" in *[!0-9]*) echo "sleep: invalid time interval" >&2; exit 1 ;; esac
exec /bin/sleep "$1"
S
    chmod +x "$SB/strictbin/sleep"
    PATH="$SB/strictbin:$PATH"
    load
  }
  load_with_strict_sleep; host_os() { echo Linux; }
  # A shell slow enough that a burnt budget kills it mid-answer, fast enough for a suite.
  mkdir -p "$SB/fakebin"
  printf '#!/bin/sh\nsleep 1\nprintf "%%s" "@KGPB@%s@KGPE@"\n' \
    "/usr/bin:$KGAI_USER_BIN:/bin" > "$SB/fakebin/bash"
  chmod +x "$SB/fakebin/bash"
  export KGAI_LOGIN_SHELL="$SB/fakebin/bash"
  path_reachable
  assert_rc "a slow shell still gets its say under a strict sleep" $? 0
}

# A profile that `exec`s something else entirely (a tmux, a different shell) swallows the
# probe's snippet: no sentinels can ever arrive. That must be a clean "could not find
# out" within the budget, not a hang and not a verdict.
t_probe_survives_exec_profile() {
  export KGAI_PROBE_TIMEOUT=2
  load; real_bash; host_os() { echo Linux; }
  printf 'exec sleep 30\n' > "$HOME/.bashrc"
  path_reachable
  assert_rc "a profile that execs away is not a verdict" $? 2
}

# ======================================================================================

suite_header 'kgai — install.sh: PATH wiring'

section 'reading a profile'
run 'empty profile is not coverage'                  t_scan_empty
run 'a live export counts'                           t_scan_plain_export
run 'a commented-out mention does not'               t_scan_commented_out
run '#export without a space does not'               t_scan_commented_no_space
run 'an indented comment does not'                   t_scan_commented_indented
run "pip's not-on-PATH warning does not"             t_scan_warning_note
run 'a trailing comment does not hide a live line'   t_scan_trailing_comment
run 'the ~/ spelling counts'                         t_scan_tilde
run 'the ${HOME}/ spelling counts'                   t_scan_home_braces
run 'the expanded spelling counts'                   t_scan_absolute
run 'a system path file counts'                      t_scan_system_file
run 'a commented system entry does not'              t_scan_system_file_commented
run "another shell's rc does not count"              t_scan_other_shells_rc
run 'a file without a trailing newline is read'      t_scan_no_trailing_newline
run 'a home directory with a space works'            t_scan_home_with_space
run 'USER_BIN outside HOME matches exactly'          t_scan_user_bin_outside_home
run 'an unreadable profile is skipped in silence'    t_scan_unreadable_file_is_quiet

section 'choosing the profile to write'
run 'zsh → .zshrc'                                   t_target_zsh
run 'macOS bash, nothing there → .bash_profile'      t_target_bash_darwin_fresh
run 'macOS bash, only .profile → .profile'           t_target_bash_darwin_only_profile
run 'macOS bash, both → .bash_profile'               t_target_bash_darwin_existing
run 'Linux bash → .bashrc'                           t_target_bash_linux
run 'Git Bash → login shell, .bash_profile'          t_target_bash_gitbash
run 'fish → config.fish, fish syntax'                t_target_fish
run 'unknown shell → .profile'                       t_target_unknown_shell
run 'KGAI_LOGIN_SHELL overrides the passwd entry'    t_login_shell_override

section 'writing the PATH entry'
run 'a clean home gets the line'                     t_entry_clean
run 'running it three times adds one block'          t_entry_idempotent
run 'a commented mention does not block it'          t_entry_commented_mention
run 'genuine coverage is left alone and stamped'     t_entry_genuinely_covered
run 'a stamp skips the probe while covered'          t_entry_stamp_skips_probe_when_covered
run 'a stamp does not survive lost coverage'         t_entry_stamp_does_not_survive_lost_coverage
run 'a stamp for another dir does not count'         t_entry_stamp_other_dir
run 'a probe overrules a mention that never fires'   t_entry_conditional_mention
run 'with no probe, the scan is trusted'             t_entry_probe_unavailable
run 'an unparseable probe is not a verdict'          t_entry_probe_says_nothing
run 'an unwritable profile is a write failure'       t_entry_unwritable_rc
run 'fish gets its directory created'                t_entry_fish_creates_dir
run 'KGAI_USER_BIN is honoured'                      t_entry_respects_user_bin_override

section 'the launcher'
run 'a clean install writes it'                      t_launcher_clean
run 'a rerun does not rewrite it'                    t_launcher_rerun_keeps_inode
run 'our own symlink is adopted'                     t_launcher_adopts_symlink
run 'a relative link into KGAI_HOME is adopted'      t_launcher_adopts_relative_symlink
run 'a relative DANGLING link is adopted'            t_launcher_adopts_relative_dangling_symlink
run 'a dangling link into KGAI_HOME is adopted'      t_launcher_adopts_broken_symlink
run 'a link escaping KGAI_HOME is refused'           t_launcher_keeps_escaping_symlink
run 'a copy of the engine is adopted'                t_launcher_adopts_engine_copy
run "a stranger's kg is left alone"                  t_launcher_keeps_foreign
run "a link to someone else's tool is left alone"    t_launcher_keeps_foreign_symlink
run 'an uncreatable bin dir is a write failure'      t_launcher_unwritable_dir
run 'an unwritable bin dir leaves no temp file'      t_launcher_unwritable_file
run 'the launcher is readable by every user'         t_launcher_is_world_runnable
run 'USER_BIN at the engine dir cannot eat the engine' t_launcher_never_overwrites_engine
run 'a mere mention of the launcher is not ownership' t_launcher_keeps_mere_mention
run 'a failed body write publishes nothing'          t_launcher_failed_write_publishes_nothing

section 'both halves, and what the user is told'
run 'a clean run wires and reports both'             t_onpath_clean
run "a stranger's kg does not cancel the PATH fix"   t_onpath_foreign_kg_still_wires_path
run 'our old symlink does not cancel it either'      t_onpath_our_symlink_still_wires_path
run 'a commented mention does not cancel it'         t_onpath_commented_mention
run 'an unwritable launcher warns and still wires'   t_onpath_launcher_unwritable
run 'unreachable PATH is a warning, not "ready"'     t_onpath_warns_when_still_unreachable
run 'a settled machine says nothing'                 t_onpath_quiet_on_second_run
run 'not being able to check is not a warning'       t_onpath_probe_unavailable_is_not_a_warning
run 'an unverifiable append is reported as exactly that' t_onpath_append_unverifiable_is_neutral
run 'a read-only profile is warned about, with the line' t_onpath_readonly_profile_warns
run 'an unwritable KGAI_HOME is warned about'        t_onpath_unwritable_kgai_home_warns
run 'the note stays one line whatever USER_BIN holds' t_note_is_one_line_despite_hostile_user_bin

section 'end to end'
run 'a real shell resolves kg'                       t_e2e_real_shell_resolves_kg
run 'the reported bug, end to end'                   t_e2e_commented_mention_resolves_kg
run 'the launcher runs the engine with its args'     t_e2e_launcher_runs_the_engine
run 'without an engine it exits 127'                 t_e2e_launcher_without_engine
run 'the launcher honours KGAI_HOME'                 t_e2e_launcher_finds_engine_via_kgai_home

section 'the script itself'
run 'install.sh parses'                              t_script_parses
run 'sourcing it changes nothing'                    t_lib_mode_is_inert
run 'the probe survives a chatty profile'            t_probe_survives_a_noisy_profile
run 'the probe reports a missing dir'                t_probe_reports_unreachable
run 'the probe gives up cleanly'                     t_probe_gives_up_cleanly
run 'a relative login shell is never executed'       t_probe_never_execs_relative_shell
run 'an unknown shell is refused, not run'           t_probe_refuses_unknown_shell
run 'a strict POSIX sleep does not burn the budget'  t_probe_ticks_survive_posix_sleep
run 'a profile that execs away is not a verdict'     t_probe_survives_exec_profile

summary
