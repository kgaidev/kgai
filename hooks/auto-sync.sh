#!/usr/bin/env bash
# SessionStart + Stop hook — fire-and-forget team sync, no LLM involved.
#
# This script only SPAWNS `kg sync --auto` detached and exits immediately (~10 ms),
# so neither the user nor the model ever waits on the network. The engine makes the
# spawn a no-op unless it is actually useful:
#   - no store here, or no remote configured  → exits silently in a few ms
#   - synced less than the cooldown (60 s) ago → skipped
#   - another sync/write holds the store lock  → skipped (never queues)
# Real attempts record their outcome in <store>/last-autosync.json; a persistently
# failing sync surfaces once per session in the install status line — never louder.
# HOME may be absent when a host does not pass its hooks the launching shell's env (Codex).
[ -n "${HOME:-}" ] || HOME="$(getent passwd "$(id -un 2>/dev/null)" 2>/dev/null | cut -d: -f6)"
KGAI_HOME="${KGAI_HOME:-$HOME/.kgai}"
BIN="$KGAI_HOME/bin/kg"
[ -x "$BIN" ] || BIN="$(command -v kg 2>/dev/null)"
[ -n "$BIN" ] && [ -x "$BIN" ] || exit 0
export LD_LIBRARY_PATH="$KGAI_HOME/lib:${LD_LIBRARY_PATH:-}"
export DYLD_LIBRARY_PATH="$KGAI_HOME/lib:${DYLD_LIBRARY_PATH:-}"
# setsid is util-linux and does not exist on macOS — without the fallback the spawn failed
# silently there and automatic sync never ran on a Mac. nohup + background in a subshell
# detaches well enough: the child survives this hook exiting and ignores the hangup.
if command -v setsid >/dev/null 2>&1; then
  setsid "$BIN" sync --auto </dev/null >/dev/null 2>&1 &
else
  ( nohup "$BIN" sync --auto </dev/null >/dev/null 2>&1 & )
fi
exit 0
