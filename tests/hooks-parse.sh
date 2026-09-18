#!/usr/bin/env bash
# tests/hooks-parse.sh — every script the plugin ships must PARSE under the bash it meets.
#
# The hooks are `#!/usr/bin/env bash`, and on a stock Mac that is bash 3.2. 3.2 scans a
# here-doc nested in $(...) for quotes, so one apostrophe in a comment of the embedded
# Python made auto-capture-stop.sh a syntax error from its first byte: in 1.7.1 the Stop
# hook died on every turn of every macOS user, nothing was ever nudged, and the only
# trace was an "unexpected EOF" line nobody reads. bash 5 parses the same file happily,
# so no suite that runs on Linux could see it. This one is cheap enough (no engine, no
# network) to run inside the bash 3.2 jobs, which is the only place it means anything.
#
# Run:  bash tests/hooks-parse.sh
set -uo pipefail
REPO="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"

T=0; PASSED=0; FAILED=0
red()   { printf '\033[31m%s\033[0m' "$1"; }
green() { printf '\033[32m%s\033[0m' "$1"; }

check() { # <description> <ok?> [detail]
  T=$((T + 1))
  if [ "$2" = 0 ]; then
    PASSED=$((PASSED + 1)); printf '  %s   %s\n' "$(green ok)" "$1"
  else
    FAILED=$((FAILED + 1)); printf '  %s %s\n' "$(red FAIL)" "$1"
    [ -n "${3:-}" ] && printf '         %s\n' "$3"
  fi
}

printf 'kgai — shipped scripts parse under bash %s\n\n' "$BASH_VERSION"

for f in "$REPO"/hooks/*.sh "$REPO"/bin/* "$REPO"/scripts/*.sh; do
  [ -f "$f" ] || continue
  head -n1 "$f" | grep -q 'bash' || continue
  err="$(bash -n "$f" 2>&1)"
  check "${f#"$REPO"/} parses" "$?" "$err"
done

[ "$T" -gt 0 ] || { printf 'FAIL: no shipped bash scripts found under %s\n' "$REPO"; exit 1; }

printf '\n%s passed, %s failed, %s total\n' "$PASSED" "$FAILED" "$T"
[ "$FAILED" = 0 ] || exit 1
