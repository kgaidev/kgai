#!/usr/bin/env bash
# tests/hooks-contract.sh — the hook scripts against the event contract of every host
# they ship to: Claude Code, Codex CLI and Gemini CLI.
#
# These hooks are the only part of the plugin the user never invokes. When one of them
# quietly stops firing, nothing errors — decisions simply stop being captured, and that
# is invisible until somebody asks the graph a question months later and it is empty. So
# each host's payload shape is pinned here, with its real field names and real tool
# names, and asserted against the exact output that host accepts.
#
# No host, no model and no network: the scripts are fed the documented event JSON on
# stdin and their stdout is checked. Fast enough to run on every commit.
#
# Run:  bash tests/hooks-contract.sh [-v]        (exit 0 = all green)
set -uo pipefail
. "$(dirname "${BASH_SOURCE[0]:-$0}")/lib.sh"

# ---- the engine under test ------------------------------------------------------------
# `kg turn mark|take` is the marker path, so a stand-in that fakes it would test nothing.
# These run against a real build. Resolved BEFORE any sandbox replaces $HOME.
#   KGAI_TEST_ENGINE=<path>   use this binary
#   otherwise                 build src/ once into dist/test-engine (needs go + cgo)
#   neither available         the marker tests skip; the rest still run
REAL_LIB="${KGAI_HOME:-$HOME/.kgai}/lib"
[ -e "$REAL_LIB/libkuzu.so" ] || [ -e "$REAL_LIB/libkuzu.dylib" ] ||
  REAL_LIB="$REPO/third_party/go-kuzu/lib/dynamic/linux-amd64"
ENGINE="${KGAI_TEST_ENGINE:-}"
if [ -z "$ENGINE" ]; then
  ENGINE="$REPO/dist/test-engine/kg"
  if [ ! -x "$ENGINE" ] || [ -n "$(find "$REPO/src" -name '*.go' -newer "$ENGINE" 2>/dev/null | head -n1)" ]; then
    if command -v go >/dev/null 2>&1; then
      mkdir -p "$(dirname "$ENGINE")"
      ( cd "$REPO/src" && go build -o "$ENGINE" . ) >/dev/null 2>&1 || ENGINE=""
    else
      ENGINE=""
    fi
  fi
fi
[ -n "$ENGINE" ] && [ -x "$ENGINE" ] || ENGINE=""
need_engine() { [ -n "$ENGINE" ] || { skip "no kg build available (set KGAI_TEST_ENGINE, or install go)"; return 1; }; }

# A sandbox whose engine answers the config calls inject-prompt.sh makes with canned
# values, and hands everything else — `turn` above all — to the real binary. Plus a private
# state dir, so the turn markers of one test cannot reach another.
sandbox() {
  lib_sandbox
  export KGAI_STATE_DIR="$SB/state"
  export KG_REAL_ENGINE="$ENGINE" KG_REAL_LIB="$REAL_LIB"
  cat > "$SB/.kgai/bin/kg" <<'STUB'
#!/usr/bin/env bash
# Canned answers for the config reads; everything else goes to the real engine.
case "$*" in
  "config get prompt")       printf '{"ok": true, "value": "x", "source": "project"}\n'; exit 0 ;;
  "config get --raw prompt") printf '%s' "${KG_STUB_PROMPT:-}"; exit 0 ;;
esac
if [ -n "${KG_REAL_ENGINE:-}" ] && [ -x "$KG_REAL_ENGINE" ]; then
  export LD_LIBRARY_PATH="${KG_REAL_LIB:-}:${LD_LIBRARY_PATH:-}"
  export DYLD_LIBRARY_PATH="${KG_REAL_LIB:-}:${DYLD_LIBRARY_PATH:-}"
  exec "$KG_REAL_ENGINE" "$@"
fi
printf '{"ok": true}\n'
STUB
  chmod +x "$SB/.kgai/bin/kg"
  KG_STUB_PROMPT=""; export KG_STUB_PROMPT
  unset KGAI_HOOK_OUTPUT
}

# Feed one event to a hook and capture what it printed. The exit code is half of the
# contract — a non-zero exit is a hook FAILURE on every host, and exit 2 means "block".
fire() { # <script> <json payload>
  OUT="$(printf '%s' "$2" | bash "$REPO/hooks/$1" 2>"$SB/hook.err")"
  RC=$?
  ERR="$(cat "$SB/hook.err" 2>/dev/null)"
}

# stdout must parse as JSON — Gemini CLI treats anything else as a protocol error, and
# Codex reads it as loose developer text instead of acting on it.
assert_json() { # desc payload
  printf '%s' "$2" | python3 -c 'import json,sys; json.load(sys.stdin)' 2>/dev/null ||
    _fail "$1: stdout is not valid JSON: [$2]"
}

json_field() { # <json> <python expression over `d`>
  printf '%s' "$1" | python3 -c "import json,sys; d=json.load(sys.stdin); print($2)" 2>/dev/null
}

marks() { cat "$KGAI_STATE_DIR"/turn-* 2>/dev/null | tr '\n' ' '; }

stop_ev() { printf '{"session_id":"%s","hook_event_name":"%s","cwd":"/p"}' "$1" "${2:-Stop}"; }

# ---- payload fixtures, one per host ---------------------------------------------------
# Claude Code and Codex send tool_name/tool_input on PostToolUse; Gemini CLI sends the
# same two fields on AfterTool. That agreement is what lets one script serve all three —
# if a host ever breaks it, these fixtures are where it shows up.
CLAUDE_EDIT='{"session_id":"s-claude","hook_event_name":"PostToolUse","cwd":"/p","tool_name":"Edit","tool_input":{"file_path":"/p/src/invoice.js","old_string":"a","new_string":"b"}}'
CODEX_PATCH='{"session_id":"s-codex","hook_event_name":"PostToolUse","cwd":"/p","tool_name":"apply_patch","tool_input":{"input":"*** Update File: src/invoice.js"}}'
GEMINI_WRITE='{"session_id":"s-gem","hook_event_name":"AfterTool","cwd":"/p","tool_name":"write_file","tool_input":{"file_path":"/p/src/invoice.js","content":"x"}}'
GEMINI_SED='{"session_id":"s-gem","hook_event_name":"AfterTool","cwd":"/p","tool_name":"run_shell_command","tool_input":{"command":"sed -i s/a/b/ src/invoice.js"}}'
SHELL_INGEST='{"session_id":"s-claude","hook_event_name":"PostToolUse","cwd":"/p","tool_name":"Bash","tool_input":{"command":"kg ingest < payload.json"}}'
SHELL_READ='{"session_id":"s-any","hook_event_name":"PostToolUse","cwd":"/p","tool_name":"Bash","tool_input":{"command":"cat skills/knowledge-graph/SKILL.md"},"tool_response":{"output":"run kg ingest to record"}}'
UNRELATED='{"session_id":"s-any","hook_event_name":"PostToolUse","cwd":"/p","tool_name":"read_file","tool_input":{"path":"/p/a.js"}}'

# ======================================================================================
# A. turn-state.sh — what this turn did, observed as it happens
# ======================================================================================

t_mark_claude_edit() {
  need_engine || return 0
  fire turn-state.sh "$CLAUDE_EDIT"
  assert_rc "exit" "$RC" 0
  assert_json "stdout" "$OUT"
  assert_has "mark" "$(marks)" "edit"
}

t_mark_codex_patch() {
  need_engine || return 0
  fire turn-state.sh "$CODEX_PATCH"
  assert_has "mark" "$(marks)" "edit"
}

t_mark_gemini_write() {
  need_engine || return 0
  fire turn-state.sh "$GEMINI_WRITE"
  assert_has "mark" "$(marks)" "edit"
}

# Codex and Gemini both edit through the shell as often as through their edit tool. A
# turn that only ever ran `sed -i` still edited code and still owes a capture decision.
t_mark_shell_edit() {
  need_engine || return 0
  fire turn-state.sh "$GEMINI_SED"
  assert_has "mark" "$(marks)" "edit"
}

t_mark_ingest() {
  need_engine || return 0
  fire turn-state.sh "$SHELL_INGEST"
  assert_has "mark" "$(marks)" "ingest"
}

# The regression this pins: `kg ingest` appears verbatim in the plugin's own skill file.
# Reading that file is not recording a decision, and counting it as one would silence the
# capture nudge for the rest of the turn — the exact failure the hook exists to prevent.
t_ingest_in_output_is_not_a_recording() {
  need_engine || return 0
  fire turn-state.sh "$SHELL_READ"
  assert_hasnt "mark" "$(marks)" "ingest"
}

t_unrelated_tool_marks_nothing() {
  need_engine || return 0
  fire turn-state.sh "$UNRELATED"
  assert_eq "no marks" "$(marks)" ""
  assert_json "stdout" "$OUT"
}

# Codex rejects a hook that returns {"suppressOutput": true} and logs it as failed
# ("PostToolUse hook returned unsupported suppressOutput"), seen in a real Codex session.
# The "nothing to report" reply must be an empty object, which all three hosts accept.
t_output_is_codex_safe() {
  need_engine || return 0
  fire turn-state.sh "$UNRELATED"
  assert_json "stdout" "$OUT"
  assert_hasnt "no suppressOutput key" "$OUT" "suppressOutput"
}

# The engine is at $KGAI_HOME/bin/kg, which defaults through $HOME/.kgai — but Codex does
# not pass HOME to hooks. turn-state must still mark: it derives HOME from the passwd
# database. A fake `getent`/`id` on PATH points that derivation at the sandbox (whose
# .kgai/bin/kg execs the real engine), so the test exercises the real code path without
# depending on — or being shadowed by — the developer's actual ~/.kgai.
t_marks_without_home_in_env() {
  need_engine || return 0
  # The sandbox engine already execs the real build (see sandbox()); point HOME here.
  mkdir -p "$SB/fakebin"
  printf '#!/bin/sh\ncase "$1" in passwd) echo "t:x:1000:1000::%s:/bin/sh" ;; esac\n' "$SB" \
    > "$SB/fakebin/getent"
  printf '#!/bin/sh\necho t\n' > "$SB/fakebin/id"
  chmod +x "$SB/fakebin/getent" "$SB/fakebin/id"
  OUT="$(printf '%s' "$CLAUDE_EDIT" | env -u HOME -u KGAI_HOME \
    KGAI_STATE_DIR="$KGAI_STATE_DIR" PATH="$SB/fakebin:$PATH" \
    bash "$REPO/hooks/turn-state.sh" 2>/dev/null)"
  assert_json "stdout" "$OUT"
  assert_has "marked with HOME derived from passwd, not the env" "$(marks)" "edit"
}

# `>/dev/null` is how shell commands silence themselves, not how they write code. Reading
# it as an edit nudges for a capture decision at the end of turns that only ever looked
# at things — and a nudge that fires on turns with nothing to record is a nudge the model
# learns to ignore.
t_silencing_redirect_is_not_an_edit() {
  need_engine || return 0
  fire turn-state.sh '{"session_id":"s-any","tool_name":"Bash","tool_input":{"command":"kg status >/dev/null 2>&1"}}'
  assert_eq "no marks" "$(marks)" ""
}

t_sessions_are_separate() {
  need_engine || return 0
  fire turn-state.sh "$CLAUDE_EDIT"
  fire turn-state.sh "$GEMINI_WRITE"
  assert_file_has "claude session" "$KGAI_STATE_DIR/turn-s-claude" "edit"
  assert_file_has "gemini session" "$KGAI_STATE_DIR/turn-s-gem" "edit"
}

# A hook that cannot write its marker must still not break the tool call it follows.
t_unwritable_state_dir_is_survivable() {
  need_engine || return 0
  need_write_deny || return 0
  mkdir -p "$SB/ro"; chmod 555 "$SB/ro"
  KGAI_STATE_DIR="$SB/ro/state" fire turn-state.sh "$CLAUDE_EDIT"
  assert_rc "exit" "$RC" 0
  assert_json "stdout" "$OUT"
  chmod 755 "$SB/ro"
}

# ======================================================================================
# B. auto-capture-stop.sh — the end-of-turn decision
# ======================================================================================

t_block_when_edited_and_unrecorded() {
  need_engine || return 0
  fire turn-state.sh "$CLAUDE_EDIT"
  fire auto-capture-stop.sh "$(stop_ev s-claude)"
  assert_rc "exit" "$RC" 0
  assert_json "stdout" "$OUT"
  assert_eq "decision" "$(json_field "$OUT" 'd["decision"]')" "block"
  assert_has "reason names the command" "$OUT" "kg ingest"
}

# Gemini fires AfterAgent instead of Stop and spells the verdict "deny" — but documents
# "block" as its alias, which is why one output serves all three hosts.
t_block_on_gemini_afteragent() {
  need_engine || return 0
  fire turn-state.sh "$GEMINI_WRITE"
  fire auto-capture-stop.sh "$(stop_ev s-gem AfterAgent)"
  assert_eq "decision" "$(json_field "$OUT" 'd["decision"]')" "block"
}

t_silent_when_already_recorded() {
  need_engine || return 0
  fire turn-state.sh "$CLAUDE_EDIT"
  fire turn-state.sh "$SHELL_INGEST"
  fire auto-capture-stop.sh "$(stop_ev s-claude)"
  assert_eq "no output" "$OUT" ""
  assert_rc "exit" "$RC" 0
}

t_silent_when_nothing_was_edited() {
  fire auto-capture-stop.sh "$(stop_ev s-quiet)"
  assert_eq "no output" "$OUT" ""
}

t_never_blocks_twice() {
  need_engine || return 0
  fire turn-state.sh "$CLAUDE_EDIT"
  fire auto-capture-stop.sh '{"session_id":"s-claude","hook_event_name":"Stop","stop_hook_active":true}'
  assert_eq "no output" "$OUT" ""
}

# The full block-and-continue cycle, which is the only path where two Stop events belong
# to one turn. Whatever the continuation did is marked under the same session, and the
# second Stop is the last chance to consume it. Miss that and the next turn — a question,
# a read, anything — is told it edited code and owes a decision.
t_continuation_marks_do_not_leak_into_the_next_turn() {
  need_engine || return 0
  fire turn-state.sh "$CLAUDE_EDIT"
  fire auto-capture-stop.sh "$(stop_ev s-claude)"
  assert_has "the turn is blocked" "$OUT" "block"
  # …the model continues, edits once more while wrapping up, and the turn ends again.
  fire turn-state.sh "$CLAUDE_EDIT"
  fire auto-capture-stop.sh '{"session_id":"s-claude","hook_event_name":"Stop","stop_hook_active":true}'
  assert_eq "the continuation is not blocked" "$OUT" ""
  # A completely separate, later turn that touches nothing.
  fire auto-capture-stop.sh "$(stop_ev s-claude)"
  assert_eq "the next turn is left alone" "$OUT" ""
}

# Without this the next turn inherits the previous turn's edits and gets nudged for work
# it did not do — which trains the model to ignore the nudge.
t_marker_is_consumed() {
  need_engine || return 0
  fire turn-state.sh "$CLAUDE_EDIT"
  fire auto-capture-stop.sh "$(stop_ev s-claude)"
  assert_has "first turn blocks" "$OUT" "block"
  fire auto-capture-stop.sh "$(stop_ev s-claude)"
  assert_eq "second turn is silent" "$OUT" ""
}

t_transcript_fallback_still_works() {
  tp="$SB/transcript.jsonl"
  printf '%s\n' '{"type":"user","message":{"content":"move invoice out of pricing"}}' > "$tp"
  printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/p/a.js"}}]}}' >> "$tp"
  fire auto-capture-stop.sh "{\"session_id\":\"s-old\",\"hook_event_name\":\"Stop\",\"transcript_path\":\"$tp\"}"
  assert_eq "decision" "$(json_field "$OUT" 'd["decision"]')" "block"
}

# Codex writes a rollout log with a completely different shape. The fallback must read it
# as "nothing to go on" and let the turn end, never block on a misparse.
t_codex_rollout_is_not_claudes_transcript() {
  tp="$SB/rollout.jsonl"
  printf '%s\n' '{"timestamp":"t","type":"session_meta","payload":{"id":"x"}}' > "$tp"
  printf '%s\n' '{"timestamp":"t","type":"response_item","payload":{"type":"function_call","name":"apply_patch"}}' >> "$tp"
  fire auto-capture-stop.sh "{\"session_id\":\"s-cx\",\"hook_event_name\":\"Stop\",\"transcript_path\":\"$tp\"}"
  assert_eq "no output" "$OUT" ""
}

# ======================================================================================
# C. inject-prompt.sh — the same rules, two output shapes
# ======================================================================================

t_rules_text_mode() {
  KG_STUB_PROMPT="always name the ClickUp ticket"
  fire inject-prompt.sh '{"session_id":"s","hook_event_name":"SessionStart","source":"startup"}'
  assert_has "the rules" "$OUT" "always name the ClickUp ticket"
  assert_hasnt "not JSON-wrapped" "$OUT" "hookSpecificOutput"
}

t_rules_json_mode() {
  KG_STUB_PROMPT="always name the ClickUp ticket"
  export KGAI_HOOK_OUTPUT=json
  fire inject-prompt.sh '{"session_id":"s","hook_event_name":"SessionStart","source":"startup"}'
  assert_json "stdout" "$OUT"
  assert_eq "event" "$(json_field "$OUT" 'd["hookSpecificOutput"]["hookEventName"]')" "SessionStart"
  assert_has "rules survive the wrapping" \
    "$(json_field "$OUT" 'd["hookSpecificOutput"]["additionalContext"]')" "always name the ClickUp ticket"
}

# Capture rules are hand-edited in a committed file. A stray quote or backslash in them
# must not produce JSON the host cannot parse — that would lose the rules AND log a hook
# failure on every session in that repo.
t_rules_json_mode_escapes() {
  KG_STUB_PROMPT='say "why", escape C:\path and a
second line'
  export KGAI_HOOK_OUTPUT=json
  fire inject-prompt.sh '{"session_id":"s","hook_event_name":"SessionStart"}'
  assert_json "stdout" "$OUT"
  assert_has "both lines survive" \
    "$(json_field "$OUT" 'd["hookSpecificOutput"]["additionalContext"]')" "second line"
}

t_no_rules_injects_nothing() {
  KG_STUB_PROMPT=""
  fire inject-prompt.sh '{"session_id":"s","hook_event_name":"SessionStart"}'
  assert_eq "text mode" "$OUT" ""
  export KGAI_HOOK_OUTPUT=json
  fire inject-prompt.sh '{"session_id":"s","hook_event_name":"SessionStart"}'
  assert_eq "json mode" "$OUT" ""
}

# ======================================================================================
# D. session-start.sh — one object on stdout, not three
# ======================================================================================

# A plugin root whose installer is a stand-in: this suite is about the wrapper's output
# shape, and the real installer is covered by the install-* suites.
fake_plugin_root() {
  mkdir -p "$SB/pkg/hooks" "$SB/pkg/scripts"
  cp "$REPO"/hooks/*.sh "$SB/pkg/hooks/"
  printf '#!/usr/bin/env bash\necho "kgai: engine ready (stand-in)"\n' > "$SB/pkg/scripts/install.sh"
  chmod +x "$SB/pkg/scripts/install.sh"
  export KGAI_PLUGIN_ROOT="$SB/pkg"
}

t_session_start_single_object() {
  fake_plugin_root
  KG_STUB_PROMPT="name the ticket"
  OUT="$(printf '%s' '{"session_id":"s","hook_event_name":"SessionStart","source":"startup"}' |
         bash "$SB/pkg/hooks/session-start.sh" 2>/dev/null)"
  assert_json "stdout" "$OUT"
  assert_eq "exactly one line" "$(printf '%s' "$OUT" | grep -c '^')" "1"
  ctx="$(json_field "$OUT" 'd["hookSpecificOutput"]["additionalContext"]')"
  assert_has "carries the installer status" "$ctx" "engine ready"
  assert_has "carries the rules" "$ctx" "name the ticket"
}

# The nested hook must be asked for TEXT. Ask it for JSON and its envelope arrives as a
# literal string inside this one's context — valid JSON that reads as gibberish.
t_session_start_does_not_nest_envelopes() {
  fake_plugin_root
  KG_STUB_PROMPT="name the ticket"
  OUT="$(printf '%s' '{"session_id":"s","hook_event_name":"SessionStart"}' |
         bash "$SB/pkg/hooks/session-start.sh" 2>/dev/null)"
  ctx="$(json_field "$OUT" 'd["hookSpecificOutput"]["additionalContext"]')"
  assert_hasnt "no nested envelope" "$ctx" "hookSpecificOutput"
}

t_session_start_with_nothing_to_say() {
  fake_plugin_root
  printf '#!/usr/bin/env bash\nexit 0\n' > "$SB/pkg/scripts/install.sh"
  KG_STUB_PROMPT=""
  OUT="$(printf '%s' '{"session_id":"s","hook_event_name":"SessionStart"}' |
         bash "$SB/pkg/hooks/session-start.sh" 2>/dev/null)"
  assert_json "stdout" "$OUT"
}

# ======================================================================================
# E. host manifests and the built packages
# ======================================================================================

# Every command a manifest names must exist, or the host logs a hook failure per session.
check_manifest() { # <manifest> <package root> <allowed events…>
  python3 - "$@" <<'PY'
import json, os, re, sys
path, root = sys.argv[1], sys.argv[2]
allowed = set(sys.argv[3:])
m = json.load(open(path, encoding="utf-8"))
problems = []
for event, groups in (m.get("hooks") or {}).items():
    if event not in allowed:
        problems.append("event %r is not one this host fires" % event)
    for g in groups:
        for h in g.get("hooks", []):
            if h.get("type") != "command":
                continue
            cmd = h.get("command", "")
            rel = re.search(r"(hooks|scripts)/[A-Za-z0-9_.-]+\.sh", cmd)
            if not rel:
                problems.append("%s: cannot tell which script %r runs" % (event, cmd))
                continue
            if not os.path.exists(os.path.join(root, rel.group(0))):
                problems.append("%s: %s is not in the package" % (event, rel.group(0)))
            if not h.get("timeout"):
                problems.append("%s: %s has no timeout" % (event, rel.group(0)))
print("\n".join(problems))
PY
}

CLAUDE_EVENTS="SessionStart SessionEnd Stop SubagentStop PreToolUse PostToolUse UserPromptSubmit PreCompact Notification"
CODEX_EVENTS="PreToolUse PermissionRequest PostToolUse PreCompact PostCompact SessionStart SessionEnd SubagentStart SubagentStop UserPromptSubmit Stop Interrupt"
GEMINI_EVENTS="BeforeTool AfterTool BeforeAgent AfterAgent BeforeModel BeforeToolSelection AfterModel SessionStart SessionEnd Notification PreCompress"

t_claude_manifest() {
  assert_eq "problems" "$(check_manifest "$REPO/hooks/hooks.json" "$REPO" $CLAUDE_EVENTS)" ""
}

t_codex_manifest() {
  assert_eq "problems" "$(check_manifest "$REPO/hosts/codex/hooks.json" "$REPO" $CODEX_EVENTS)" ""
}

t_gemini_manifest() {
  assert_eq "problems" "$(check_manifest "$REPO/hosts/gemini/hooks/hooks.json" "$REPO" $GEMINI_EVENTS)" ""
}

# Gemini measures hook timeouts in MILLISECONDS; Claude and Codex in seconds. Copying a
# manifest across without converting gives every Gemini hook a 20 ms budget, which kills
# the installer and the capture nudge on every turn — silently.
t_gemini_timeouts_are_milliseconds() {
  local lo
  lo="$(python3 -c "
import json
m = json.load(open('$REPO/hosts/gemini/hooks/hooks.json'))
print(min(h['timeout'] for gs in m['hooks'].values() for g in gs for h in g['hooks']))
")"
  [ "${lo:-0}" -ge 1000 ] ||
    _fail "shortest Gemini timeout is ${lo:-none}; milliseconds means every value is >= 1000"
}

# The host packages are assembled, not committed, so the build is what has to be tested:
# a package missing one script is a plugin that half-works on a user's machine.
t_built_packages_are_self_contained() {
  if ! bash "$REPO/scripts/build-host-pkg.sh" --out "$SB/dist" >/dev/null 2>&1; then
    _fail "build-host-pkg.sh failed"
    return
  fi
  local host
  for host in codex gemini; do
    assert_exists "$host hooks" "$SB/dist/$host/hooks/hooks.json"
    assert_exists "$host skill" "$SB/dist/$host/skills/knowledge-graph/SKILL.md"
    assert_exists "$host installer" "$SB/dist/$host/scripts/install.sh"
    assert_eq "$host manifest problems" \
      "$(check_manifest "$SB/dist/$host/hooks/hooks.json" "$SB/dist/$host" $CODEX_EVENTS $GEMINI_EVENTS)" ""
  done
  assert_exists "gemini manifest" "$SB/dist/gemini/gemini-extension.json"
  assert_exists "gemini commands are TOML" "$SB/dist/gemini/commands/kg-ask.toml"
  assert_exists "gemini context file" "$SB/dist/gemini/GEMINI.md"
  assert_exists "codex manifest" "$SB/dist/codex/plugin.json"
  assert_exists "codex commands stay Markdown" "$SB/dist/codex/commands/kg-ask.md"
}

# Every host package must claim the version the plugin actually ships, or the engine and
# the host report two different numbers and nobody can tell which one is installed.
t_built_packages_carry_the_version() {
  local v
  v="$(python3 -c "import json;print(json.load(open('$REPO/.claude-plugin/plugin.json'))['version'])")"
  bash "$REPO/scripts/build-host-pkg.sh" --out "$SB/dist" >/dev/null 2>&1
  assert_eq "codex version" \
    "$(python3 -c "import json;print(json.load(open('$SB/dist/codex/plugin.json'))['version'])")" "$v"
  assert_eq "gemini version" \
    "$(python3 -c "import json;print(json.load(open('$SB/dist/gemini/gemini-extension.json'))['version'])")" "$v"
}

# Three manifests now describe the same plugin — Claude Code's, the portable one Codex
# reads at the repo root, and the Gemini extension's. A user who installs from the same
# commit on two hosts and is told two different versions has no way to tell which one is
# lying.
t_manifests_agree() {
  local out
  out="$(python3 - "$REPO" <<'PY'
import json, os, sys
repo = sys.argv[1]
claude = json.load(open(os.path.join(repo, ".claude-plugin", "plugin.json")))
portable = json.load(open(os.path.join(repo, "plugin.json")))
gem = json.load(open(os.path.join(repo, "hosts", "gemini", "gemini-extension.json")))
problems = []
for name, m in (("plugin.json", portable), ("hosts/gemini/gemini-extension.json", gem)):
    for key in ("name", "version", "description"):
        if m.get(key) != claude.get(key):
            problems.append("%s disagrees with .claude-plugin/plugin.json on %r" % (name, key))
hooks = (portable.get("extensions", {}).get("com.openai", {}) or {}).get("hooks")
if not hooks:
    problems.append("plugin.json does not point Codex at a hooks manifest")
elif not os.path.exists(os.path.join(repo, hooks.lstrip("./"))):
    problems.append("plugin.json points Codex at %s, which does not exist" % hooks)
print("\n".join(problems))
PY
)"
  assert_eq "problems" "$out" ""
}

# tests/host-smoke.sh costs model tokens, so its own wiring cannot be checked by running
# it for real. This drives it against a stand-in host binary: no model, no network, but it
# proves the harness actually assembles and launches a command. The bug it pins:
# prepare_host was called from a pipeline, whose subshell swallowed the RUN_CMD
# assignment — every run then came back rc=125 from `timeout`'s usage message, which reads
# exactly like the plugin failing on that host.
t_smoke_harness_launches_its_host() {
  local out rc
  out="$(KGAI_SMOKE_CLAUDE=/bin/true \
         KGAI_SMOKE_WORK="$SB/smoke" \
         KGAI_SMOKE_TIMEOUT=30 \
         KGAI_HOME="$SB/.kgai" \
         bash "$REPO/tests/host-smoke.sh" claude trivial 1 2>&1)"
  rc=$?
  assert_hasnt "a command was assembled" "$out" "produced no command"
  assert_has "the run is reported" "$out" "run1"
  # `trivial` expects zero decisions and the stand-in host records none, so the harness
  # itself must come back green — anything else means it failed before reaching the host.
  assert_rc "harness verdict" "$rc" 0
}

# Gemini interpolates {{args}} and ignores $ARGUMENTS. A command that still says
# $ARGUMENTS asks the model to answer about the literal string "$ARGUMENTS".
t_gemini_commands_use_gemini_arguments() {
  bash "$REPO/scripts/build-host-pkg.sh" gemini --out "$SB/dist" >/dev/null 2>&1
  assert_file_has "converted" "$SB/dist/gemini/commands/kg-ask.toml" "{{args}}"
  assert_file_hasnt "no Claude placeholder left" "$SB/dist/gemini/commands/kg-ask.toml" "\$ARGUMENTS"
  assert_file_has "description survives the frontmatter" "$SB/dist/gemini/commands/kg-ask.toml" "description ="
}

# ======================================================================================

suite_header "hooks-contract"

section "A. turn-state.sh — what this turn did"
run "Claude Edit marks an edit"                          t_mark_claude_edit
run "Codex apply_patch marks an edit"                    t_mark_codex_patch
run "Gemini write_file marks an edit"                    t_mark_gemini_write
run "an in-place shell edit marks an edit"               t_mark_shell_edit
run "kg ingest in a command marks a recording"           t_mark_ingest
run "kg ingest in tool OUTPUT is not a recording"        t_ingest_in_output_is_not_a_recording
run "an unrelated tool marks nothing"                    t_unrelated_tool_marks_nothing
run "hook output is Codex-safe (no suppressOutput)"      t_output_is_codex_safe
run "marks even when HOME is stripped from the env"      t_marks_without_home_in_env
run "a >/dev/null redirect is not an edit"               t_silencing_redirect_is_not_an_edit
run "sessions do not bleed into each other"              t_sessions_are_separate
run "an unwritable state dir never breaks the tool call" t_unwritable_state_dir_is_survivable

section "B. auto-capture-stop.sh — the end-of-turn decision"
run "edited and unrecorded → the turn is blocked"        t_block_when_edited_and_unrecorded
run "Gemini AfterAgent gets the same verdict"            t_block_on_gemini_afteragent
run "already recorded → the turn ends silently"          t_silent_when_already_recorded
run "no edits → the turn ends silently"                  t_silent_when_nothing_was_edited
run "stop_hook_active → never blocks twice in a row"     t_never_blocks_twice
run "continuation marks never leak into the next turn"   t_continuation_marks_do_not_leak_into_the_next_turn
run "the marker is consumed, next turn starts clean"     t_marker_is_consumed
run "no marker → Claude's transcript still works"        t_transcript_fallback_still_works
run "a Codex rollout is not read as Claude's transcript" t_codex_rollout_is_not_claudes_transcript

section "C. inject-prompt.sh — the rules, in two shapes"
run "text mode is what Claude Code always received"      t_rules_text_mode
run "json mode wraps the same text"                      t_rules_json_mode
run "json mode survives quotes, backslashes, newlines"   t_rules_json_mode_escapes
run "no rules configured → nothing is injected"          t_no_rules_injects_nothing

section "D. session-start.sh — one object, not three"
run "the wrapper emits exactly one JSON object"          t_session_start_single_object
run "envelopes are not nested"                           t_session_start_does_not_nest_envelopes
run "nothing to say is still valid JSON"                 t_session_start_with_nothing_to_say

section "E. host manifests and the built packages"
run "the Claude Code manifest resolves"                  t_claude_manifest
run "the Codex manifest uses Codex events"               t_codex_manifest
run "the Gemini manifest uses Gemini events"             t_gemini_manifest
run "Gemini timeouts are in milliseconds"                t_gemini_timeouts_are_milliseconds
run "all three manifests describe the same plugin"       t_manifests_agree
run "each built package is self-contained"               t_built_packages_are_self_contained
run "each built package carries the version"             t_built_packages_carry_the_version
run "Gemini commands use Gemini's argument syntax"       t_gemini_commands_use_gemini_arguments
run "the smoke harness actually launches its host"       t_smoke_harness_launches_its_host

summary
