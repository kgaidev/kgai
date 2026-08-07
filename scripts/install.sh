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

# Everything below hangs off $HOME — the install home, the user bin, every profile. With
# HOME unset, `set -u` would kill the hook with a bare `HOME: unbound variable`, the one
# exit in this script that does not say kgai. Say it properly and stand down instead.
if [ -z "${HOME:-}" ]; then
  echo "kgai: ⚠️ HOME is not set — cannot locate the install home (~/.kgai). Set HOME and start a new session."
  return 0 2>/dev/null || exit 0
fi

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
# The ~/.kgai skeleton is created down in the script body, AFTER the library guard and the
# platform refusal — sourcing this file must change nothing, and a refused platform must
# not be left with an empty install home it was just told it cannot use.

status() { echo "kgai: $*"; }

# Sub-second polling where this host's sleep takes fractions (GNU and macOS: yes; a strict
# POSIX sleep: no). Detected once, because ticking with a sleep that REJECTS 0.1 — it
# exits non-zero instantly — would burn a whole polling budget in milliseconds, and every
# probe below would then report "could not find out" with nothing ever said about it.
if sleep 0.1 2>/dev/null; then TICK=0.1; TICKS_PER_SEC=10; else TICK=1; TICKS_PER_SEC=1; fi

# Both caps exist so the tests are not the first place a wedged process is discovered.
# The probe asks one interactive shell for its PATH; the engine cap covers every `kg`
# call the hook makes — the hook's own budget is 180s (hooks/hooks.json), and one hung
# engine used to stall session start for all of it.
PROBE_TIMEOUT="${KGAI_PROBE_TIMEOUT:-10}"
ENGINE_TIMEOUT="${KGAI_ENGINE_TIMEOUT:-60}"

# Wait out a backgrounded pid, killing it once its budget (seconds) is spent. The exit
# status is the child's own, or 137 after a kill.
reap_within() { # <pid> <seconds>
  local pid="$1" budget=$(($2 * TICKS_PER_SEC)) waited=0
  while kill -0 "$pid" 2>/dev/null; do
    [ "$waited" -ge "$budget" ] && { kill -9 "$pid" 2>/dev/null; break; }
    sleep "$TICK"; waited=$((waited + 1))
  done
  wait "$pid" 2>/dev/null
}

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
PATH_OK_STAMP="$KGAI_HOME/.path-ok"

# Wrapped so the tests can pretend to be another OS. Every LIBRARY-mode platform decision
# goes through it, so sourcing tests can override the function. The script body below the
# library guard (asset selection, the Windows refusal, the rpath flavour) calls `uname`
# directly — those branches run as a child process, where the tests shadow `uname` on PATH
# instead; the two seams cover disjoint code and cannot disagree on one decision.
host_os() { uname -s; }

# Does this platform's terminal open a LOGIN shell? A macOS Terminal tab and a Git Bash
# window do; a Linux terminal window does not. bash reads a completely different profile
# in each case (.bash_profile vs .bashrc), so this one answer decides both where the PATH
# line goes and how the verification probe has to start its shell.
terminal_is_login() {
  case "$(host_os)" in Darwin|MINGW*|MSYS*|CYGWIN*) return 0 ;; *) return 1 ;; esac
}

# Collapse . and .. in an absolute path textually — no filesystem access, so it also works
# for a symlink whose target does not exist (which `realpath -e` / `readlink -f` cannot,
# and which macOS's userland lacks a portable flag for anyway). It does not resolve
# intermediate symlinks; that is fine here, because the only use is deciding whether a path
# is inside $KGAI_HOME and both sides are normalised the same way. `set -f` while splitting
# on `/` so a segment that is a glob character is not expanded against the cwd.
lexical_abs() {
  case "$1" in /*) ;; *) return 1 ;; esac
  local part out="" had_f=0
  case "$-" in *f*) had_f=1 ;; esac
  set -f
  local IFS=/
  for part in $1; do
    case "$part" in
      ''|.) ;;
      ..)   out="${out%/*}" ;;
      *)    out="$out/$part" ;;
    esac
  done
  [ "$had_f" = 1 ] || set +f
  printf '%s\n' "${out:-/}"
}

# Is the file at $1 something WE put there? The launcher script is only the newest of
# three spellings — recognising the older two is what stops the installer from mistaking
# its own leftovers for a stranger's tool and backing off permanently:
#   * the launcher script (every version since v1.4.0),
#   * a symlink into $KGAI_HOME — what a pre-1.4.0 by-hand install left behind,
#   * a byte-for-byte copy of the engine — what `cp ~/.kgai/bin/kg ~/.local/bin/` leaves.
ours_already() {
  local p="$1" target home
  # Anchored to the line the launcher has opened with since v1.4.0. A bare substring match
  # adopted (and overwrote) any file that merely MENTIONED "kgai launcher" somewhere.
  grep -q '^# kgai launcher' "$p" 2>/dev/null && return 0
  if [ -L "$p" ]; then
    target="$(readlink "$p" 2>/dev/null)"
    # A relative link resolves against the directory the link lives in, not $PWD. Then
    # canonicalise both sides: a real pre-1.4.0 link is `../../.kgai/bin/kg`, whose raw
    # form is littered with `..` and never matched a plain `$KGAI_HOME/*` glob — so the
    # whole symlink branch was dead and every upgraded machine's own link was refused as a
    # stranger's tool. Canonicalising also stops a crafted `$KGAI_HOME/../outside` link
    # from being misread as ours.
    case "$target" in ""|/*) ;; *) target="$(dirname "$p")/$target" ;; esac
    if [ -n "$target" ]; then
      target="$(lexical_abs "$target" 2>/dev/null || printf '%s' "$target")"
      home="$(lexical_abs "$KGAI_HOME" 2>/dev/null || printf '%s' "$KGAI_HOME")"
      case "$target" in "$home"/*) return 0 ;; esac
    fi
  fi
  [ -f "$BIN" ] && [ -f "$p" ] && cmp -s "$p" "$BIN" && return 0
  return 1
}

write_launcher() {
  local dest="$USER_BIN/kg" tmp dabs babs
  # KGAI_USER_BIN pointed at the engine's own directory makes $dest THE ENGINE. Every
  # ownership test below then answers "ours" (identical bytes), and the launcher would
  # overwrite the engine with a script that execs itself forever — after which every
  # session start hung on the first `kg` call. The engine already IS runnable at that
  # path, so there is nothing for a launcher to add: succeed by doing nothing.
  dabs="$(lexical_abs "$dest" 2>/dev/null)" || dabs=""
  babs="$(lexical_abs "$BIN" 2>/dev/null)" || babs=""
  if [ -n "$dabs" ] && [ "$dabs" = "$babs" ]; then return 0; fi
  mkdir -p "$USER_BIN" 2>/dev/null || return 1
  # A fresh unique temp via mktemp, not a predictable "$dest.new.$$": two sessions starting
  # together must not write the same temp (one could rename a half-written file into
  # place), and an unpredictable name also cannot be pre-planted as a symlink for the `cat`
  # below to follow out of $USER_BIN.
  tmp="$(mktemp "$USER_BIN/kg.new.XXXXXX" 2>/dev/null)" || return 1
  # Never clobber a different tool's binary that happens to be called kg. Distinct from
  # a write failure (return 1) so the status line can say which one actually happened.
  # -e is false for a BROKEN symlink, so test the link itself too: a dangling link into
  # $KGAI_HOME is ours to replace, and a dangling link anywhere else is still not ours.
  if { [ -e "$dest" ] || [ -L "$dest" ]; } && ! ours_already "$dest"; then
    rm -f "$tmp" 2>/dev/null
    return 2
  fi
  # A launcher script, not a symlink: the macOS rpath is @loader_path/../lib, resolved
  # against the path the binary was started from. Through a symlink in ~/.local/bin that
  # would look for the native lib in ~/.local/lib and fail to load.
  # `cat`'s own exit status is checked below — a short write (disk full, a quota) used to
  # pass the old "does the temp exist" test and get published as a corrupt executable.
  if ! cat 2>/dev/null > "$tmp" <<'LAUNCHER'
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
  then rm -f "$tmp" 2>/dev/null; return 1; fi
  # Already exactly this launcher: leave the inode alone. A SYMLINK never counts as
  # already-correct even when it resolves to the same bytes — it is the pre-1.4.0 shape
  # we are here to replace, and following it would rewrite the engine in $KGAI_HOME.
  if [ -f "$dest" ] && [ ! -L "$dest" ] && cmp -s "$tmp" "$dest"; then
    rm -f "$tmp"; return 0
  fi
  # An explicit 755, not `chmod +x`: mktemp creates 0600, and +x under the usual umask
  # yields 0711 — a shell script other users of a shared machine can execute but not READ,
  # which is to say not run at all.
  chmod 755 "$tmp" 2>/dev/null && mv "$tmp" "$dest" 2>/dev/null ||
    { rm -f "$tmp" 2>/dev/null; return 1; }
}

# The shell that OWNS the user's terminals. $SHELL is merely whatever spawned this hook:
# Claude Code can be started from a bash session, from a GUI app, or from a launcher that
# sets it to something else entirely, so on a Mac whose Terminal windows are zsh it often
# reads /bin/bash — and the PATH line then lands in .bash_profile, which zsh never reads.
# The passwd entry is what Terminal actually opens, so it wins where it can be read.
login_shell() {
  # An explicit override wins: a passwd entry can be stale or point at a shell the user
  # abandoned, and this is the only knob that fixes that without editing system records.
  if [ -n "${KGAI_LOGIN_SHELL:-}" ]; then printf '%s\n' "$KGAI_LOGIN_SHELL"; return; fi
  local s=""
  case "$(host_os)" in
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

# Every file that could plausibly put something on a fresh terminal's PATH. This is the
# SEARCH list — deliberately wider than the one file we would write to, because the line
# we are looking for may well be somewhere we would never have put it (a .bash_profile
# that sources .bashrc is the usual shape).
rc_files() {
  case "$(shell_family)" in
    zsh)  printf '%s\n' "$HOME/.zshrc" "$HOME/.zprofile" "$HOME/.zshenv" "$HOME/.zlogin" ;;
    bash) printf '%s\n' "$HOME/.bash_profile" "$HOME/.bashrc" "$HOME/.bash_login" "$HOME/.profile" ;;
    fish) printf '%s\n' "$HOME/.config/fish/config.fish" "$HOME/.config/fish/conf.d"/*.fish ;;
    *)    printf '%s\n' "$HOME/.profile" ;;
  esac
}

# System-wide files, read before any of the user's own. A function so sourcing tests can
# point it somewhere harmless instead of the real /etc — and an env override
# (colon-separated) so script-mode test runs are sandboxed the same way; without it, a
# host /etc entry that happens to mention the user bin silently changed which branch a
# flow test exercised.
system_path_files() {
  if [ -n "${KGAI_SYSTEM_PATH_FILES+x}" ]; then
    local part had_f=0
    case "$-" in *f*) had_f=1 ;; esac
    set -f
    local IFS=:
    for part in $KGAI_SYSTEM_PATH_FILES; do
      [ -n "$part" ] && printf '%s\n' "$part"
    done
    [ "$had_f" = 1 ] || set +f
    return 0
  fi
  printf '%s\n' /etc/paths /etc/paths.d/* /etc/profile /etc/zprofile \
                /etc/zshrc /etc/profile.d/* /etc/environment
}

# Where the line GOES, which is not the same question as where it might already be. In a
# login shell bash reads exactly ONE of .bash_profile / .bash_login / .profile — the first
# that exists — so creating .bash_profile on a machine that only has .profile stops
# .profile from ever being read again. Prefer the file the shell already reads; create the
# conventional one only when there is none.
rc_target_candidates() {
  case "$(shell_family)" in
    zsh)  printf '%s\n' "$HOME/.zshrc" ;;
    fish) printf '%s\n' "$HOME/.config/fish/config.fish" ;;
    bash) if terminal_is_login
          then printf '%s\n' "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"
          else printf '%s\n' "$HOME/.bashrc"; fi ;;
    *)    printf '%s\n' "$HOME/.profile" ;;
  esac
}

rc_target() {
  local f first=""
  while IFS= read -r f; do
    [ -n "$first" ] || first="$f"
    [ -f "$f" ] && { printf '%s\n' "$f"; return; }
  done < <(rc_target_candidates)
  printf '%s\n' "$first"
}

# A directory name is DATA; a shell profile is CODE that runs at every login. So the path
# is written as a quoted literal, never interpolated bare. Unquoted, a directory with a
# space in it silently produced two broken PATH entries (fish took the space as a list
# separator), and one containing a quote turned the rest of the name into commands the
# user's shell then executed on every new terminal — reproduced before this was fixed.
# $USER_BIN comes from $HOME or KGAI_USER_BIN, so this is about not mangling odd but legal
# directory names rather than about a remote attacker; the profile is the last file in
# which to take that chance.
#
# Built one character at a time on purpose. The obvious `${s//\'/…}` form expands the
# backslashes in its replacement DIFFERENTLY on bash 3.2 (which is /bin/bash on every Mac,
# the platform this file targets) than on bash 4+/5: the 3.2 result was itself a syntax
# error that broke the very login it was meant to protect. A character loop touches no
# version-dependent substitution and is byte-identical on 3.2 and 5.x (verified).
squote() { # POSIX single-quoted literal for bash/zsh/sh: ' becomes '\''
  local s="$1" out="'" i=0 c
  while [ "$i" -lt "${#s}" ]; do
    c="${s:$i:1}"
    if [ "$c" = "'" ]; then out="$out'\\''"; else out="$out$c"; fi
    i=$((i + 1))
  done
  printf "%s'" "$out"
}

fquote() { # fish single-quoted literal: only \ and ' are special inside ''
  local s="$1" out="'" i=0 c
  while [ "$i" -lt "${#s}" ]; do
    c="${s:$i:1}"
    case "$c" in
      '\') out="$out\\\\" ;;   # a literal backslash → \\
      "'") out="$out\\'"  ;;   # a literal quote → \'
      *)   out="$out$c"   ;;
    esac
    i=$((i + 1))
  done
  printf "%s'" "$out"
}

# The line itself, in the syntax of the shell that will read it.
path_line() {
  case "$(shell_family)" in
    fish) printf '%s\n' "set -gx PATH $(fquote "$USER_BIN") \$PATH" ;;
    *)    printf '%s\n' "export PATH=$(squote "$USER_BIN"):\"\$PATH\"" ;;
  esac
}

# A newline cannot be expressed in a one-line PATH entry at all, so such a directory is
# refused rather than written as two lines of something else.
path_line_possible() {
  case "$USER_BIN" in *"
"*) return 1 ;; esac
  return 0
}

# Does this ONE file put $USER_BIN on PATH? Only uncommented text counts. The old check
# was a plain grep for the directory anywhere in the file, so a COMMENTED mention — which
# uv, pipx and pip all leave behind, often as a "# WARNING: … is not on PATH" note — read
# as "already handled". The profile line was then never written, PATH_NOTE stayed empty,
# and the installer reported `engine ready` while `kg` was unreachable from any terminal.
mentions_user_bin() {
  local f="$1" line rel="${USER_BIN#"$HOME"/}" lit="$USER_BIN" hvar hbrace tilde
  hvar='$HOME'"/$rel"; hbrace='${HOME}'"/$rel"; tilde="~/$rel"
  # `|| [ -n "$line" ]` so a last line without a trailing newline is still examined.
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%%#*}"          # everything from the first # is a comment
    [ -n "$line" ] || continue
    case "$line" in *"$lit"*) return 0 ;; esac
    [ "$rel" = "$lit" ] && continue   # $USER_BIN lives outside $HOME — no short spellings
    case "$line" in *"$hvar"*|*"$hbrace"*|*"$tilde"*) return 0 ;; esac
  done < "$f"
  return 1
}

# Will a NEW terminal find $USER_BIN? This process's own PATH cannot answer that: Claude
# Code hands hooks an environment of its own making — on macOS a GUI-launched app inherits
# launchd's PATH, and the CLI's installer puts `claude` in ~/.local/bin and exports it for
# its children. Either way $USER_BIN can be on PATH here while the user's Terminal has
# never heard of it, and the old check then skipped the profile line — the exact reason
# `kg` stayed command-not-found on a Mac. So read the files that build a terminal's PATH:
# the system-wide configuration plus the login shell's own profiles.
path_covered() {
  local f
  # Read the candidates line by line rather than splitting them on whitespace — a home
  # directory with a space in it is legal on macOS.
  while IFS= read -r f; do
    [ -f "$f" ] || continue
    # Readable is part of the test: a root-owned 600 file under /etc/profile.d is routine
    # on a managed machine, and `read`ing it leaked a Permission denied onto stderr at
    # every session start. An unreadable file cannot vouch for coverage either way.
    [ -r "$f" ] || continue
    mentions_user_bin "$f" && return 0
  done < <(system_path_files; rc_files)
  return 1
}

# Have we already added our line to a file this shell reads? Checked before anything else,
# so a second session neither probes nor appends a duplicate.
#
# Deliberate tradeoff: once the mark exists anywhere in the family's files, no session
# re-probes reachability — so wiring that breaks LATER (say, a new .bash_profile that
# shadows the .profile holding the mark) stays silent. The alternative is a ~10s
# login-shell probe at every session start for everyone; removing the marked line re-arms
# the check. Documented in the README's PATH section.
marked_already() {
  local f
  while IFS= read -r f; do
    [ -f "$f" ] && grep -qF "$KGAI_MARK" "$f" 2>/dev/null && return 0
  done < <(rc_files)
  return 1
}

# Reading the files can only tell you what they SAY. Running one tells you what a terminal
# actually ends up with — a mention inside a branch that never fires, or one a later
# `export PATH=…` overwrites, is indistinguishable from a working entry until you ask.
# Prints the resulting PATH; returns 2 when it could not be obtained at all.
#
# The interactive flag matters as much as the login flag: on Linux the profile we write is
# .bashrc, which a LOGIN shell never reads, so probing with -l alone would report correct
# wiring as broken. Started with stdin on /dev/null and both sentinels around the value,
# because a system profile is free to print whatever it likes (Ubuntu's prints a sudo
# hint), and killed after ten seconds so a pathological rc cannot hang the session.
probe_login_path() {
  local sh base snippet tmp out
  # The probe EXECUTES this path, and login_shell's answer is environment-derived
  # (KGAI_LOGIN_SHELL, $SHELL) — so it is gated: only a known shell is run, and only from
  # an absolute path. A relative path would resolve against the hook's cwd (the project —
  # anything checked out there could name itself `bash`), so anything not absolute is
  # re-resolved by NAME on PATH. An unknown shell means "cannot probe" (rc 2), never
  # "run it and see".
  sh="$(login_shell)"
  base="$(basename "$sh")"
  case "$base" in bash|zsh|fish|sh|dash|ash|ksh) ;; *) return 2 ;; esac
  case "$sh" in /*) [ -x "$sh" ] || sh="" ;; *) sh="" ;; esac
  [ -n "$sh" ] || sh="$(command -v "$base" 2>/dev/null || true)"
  case "$sh" in /*) [ -x "$sh" ] || return 2 ;; *) return 2 ;; esac
  case "$(shell_family)" in
    # fish keeps PATH as a list, not a colon-joined string.
    fish) snippet='printf "%s" "@KGPB@"(string join : $PATH)"@KGPE@"' ;;
    *)    snippet='printf "%s" "@KGPB@$PATH@KGPE@"' ;;
  esac
  tmp="$(mktemp "${TMPDIR:-/tmp}/kgai-path.XXXXXX" 2>/dev/null)" || return 2
  if terminal_is_login; then
    "$sh" -lic "$snippet" >"$tmp" 2>/dev/null </dev/null &
  else
    "$sh" -ic "$snippet" >"$tmp" 2>/dev/null </dev/null &
  fi
  reap_within $! "$PROBE_TIMEOUT"
  out="$(tr -d '\n' < "$tmp" 2>/dev/null)"
  rm -f "$tmp"
  case "$out" in *"@KGPB@"*"@KGPE@"*) ;; *) return 2 ;; esac
  out="${out#*@KGPB@}"; out="${out%%@KGPE@*}"
  [ -n "$out" ] || return 2
  printf '%s\n' "$out"
}

# 0 = a real terminal has $USER_BIN on PATH, 1 = it does not, 2 = could not find out.
# The three-way answer is the point: "could not find out" must never be reported to the
# user as either success or failure.
path_reachable() {
  local p
  p="$(probe_login_path)" || return 2
  case ":$p:" in *":$USER_BIN:"*|*":$USER_BIN/:"*) return 0 ;; esac
  return 1
}

# This script runs at EVERY SessionStart, so two Claude Code windows opened together used
# to reach the append at the same time and each add its own block — six parallel sessions
# produced six copies of the same two lines in the profile, and nothing ever removed them.
# mkdir is the portable atomic test-and-set that stops that.
#
# A session killed mid-write would otherwise leave the lock behind and block the fix
# forever, so a lock whose owning process is gone is reclaimed. The reclaim must itself be
# race-free: a plain "rmdir the stale one, then mkdir mine" lets two sessions both rmdir
# and both mkdir, and the second deletes the first's fresh lock — the exact duplicate-block
# outcome the lock exists to prevent. So each session writes its own pid into the lock and,
# after any reclaim, re-reads it: only the session whose pid is actually in the dir holds
# it. `find -mmin +1` matches an mtime strictly older than a minute (so up to ~2 minutes in
# practice), comfortably longer than the ≤10s a writer can hold it.
RC_LOCK="$KGAI_HOME/.rc.lock"

_rc_lock_owns() { [ "$(cat "$RC_LOCK/pid" 2>/dev/null)" = "$$" ]; }

# Is the lock abandoned? Three answers feed it, in order of authority:
#   * a recorded owner that is still ALIVE means never stale — mtime alone used to be the
#     whole test, and a skewed clock (a network home directory) could then break a live
#     lock, which is the duplicate-block bug through the back door;
#   * an mtime strictly older than a minute (so up to ~2 in practice — a writer holds the
#     lock well under a second) means abandoned;
#   * with no `find` to ask about age, a recorded owner that is provably GONE is enough —
#     a lock with no pid at all stays untouchable then, which only errs toward deferring.
_rc_lock_stale() {
  local pid
  pid="$(cat "$RC_LOCK/pid" 2>/dev/null)"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then return 1; fi
  [ -n "$(find "$RC_LOCK" -maxdepth 0 -mmin +1 2>/dev/null)" ] && return 0
  if ! command -v find >/dev/null 2>&1 && [ -n "$pid" ]; then return 0; fi
  return 1
}

# `mkdir` is the sole arbiter: exactly one racing session creates the lock dir, and that
# session records its pid inside. Acquiring is uncontended in the normal case.
#
# The hard part is a lock left behind by a session that was killed while holding it — an
# abandoned lock must be reclaimed, or the PATH line is never written on that machine
# again. The reclaim has to be race-free: the naive "if stale: rmdir; mkdir mine" (and
# even an atomic rename) lets a second reclaimer act on the FRESH lock a first reclaimer
# just created, because each judged staleness on the OLD lock but acts on whatever sits
# there now — the reproduced duplicate-block bug. So a separate break-lock serialises the
# reclaimers: only the one session that creates `.rc.lock.break` removes the stale lock,
# re-checking under that exclusive hold that it is still the same stale lock. Once it is
# gone, every session converges on the single atomic `mkdir` again, where only one wins.
#
# Returns 0 holding the lock, 1 when a live session holds it (defer quietly — that session
# does the work and reports it), 2 when the lock cannot even be CREATED (an unwritable
# $KGAI_HOME) — a failure the caller must say out loud, because nothing else will.
rc_lock_acquire() {
  mkdir -p "$KGAI_HOME" 2>/dev/null
  mkdir "$RC_LOCK" 2>/dev/null && { printf '%s\n' "$$" 2>/dev/null > "$RC_LOCK/pid"; return 0; }
  # The break-lock gets the same staleness reclaim as the lock it guards: a reclaimer
  # killed between its mkdir and rmdir used to leave `.rc.lock.break` behind forever, and
  # with a stale lock also present no session could ever break it again — PATH wiring
  # permanently and silently off on that machine, one level down from the lock it fixes.
  if [ -d "$RC_LOCK.break" ] &&
     [ -n "$(find "$RC_LOCK.break" -maxdepth 0 -mmin +1 2>/dev/null)" ]; then
    rm -rf "$RC_LOCK.break" 2>/dev/null
  fi
  if _rc_lock_stale && mkdir "$RC_LOCK.break" 2>/dev/null; then
    # Sole breaker. The stale lock cannot have been refreshed — it still exists, so every
    # other session's mkdir is failing — so removing it now is safe.
    _rc_lock_stale && rm -rf "$RC_LOCK" 2>/dev/null
    rmdir "$RC_LOCK.break" 2>/dev/null
  fi
  # One clean attempt at the freed name; whoever loses this atomic mkdir defers.
  mkdir "$RC_LOCK" 2>/dev/null && { printf '%s\n' "$$" 2>/dev/null > "$RC_LOCK/pid"; return 0; }
  [ -d "$RC_LOCK" ] && return 1
  return 2
}

# Only the recorded owner releases, so a session that lost the race can never delete the
# winner's live lock.
rc_lock_release() {
  _rc_lock_owns || return 0
  rm -f "$RC_LOCK/pid" 2>/dev/null
  rmdir "$RC_LOCK" 2>/dev/null || true
}

# Appends one marked PATH line to the file the user's terminal reads. Returns 0 only when
# it actually added it (so the status line mentions it exactly once), 1 when there was
# nothing to do, and 2 when the line is NEEDED and could not be written — the caller must
# report that, because "could not write" folded into "nothing to do" is how this failure
# stayed silent for a release.
ensure_path_entry() {
  PATH_RC=""
  marked_already && return 1
  path_line_possible || return 1
  local lrc
  rc_lock_acquire; lrc=$?
  # An uncreatable lock is a write failure (unwritable $KGAI_HOME), not contention.
  [ "$lrc" = 2 ] && return 2
  # Losing the race is not a failure: the session that holds the lock is doing the same
  # work, and its status line reports it.
  [ "$lrc" = 0 ] || return 1
  _ensure_path_entry_locked; local rc=$?
  rc_lock_release
  return $rc
}

_ensure_path_entry_locked() {
  # Re-checked under the lock: the winner of the race may have written it just now.
  marked_already && return 1
  if path_covered; then
    # The stamp is ONLY a way to skip the expensive probe when coverage is still present —
    # never a reason to skip the scan above. It used to be checked before path_covered,
    # which was the bug: when the external line that provided coverage (a uv/pipx entry,
    # the user's own export) was later removed, the scan would have caught it, but a stamp
    # that still named this dir short-circuited the whole function — so the line was never
    # re-added and the session reported success while `kg` was command-not-found.
    [ -f "$PATH_OK_STAMP" ] && [ "$(cat "$PATH_OK_STAMP" 2>/dev/null)" = "$USER_BIN" ] && return 1
    local pr
    path_reachable; pr=$?
    # Confirmed by a real shell: remember it, so later sessions skip the probe entirely.
    # (stderr redirect first — a failed stamp write must not leak bash's own error.)
    [ "$pr" = 0 ] && { printf '%s\n' "$USER_BIN" 2>/dev/null > "$PATH_OK_STAMP"; return 1; }
    # No probe available (unusual shell, no mktemp): the scan is the best evidence there is.
    [ "$pr" = 2 ] && return 1
  else
    # Coverage is gone. Drop any stamp that still claims this dir, so a future session that
    # regains external coverage re-confirms it by probing rather than trusting a stale yes.
    [ -f "$PATH_OK_STAMP" ] && rm -f "$PATH_OK_STAMP" 2>/dev/null
  fi
  local rc
  rc="$(rc_target)"
  [ -n "$rc" ] || return 1
  # From here on the line is NEEDED, so failing to land it is rc 2, distinct from every
  # "nothing to do" above. The stderr redirect comes FIRST: redirections apply left to
  # right, and with `>> file` first a refused append (read-only profile) printed bash's
  # own Permission denied before the 2>/dev/null could catch it — at every session start.
  mkdir -p "$(dirname "$rc")" 2>/dev/null || return 2
  printf '\n%s\n%s\n' "$KGAI_MARK" "$(path_line)" 2>/dev/null >> "$rc" || return 2
  PATH_RC="$rc"
  return 0
}

ensure_on_path() {
  PATH_NOTE=""
  local w ep note=""
  write_launcher; w=$?
  if [ "$w" = 2 ]; then
    note="note: \`$USER_BIN/kg\` already exists and is not ours, so it was left alone — the plugin's engine is at $BIN. Remove or rename that file and the next session installs the launcher. "
  elif [ "$w" != 0 ]; then
    note="⚠ could not write $USER_BIN/kg (no permission?) — inside Claude Code \`kg\` still works; in your own terminal run $BIN directly. "
  fi

  # Deliberately unconditional. Whether the launcher could be written and whether
  # $USER_BIN is on PATH are separate problems with separate fixes, and letting the first
  # one skip the second is what left the PATH line unwritten on every machine that
  # already had some `kg` in ~/.local/bin — including our own pre-1.4.0 symlink.
  ensure_path_entry; ep=$?
  if [ "$ep" = 0 ]; then
    local pr
    # A second probe on a transition session (the first ran inside the covered branch of
    # _ensure_path_entry_locked) is deliberate: that one measured the profile BEFORE the
    # append, this one verifies the append took. Two states, two questions.
    path_reachable; pr=$?
    if [ "$pr" = 1 ]; then
      # Wired, and a real terminal still does not see it. Say so. Announcing success here
      # is exactly what made this class of failure invisible for a whole release.
      note="${note}⚠ \`kg\` is NOT on your terminal's PATH yet — the line went into $PATH_RC, but a fresh shell still does not pick it up. Add this to the profile your terminal actually reads: $(path_line) "
    elif [ "$pr" = 2 ]; then
      # "Could not find out" is reported as exactly that — the probe's own contract says
      # it must never be dressed up as either success or failure.
      note="${note}the PATH line for \`kg\` was added to $PATH_RC — whether a new terminal picks it up could not be verified. If \`kg\` is not found there, run: $(path_line) "
    elif [ "$w" = 0 ]; then
      note="${note}\`kg\` is now on your PATH via $USER_BIN (added to $PATH_RC) — new terminals have it; in one that is already open, run: $(path_line) "
    else
      note="${note}$USER_BIN was added to your PATH (in $PATH_RC) so the launcher is found once it can be installed. "
    fi
  elif [ "$ep" = 2 ]; then
    # The line is needed and could not be written — a read-only profile, or a $KGAI_HOME
    # the lock cannot even be created in. The one line the user can act on:
    note="${note}⚠ could not add \`$USER_BIN\` to your PATH (profile or \$KGAI_HOME not writable) — add this line to the profile your terminal actually reads: $(path_line) "
  fi
  # One status LINE is the contract — SessionStart stdout reaches the agent as a line —
  # and two of the interpolated values ($USER_BIN, $HOME) may legally contain a newline.
  # They are data, so they are flattened, not trusted.
  PATH_NOTE="$(printf '%s' "$note" | tr '\n' ' ')"
}

# Every store-reading engine call goes through here: in the project root (a call that ran
# in the hook's own cwd once reported another repository's conflicts), with the native lib
# on the loader path, and time-boxed — one wedged engine used to stall session start for
# the hook's whole 180s budget. Yesterday's copy-pasted incantation is also how the cwd
# bug happened: one call site drifted from the pattern. stdout is the engine's; stderr is
# discarded; the exit status is the engine's own, or 137 after a kill.
run_engine() {
  local tmp rc
  tmp="$(mktemp "${TMPDIR:-/tmp}/kgai-eng.XXXXXX" 2>/dev/null)" || return 1
  ( cd "$(project_root)" && KGAI_HOME="$KGAI_HOME" \
      LD_LIBRARY_PATH="$LIBDIR:${LD_LIBRARY_PATH:-}" \
      DYLD_LIBRARY_PATH="$LIBDIR:${DYLD_LIBRARY_PATH:-}" \
      "$BIN" "$@" ) >"$tmp" 2>/dev/null &
  reap_within $! "$ENGINE_TIMEOUT"; rc=$?
  cat "$tmp" 2>/dev/null
  rm -f "$tmp" 2>/dev/null
  return "$rc"
}

ensure_store() {
  # Create the store once, if the engine says there isn't one. ASK the engine rather
  # than testing <project>/.kgai/store: the store may be configured elsewhere entirely
  # (the `store` setting, KGAI_STORE — several repos sharing one graph), and a path
  # guessed here would re-init on every session and miss the shared store completely.
  run_engine status | grep -q '"initialized": *false' || return 0
  run_engine init >/dev/null 2>&1 || true
}

# An installed file is not a working engine. `kg version` is the cheapest command that
# still loads the native graph library, so it proves the whole chain: right architecture,
# dylib found next to the binary, allowed to run by the OS. Without this the installer
# announced "engine ready" for a binary that died on its first call, and every kg command
# for the rest of the session failed silently behind `|| true`.
ENGINE_ERR=""
engine_works() {
  local tmp out rc
  # `version` reads no store, so no cd — but the same time-box as every other engine call:
  # an engine that HANGS (a wedged filesystem, a launcher looping on itself) used to hold
  # this line, and with it the whole session start, until the hook's 180s cap fired.
  tmp="$(mktemp "${TMPDIR:-/tmp}/kgai-eng.XXXXXX" 2>/dev/null)" || return 1
  KGAI_HOME="$KGAI_HOME" LD_LIBRARY_PATH="$LIBDIR:${LD_LIBRARY_PATH:-}" \
    DYLD_LIBRARY_PATH="$LIBDIR:${DYLD_LIBRARY_PATH:-}" "$BIN" version >"$tmp" 2>&1 &
  reap_within $! "$ENGINE_TIMEOUT"; rc=$?
  out="$(cat "$tmp" 2>/dev/null)"
  rm -f "$tmp" 2>/dev/null
  if [ "$rc" = 0 ]; then ENGINE_ERR=""; return 0; fi
  # One line, first non-empty: dyld and the loader are verbose, the status line is not.
  # A signal kill (9/SIGKILL → 137) prints nothing we can capture; on macOS that is
  # almost always the OS refusing a binary whose signature it does not accept.
  # The engine's stderr lands in the status line the agent reads, so it is treated as
  # data: one line, control characters stripped, length capped.
  ENGINE_ERR="$(printf '%s' "$out" | grep -v '^[[:space:]]*$' | head -n1 |
                  tr -d '\000-\037' | cut -c1-300)"
  if [ -z "$ENGINE_ERR" ]; then
    if [ "$rc" = 137 ]; then ENGINE_ERR="did not respond within ${ENGINE_TIMEOUT}s"
    else ENGINE_ERR="exited with code $rc"; fi
  fi
  return 1
}

# Background auto-sync runs silently; a persistent failure surfaces once per session
# instead of nagging on every turn. Soft failures (expired SSO, offline) report ok:true
# with a detail, so check for either.
#
# ASK the engine where the store is. Guessing <project>/.kgai/store means the warning
# never fires for a store the `store` setting or KGAI_STORE moved — the multi-repo setup
# where a silent sync failure costs the most.
autosync_warning() {
  local lastsync root
  root="$(run_engine config | sed -n 's/.*"store_root": *"\([^"]*\)".*/\1/p')"
  lastsync="${root:-$(project_root)/.kgai/store}/last-autosync.json"
  if [ -f "$lastsync" ] && grep -qE '"ok": *false|"detail":' "$lastsync" 2>/dev/null; then
    printf '%s' "⚠ background team sync did not sync on its last attempt — tell the user to run \`kg sync\` to see why. "
  fi
}

report_ready() {
  ensure_on_path
  ensure_store
  local extra="$PATH_NOTE"
  if ! echo "$WANT" 2>/dev/null > "$KGAI_HOME/.srcver"; then
    # Without the fingerprint every later session decides the engine is out of date and
    # downloads it again — that gets said once, here, instead of silently paid at every
    # session start until someone wonders why starting is slow.
    extra="${extra}⚠ could not record the installed version at $KGAI_HOME/.srcver — the engine will be re-downloaded at every session start until $KGAI_HOME is writable. "
  fi
  # A heads-up if there are unresolved conflict branches. run_engine answers from the
  # project's root — without the cd this counted the conflicts of whatever directory the
  # hook happened to start in, observed reporting one repository's conflicts in another.
  local conf
  conf="$(run_engine conflicts | grep -o '"count": *[0-9]*' | grep -o '[0-9]*' || true)"
  if [ -n "$conf" ] && [ "$conf" != "0" ]; then
    extra="${extra}⚠ $conf unresolved decision conflict(s) — run /kgai:kg-conflicts. "
  fi
  extra="${extra}$(autosync_warning)"
  status "engine ready ($1). ${extra}Use /kgai:kg-ask before non-trivial changes; /kgai:kg-decision to record decisions."
}

# Sourcing the script with KGAI_INSTALL_LIB=1 stops here, with every function defined and
# nothing done. tests/*.sh drive the individual functions that way, against a sandbox
# $HOME, without downloading or building an engine. `return` works only when sourced (the
# intended use); guard it so that executing the script with the var set exits cleanly
# instead of printing a `return: can only ...` error and then proceeding to a full install.
if [ -n "${KGAI_INSTALL_LIB:-}" ]; then return 0 2>/dev/null || exit 0; fi

# Windows, reached through Git Bash where uname reports MINGW*/MSYS*/CYGWIN*, has no native
# engine — refuse here, before the prebuilt download and the source build, so the message
# is the accurate "use WSL" rather than the "install Go" / "github unreachable" that the
# build path would emit (Git Bash has no Go, and fetch-libs.sh exits on a non-Linux/Darwin
# uname). WSL reports Linux and installs normally.
case "$(host_os)" in
  MINGW*|MSYS*|CYGWIN*)
    status "⚠️ ENGINE NOT INSTALLED — Windows is not supported natively (Git Bash reports $(uname -s)). Run Claude Code inside WSL, where kgai installs as on any Linux."
    exit 0 ;;
esac

# Only past both guards: a refused platform (just told the engine cannot exist here) must
# not be left with an empty ~/.kgai, and sourcing the library must create nothing at all.
mkdir -p "$KGAI_HOME/bin" "$LIBDIR"

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
    status "⚠️ ENGINE INSTALLED BUT NOT RUNNING — kgai will NOT work this session ($ENGINE_ERR). Fix: rm -rf \"$KGAI_HOME/bin\" \"$KGAI_HOME/lib\" and start a new session to reinstall it (that is the engine only — $KGAI_HOME also holds your machine-wide config and approvals)."
    exit 0
  fi
  ensure_on_path
  ensure_store
  # The sync warning has to live here too: this is the path a normal session takes (the
  # engine is current), and report_ready — where it used to live alone — is only reached
  # by a session that installs or updates the engine. So the one thing it exists to tell
  # you was told approximately never.
  note="$PATH_NOTE$(autosync_warning)"
  [ -n "$note" ] && status "$note"
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
  # Unique temps via mktemp, never a fixed "$BIN.new": two sessions downloading together
  # interleaved their curl writes into one shared file, and either could publish (mv)
  # bytes the other was still writing — after the checksum had passed on an earlier state
  # of that file. The name keeps the kg.new. prefix so one glob still matches every temp.
  tmp_kg="$(mktemp "$KGAI_HOME/bin/kg.new.XXXXXX" 2>/dev/null)" || tmp_kg=""
  tmp_lib="$(mktemp "$LIBDIR/$lib_file.new.XXXXXX" 2>/dev/null)" || tmp_lib=""
  if [ -n "$tmp_kg" ] && [ -n "$tmp_lib" ] \
     && curl -fsSL -o "$tmp_kg" "$KG_RELEASE_BASE/kg-$os-$arch" 2>/dev/null \
     && curl -fsSL -o "$tmp_lib" "$KG_RELEASE_BASE/$lib_asset" 2>/dev/null \
     && verify_asset "$tmp_kg" "kg-$os-$arch" \
     && verify_asset "$tmp_lib" "$lib_asset"; then
    # 755 before the rename, and explicit: mktemp creates 0600, and `+x` under the usual
    # umask would publish 0711 — unreadable, so unrunnable, for other users of the machine.
    chmod 755 "$tmp_kg" 2>/dev/null
    mv "$tmp_kg" "$BIN"
    mv "$tmp_lib" "$LIBDIR/$lib_file"
    if engine_works; then
      report_ready "prebuilt $os-$arch"
      exit 0
    fi
    # Downloaded fine, checksum matched, still will not run here: a source build is the
    # one remaining self-heal (it produces a binary for THIS machine), so try it.
    status "prebuilt $os-$arch does not run on this machine ($ENGINE_ERR) — trying a source build…"
  else
    rm -f ${tmp_kg:+"$tmp_kg"} ${tmp_lib:+"$tmp_lib"} 2>/dev/null || true
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
  # Windows (MINGW*/MSYS*/CYGWIN*) was already refused above, before this section.
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
