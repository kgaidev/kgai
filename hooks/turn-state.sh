#!/usr/bin/env bash
# PostToolUse / AfterTool hook — records what THIS turn did, host-independently.
# Claude Code: PostToolUse · Codex: PostToolUse · Gemini CLI: AfterTool.
#
# Auto-capture needs two facts at end of turn: did the turn edit code, and did the model
# already record a decision. On Claude Code those can be read back out of the transcript
# JSONL; on Codex and Gemini CLI they cannot — Codex writes a rollout log with an entirely
# different shape (`type: response_item`) and Gemini's AfterAgent event carries no
# transcript to parse at all. A parser per host is three parsers to keep correct against
# three moving formats.
#
# So the turn is observed as it happens instead. The engine does the reading (`kg turn
# mark`, which takes the event payload on stdin) because this runs after EVERY matching
# tool call: shelling out to an interpreter to parse one JSON object cost more than
# starting the whole engine does, and on a machine without that interpreter the hook
# silently did nothing — decisions stopped being captured with no error anywhere.
#
# Which tools reach this at all is the host's matcher in its own hooks.json, since only the
# host knows its tool names.
[ -n "${KGAI_DISABLE_HOOKS:-}" ] && exit 0

# HOME may be absent: a host does not have to hand its hooks the launching shell's
# environment, and $KGAI_HOME defaults to $HOME/.kgai. Derive it from the passwd database
# — which needs no environment — before that default is computed. Harmless when HOME is
# already set (the guard skips it).
[ -n "${HOME:-}" ] || HOME="$(getent passwd "$(id -un 2>/dev/null)" 2>/dev/null | cut -d: -f6)"

# Locate the engine: the direct path first, then the launcher install.sh puts on PATH
# (a host that strips custom vars still keeps a PATH carrying the user's entries).
KGAI_HOME="${KGAI_HOME:-$HOME/.kgai}"
BIN="$KGAI_HOME/bin/kg"
[ -x "$BIN" ] || BIN="$(command -v kg 2>/dev/null)"

if [ -n "$BIN" ] && [ -x "$BIN" ]; then
  export LD_LIBRARY_PATH="$KGAI_HOME/lib:${LD_LIBRARY_PATH:-}"
  export DYLD_LIBRARY_PATH="$KGAI_HOME/lib:${DYLD_LIBRARY_PATH:-}"
  # Every failure here is ignored on purpose, including "this engine is too old to know
  # `turn`": the tool call the user is waiting on must not break over a missed marker, and
  # the end-of-turn hook falls back to the transcript when no marker was written.
  "$BIN" turn mark >/dev/null 2>&1
fi

# "Nothing to report", in the one shape all three hosts accept. NOT {"suppressOutput":true}:
# Gemini and Claude honor that key, but Codex rejects it and logs the hook as failed. An
# empty object directs nothing and parses cleanly everywhere.
echo '{}'
exit 0
