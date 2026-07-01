#!/usr/bin/env bash
# Stop hook — deterministic auto-capture trigger.
#
# The failure mode without this: the model reads the graph and even recognizes "this is
# a structural decision worth recording", but ends its turn without writing it (~50-75%
# reliable). This hook fires at end of turn, and IF the turn edited code, forces the
# model to record any structural decision NOW (or record nothing for trivial work).
#
# Loop-safe: it never blocks twice in a row (honors stop_hook_active). It adds no LLM
# cost of its own — it just injects one focused instruction at exactly the right moment.
input=$(cat)
python3 - "$input" <<'PY'
import sys, json, os, time

def dbg(msg):
    p = os.environ.get("KGAI_HOOK_DEBUG")
    if p:
        try:
            open(p, "a").write(f"{time.time():.0f} {msg}\n")
        except Exception:
            pass

try:
    ev = json.loads(sys.argv[1])
except Exception:
    dbg("no-json"); sys.exit(0)
dbg("fired active=%s tp=%s" % (ev.get("stop_hook_active"), ev.get("transcript_path")))

# Already nudged this turn → let the turn end (prevents an infinite stop loop).
if ev.get("stop_hook_active"):
    sys.exit(0)

tp = ev.get("transcript_path")
if not tp:
    sys.exit(0)
try:
    lines = open(tp, encoding="utf-8").read().splitlines()
except Exception:
    sys.exit(0)

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
recorded = False  # did the model already run `kg ingest` this turn?
for o in parsed[last_human + 1:]:
    for b in tool_uses(o):
        name = b.get("name")
        if name in EDIT_TOOLS:
            edited += 1
        cmd = (b.get("input") or {}).get("command", "") if name == "Bash" else ""
        if "kg ingest" in cmd or "kg-decision" in cmd:
            recorded = True

dbg("edited=%s recorded=%s last_human=%s lines=%s" % (edited, recorded, last_human, len(lines)))
if not edited or recorded:
    sys.exit(0)

dbg("BLOCK issued")
reason = (
    "Before you stop: this turn edited code. If it involved a STRUCTURAL/architectural "
    "decision about the codebase — splitting/merging/moving/renaming a module or feature, "
    "changing a dependency or ownership boundary, changing how something is exposed or "
    "rendered, or deprecating/replacing a prior decision — you MUST record it NOW via a "
    "single `kg ingest` (do NOT ask permission; use the knowledge-graph skill's DO/DON'T "
    "rules to decide what counts). If the turn made no such structural decision (a pure "
    "rename, formatting, a bug fix) or you already recorded it, record nothing and just "
    "stop. Either record and note in one line what you captured, or stop."
)
print(json.dumps({"decision": "block", "reason": reason}))
PY
