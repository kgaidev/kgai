#!/usr/bin/env bash
# tests/run.sh — every installer suite, one exit code.
#
#   bash tests/run.sh          # all suites
#   bash tests/run.sh -v       # plus each test's own output
#
# The suites are independent scripts; this only sequences them and sums up. Nothing here
# reaches the network or touches the real $HOME.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.."

FAILED_SUITES=""
for suite in tests/install-flow.sh tests/install-path.sh tests/install-rc-safety.sh \
             tests/repo-hygiene.sh; do
  printf '\n═══ %s ═══\n' "$suite"
  bash "$suite" "$@" || FAILED_SUITES="$FAILED_SUITES $suite"
done

printf '\n'
if [ -n "$FAILED_SUITES" ]; then
  printf 'FAILED:%s\n' "$FAILED_SUITES"
  exit 1
fi
printf 'all installer suites passed\n'
