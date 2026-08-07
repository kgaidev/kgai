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

printf '\n%s passed, %s failed, %s total\n' "$PASSED" "$FAILED" "$T"
[ "$FAILED" = 0 ] || exit 1
