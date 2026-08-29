#!/usr/bin/env bash
# tests/repo-hygiene.sh — invariants about the repository itself, not the installer.
#
# These are the rules a reviewer would otherwise have to re-check by hand on every
# release: CI actions stay pinned to commits (a moved tag is exactly how a compromised
# action reaches every workflow that trusts it), workflows say what they are allowed to
# write, and the version the plugin claims is the version the changelog leads with.
#
# Run:  bash tests/repo-hygiene.sh [-v]
set -uo pipefail
REPO="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"

T=0; PASSED=0; FAILED=0
red()   { printf '\033[31m%s\033[0m' "$1"; }
green() { printf '\033[32m%s\033[0m' "$1"; }

check() { # <description> <ok?>
  T=$((T + 1))
  if [ "$2" = 0 ]; then
    PASSED=$((PASSED + 1)); printf '  %s   %s\n' "$(green ok)" "$1"
  else
    FAILED=$((FAILED + 1)); printf '  %s %s\n' "$(red FAIL)" "$1"
    [ -n "${3:-}" ] && printf '         %s\n' "$3"
  fi
}

printf 'kgai — repo hygiene\n\n'

# Every third-party action is pinned to a full commit SHA (a trailing "# vN" comment
# keeps the human-readable version next to it).
unpinned="$(grep -hn 'uses:' "$REPO/.github/workflows/"*.yml |
  grep -Ev 'uses:[[:space:]]*[^@]+@[0-9a-f]{40}([[:space:]]|$)' || true)"
check "every workflow action is pinned to a commit SHA" \
  "$([ -z "$unpinned" ]; echo $?)" "$unpinned"

# Every workflow declares its permissions at the top level — none may inherit the
# repository default silently.
missing=""
for wf in "$REPO/.github/workflows/"*.yml; do
  grep -q '^permissions:' "$wf" || missing="$missing ${wf##*/}"
done
check "every workflow declares top-level permissions" \
  "$([ -z "$missing" ]; echo $?)" "missing in:$missing"

# The version plugin.json claims is the version the changelog leads with — the same pair
# the release workflow refuses to tag apart.
plugin_ver="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
  "$REPO/.claude-plugin/plugin.json" | head -n1)"
changelog_ver="$(sed -n 's/^## \[\([0-9][^]]*\)\].*/\1/p' "$REPO/CHANGELOG.md" | head -n1)"
check "plugin.json ($plugin_ver) and CHANGELOG ($changelog_ver) agree on the version" \
  "$([ -n "$plugin_ver" ] && [ "$plugin_ver" = "$changelog_ver" ]; echo $?)"

# The engine release workflow fires on `v*` tags and the extension release on `vscode-v*`
# tags; the two filters must not overlap, or an extension release also starts an engine
# release that fails its version check (vscode-v0.2.0 did exactly that). GitHub's filter
# globs and bash `case` globs agree on the patterns used here.
tag_filter() { sed -n "s/^[[:space:]]*tags:[[:space:]]*\['\([^']*\)'\].*/\1/p" "$1" | head -n1; }
engine_glob="$(tag_filter "$REPO/.github/workflows/build.yml")"
ext_glob="$(tag_filter "$REPO/.github/workflows/vscode.yml")"
matches() { case "$2" in $1) return 0 ;; *) return 1 ;; esac; }
ok=0
{ [ -n "$engine_glob" ] && [ -n "$ext_glob" ] &&
  matches "$engine_glob" v1.5.2 && ! matches "$engine_glob" vscode-v0.2.0 &&
  matches "$ext_glob" vscode-v0.2.0 && ! matches "$ext_glob" v1.5.2; } || ok=1
check "release tag filters do not overlap (engine '$engine_glob', extension '$ext_glob')" "$ok"

# The extension's package.json lives in editors/vscode, but vsce rewrites the README's
# relative image and link paths against the repository root — 0.2.0 shipped a listing with
# eight broken pictures. Every `vsce package` call must therefore name the directory.
bad=""
for src in "$REPO/.github/workflows/vscode.yml" "$REPO/editors/vscode/package.json"; do
  calls="$(tr '\n' ' ' < "$src" | tr -s ' ' | sed 's/\\ / /g' | grep -o 'vsce package[^&|;"]*' || true)"
  [ -n "$calls" ] || bad="$bad ${src##*/}:no-vsce-package-call"
  printf '%s\n' "$calls" | while IFS= read -r call; do
    [ -z "$call" ] && continue
    case "$call" in
      *"--baseImagesUrl https://github.com/kgaidev/kgai/raw/HEAD/editors/vscode/"*) ;;
      *) echo " ${src##*/}:images" ;;
    esac
    case "$call" in
      *"--baseContentUrl https://github.com/kgaidev/kgai/blob/HEAD/editors/vscode/"*) ;;
      *) echo " ${src##*/}:content" ;;
    esac
  done > "$REPO/.hygiene-vsce.tmp"
  bad="$bad$(cat "$REPO/.hygiene-vsce.tmp")"; rm -f "$REPO/.hygiene-vsce.tmp"
done
check "every vsce package call rewrites README paths under editors/vscode/" \
  "$([ -z "$bad" ]; echo $?)" "missing:$bad"

printf '\n%s passed, %s failed, %s total\n' "$PASSED" "$FAILED" "$T"
[ "$FAILED" = 0 ] || exit 1
