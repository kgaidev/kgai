#!/usr/bin/env bash
# End-of-turn hook — deterministic auto-capture trigger.
# Claude Code: Stop · Codex: Stop · Gemini CLI: AfterAgent.
#
# The failure mode without this: the model reads the graph and even recognizes "this is
# a structural decision worth recording", but ends its turn without writing it (~50-75%
# reliable). This hook fires at end of turn, and IF the turn edited code, forces the
# model to record any structural decision NOW (or record nothing for trivial work).
#
# It needs two facts: did this turn edit code, and did the model already record. First
# choice is `kg turn take` — the marks hooks/turn-state.sh left while the turn was
# running, read and cleared in one call. That is host-independent, and the only thing that
# works on Codex and Gemini, whose transcripts are either shaped nothing like Claude's or
# absent from the event. Without an engine that can answer (too old, not installed yet) it
# falls back to reading Claude's transcript, which is what this hook did before.
#
# Loop-safe: it never blocks twice in a row (honors stop_hook_active). It adds no LLM
# cost of its own — it just injects one focused instruction at exactly the right moment.
[ -n "${KGAI_DISABLE_HOOKS:-}" ] && exit 0
# HOME may be absent when a host does not pass its hooks the launching shell's env
# (Codex); derive it from passwd, which needs no environment, before the default below.
[ -n "${HOME:-}" ] || HOME="$(getent passwd "$(id -un 2>/dev/null)" 2>/dev/null | cut -d: -f6)"
KGAI_HOME="${KGAI_HOME:-$HOME/.kgai}"
BIN="$KGAI_HOME/bin/kg"
[ -x "$BIN" ] || BIN="$(command -v kg 2>/dev/null)"
[ -n "$BIN" ] && [ -x "$BIN" ] || exit 0
export LD_LIBRARY_PATH="$KGAI_HOME/lib:${LD_LIBRARY_PATH:-}"
export DYLD_LIBRARY_PATH="$KGAI_HOME/lib:${DYLD_LIBRARY_PATH:-}"

dbg() { [ -n "${KGAI_HOOK_DEBUG:-}" ] && printf '%s %s\n' "$(date +%s 2>/dev/null)" "$*" >> "$KGAI_HOOK_DEBUG"; return 0; }

input=$(cat)

# Read and consume the marks BEFORE the stop_hook_active check, never after. When this
# hook blocks, the turn continues and ends a second time with stop_hook_active set — and
# whatever that continuation edited has landed in a fresh marker by then. Returning early
# without consuming it leaves those marks for the NEXT turn, which then gets told it
# edited code it never touched. A nudge that fires on the wrong turn teaches the model to
# ignore it.
marks="$(printf '%s' "$input" | "$BIN" turn take --json 2>/dev/null)"
found="$(printf '%s' "$marks" | sed -n 's/.*"found": *\([a-z]*\).*/\1/p' | head -n1)"
edited="$(printf '%s' "$marks" | sed -n 's/.*"edited": *\([0-9]*\).*/\1/p' | head -n1)"
recorded="$(printf '%s' "$marks" | sed -n 's/.*"recorded": *\([a-z]*\).*/\1/p' | head -n1)"
source=marker

# ---- fallback: Claude Code's transcript ----------------------------------------------
# Only Claude's transcript has this shape. Codex's rollout JSONL (`type: response_item`)
# matches nothing here and simply yields "no edits", which is the safe direction: the turn
# ends instead of being blocked on a guess. Needs python3; without it (a fresh macOS before
# the Command Line Tools) there is no fallback, which is exactly the hole that moving the
# marker path into the engine closed.
if [ "$found" != "true" ] && command -v python3 >/dev/null 2>&1; then
  source=transcript
  # The payload goes in as an ARGUMENT, not on stdin: stdin is already spoken for by the
  # heredoc carrying this program, so a piped payload would be swallowed and every turn
  # would read as "edited nothing".
  fallback="$(python3 - "$input" <<'PY'
import sys, json

try:
    ev = json.loads(sys.argv[1])
except Exception:
    print("0 false"); raise SystemExit
tp = ev.get("transcript_path")
if not tp:
    print("0 false"); raise SystemExit
try:
    lines = open(tp, encoding="utf-8").read().splitlines()
except Exception:
    print("0 false"); raise SystemExit

# Scope to THIS turn: the human's prompt is a user message whose content is a plain
# STRING. Everything else with type "user" (tool results, skill-injected text, reminders)
# is a list, so it must not be mistaken for the turn boundary.
EDIT_TOOLS = {"Edit", "Write", "MultiEdit", "NotebookEdit"}

parsed = [None] * len(lines)
last_human = -1
for i, ln in enumerate(lines):
    try:
        parsed[i] = json.loads(ln)
    except Exception:
        parsed[i] = None
    o = parsed[i]
    if o and o.get("type") == "user" and isinstance((o.get("message") or {}).get("content"), str):
        last_human = i

def tool_uses(o):
    c = (o.get("message") or {}).get("content") if o else None
    if isinstance(c, list):
        for b in c:
            if isinstance(b, dict) and b.get("type") == "tool_use":
                yield b

edited = 0
recorded = False
for o in parsed[last_human + 1:]:
    for b in tool_uses(o):
        name = b.get("name")
        if name in EDIT_TOOLS:
            edited += 1
        cmd = (b.get("input") or {}).get("command", "") if name == "Bash" else ""
        if "kg ingest" in cmd or "kg-decision" in cmd:
            recorded = True
print("%d %s" % (edited, "true" if recorded else "false"))
PY
)"
  edited="${fallback%% *}"
  recorded="${fallback##* }"
fi

dbg "fired source=$source edited=${edited:-0} recorded=${recorded:-false} found=${found:-false}"

# Already nudged this turn → let the turn end (prevents an infinite stop loop).
case "$input" in
  *'"stop_hook_active": true'*|*'"stop_hook_active":true'*) exit 0 ;;
esac

[ "${edited:-0}" -gt 0 ] 2>/dev/null || exit 0
[ "${recorded:-false}" = "true" ] && exit 0

dbg "BLOCK issued"
# Kept on one line and free of double quotes and backslashes, because it is interpolated
# straight into the JSON below. The contract suite asserts that this stays valid JSON.
reason='Before you stop: this turn edited code. If it involved a STRUCTURAL/architectural decision about the codebase — splitting/merging/moving a module or feature, renaming a domain element (its canonical name — code-level renames of files/functions do not count), changing a dependency or ownership boundary, changing how something is exposed or rendered, or deprecating/replacing a prior decision — you MUST record it NOW via a single `kg ingest` (do NOT ask permission; use the knowledge-graph skill DO/DONT rules to decide what counts). If the turn made no such decision — a code-level rename, formatting, a bug fix, or ONLY analyses/reports/recommendations with no choice acted on — record nothing and just stop. Either record and note in one line what you captured, or stop.'
printf '{"decision": "block", "reason": "%s"}\n' "$reason"
