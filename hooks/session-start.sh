#!/usr/bin/env bash
# SessionStart hook, one entry — for hosts that want a single JSON object on stdout.
#
# Claude Code runs the three pieces below as three hook entries and concatenates whatever
# they print. Gemini CLI cannot: it parses stdout as JSON and treats anything else as a
# protocol error, so three writers would be three errors. Codex accepts either, and is
# served by the same single object.
#
# So this wrapper runs the same three pieces in the same order, collects their context,
# and emits it once. The pieces stay independent scripts — Claude's hooks.json keeps
# calling them directly, and they keep their own timeouts and failure behaviour.
[ -n "${KGAI_DISABLE_HOOKS:-}" ] && exit 0
ROOT="${KGAI_PLUGIN_ROOT:-${CLAUDE_PLUGIN_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." 2>/dev/null && pwd)}}"
input=$(cat)

out=""
add() { [ -n "$1" ] && out="${out:+$out
}$1"; }

# The installer is the slow one (first run downloads the engine); the host's own timeout
# in hooks.json is what bounds it. Its status line is written for the model to read.
add "$(bash "$ROOT/scripts/install.sh" 2>/dev/null)"
# Fire-and-forget, prints nothing, returns in milliseconds.
bash "$ROOT/hooks/auto-sync.sh" >/dev/null 2>&1
# Asked for TEXT on purpose: this wrapper owns the JSON envelope, and a nested one would
# arrive as a literal `{"hookSpecificOutput":…}` string inside the context.
add "$(KGAI_HOOK_OUTPUT=text bash "$ROOT/hooks/inject-prompt.sh" 2>/dev/null)"

[ -n "$out" ] || { echo '{}'; exit 0; }   # {} not {"suppressOutput":true}: Codex rejects that key

if [ "${KGAI_HOOK_OUTPUT:-json}" != "json" ]; then
  printf '%s\n' "$out"
  exit 0
fi
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import json,sys; print(json.dumps({"hookSpecificOutput": {"hookEventName": "SessionStart", "additionalContext": sys.argv[1]}}))' "$out"
else
  esc=$(printf '%s' "$out" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/\t/\\t/g' -e 's/\r//g' | awk 'NR>1{printf "\\n"} {printf "%s", $0}')
  printf '{"hookSpecificOutput": {"hookEventName": "SessionStart", "additionalContext": "%s"}}\n' "$esc"
fi
