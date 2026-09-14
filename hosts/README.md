# kgai on hosts other than Claude Code

The engine (`kg`) has never cared which agent calls it — it is a CLI that prints JSON.
What is host-specific is the wiring around it: how the skill is discovered, what the
slash commands look like, and above all which lifecycle events the host fires and what
shape it expects a hook to answer in.

That wiring now exists for three hosts.

| | Claude Code | Codex CLI | Gemini CLI |
|---|---|---|---|
| manifest | `.claude-plugin/plugin.json` | `plugin.json` (root, Agent Plugins schema) | `gemini-extension.json` |
| skill | `skills/knowledge-graph/` | same file, `skills/` | same file, `skills/` |
| commands | `commands/*.md` | `commands/*.md` (argument-taking ones are dropped) | `commands/*.toml` |
| always-on context | the skill description | the skill description | `GEMINI.md` |
| session start | `SessionStart` | `SessionStart` | `SessionStart` |
| tool observed | `PostToolUse` | `PostToolUse` | `AfterTool` |
| end of turn | `Stop` | `Stop` | `AfterAgent` |
| hook stdout | text or JSON | text or JSON | **JSON only** |
| hook timeout unit | seconds | seconds | **milliseconds** |

## What is shared, and why

`hooks/*.sh` and `scripts/install.sh` are the same files on every host. Two changes made
that possible:

**The turn is observed as it happens.** Auto-capture needs to know whether the turn
edited code and whether the model already recorded a decision. That used to be read out
of Claude Code's transcript JSONL at end of turn — which cannot work anywhere else:
Codex writes a rollout log in an unrelated shape (`type: response_item`) and Gemini's
`AfterAgent` event carries no transcript at all. So `hooks/turn-state.sh` marks each edit
and each `kg ingest` while the turn runs, and `hooks/auto-capture-stop.sh` only has to
read it back. Claude's transcript parser is still there as a fallback for a session where
no marker was written.

The reading is the engine's job — `kg turn mark` and `kg turn take`, taking the hook event
payload on stdin. It sits there rather than in the hook scripts because this runs after
every matching tool call: shelling out to python to parse one JSON object cost 24 ms,
where the whole Go binary starts and answers in 6, and on a machine without python the
hook silently did nothing at all. `internal/turn` holds the classification and its tests;
the hooks are now thin enough to read in one screen.

An engine too old to know `kg turn` fails softly — the mark is skipped, no marker is
written, and the end-of-turn hook falls back to the transcript. Claude Code keeps working
either way; the other two hosts simply do not nudge until the engine is current.

**One output shape.** All three hosts accept `{"hookSpecificOutput": {"additionalContext":
…}}` at session start and `{"decision": "block", "reason": …}` at end of turn (Gemini
spells it "deny" and documents "block" as its alias). `inject-prompt.sh` emits the JSON
form when `KGAI_HOOK_OUTPUT=json`; Claude Code keeps getting the plain text it always got.

`hosts/<host>/` holds only what genuinely differs: the manifest and the event wiring.

## Building and installing

Claude Code installs this repository as-is. The other two copy the directory they are
pointed at, so each gets a self-contained package:

```bash
bash scripts/build-host-pkg.sh          # → dist/codex/ and dist/gemini/
```

**Codex CLI** reads the repo directly — `.claude-plugin/marketplace.json` is a format it
accepts, and the root `plugin.json` points it at `hosts/codex/hooks.json` instead of
Claude's event names:

```bash
codex plugin marketplace add kgaidev/kgai      # or a local path, for development
codex plugin add kgai@kgai-marketplace
```

Codex will not run a plugin's hooks until they have been reviewed and trusted — the
first session asks. Until then the skill and commands work and auto-capture does not.

**Gemini CLI** installs the built extension:

```bash
gemini extensions link  ./dist/gemini    # development: edits are picked up live
gemini extensions install ./dist/gemini  # or a GitHub URL, for a real install
```

Gemini asks before running each `kg` command, since they go through
`run_shell_command`. Adding `kg` to the workspace policy removes the prompting.

## Testing

Three layers, cheapest first.

```bash
bash tests/hooks-contract.sh        # no host, no model, no network — runs in CI
bash tests/host-smoke.sh codex      # a real session on a real host — costs tokens
```

`tests/hooks-contract.sh` feeds each host's documented event payload to the hook scripts
and checks the exact output that host accepts, including the traps that are silent in
production: Gemini's millisecond timeouts, a manifest naming a script that is not in the
package, and `kg ingest` appearing in tool *output* (it is in this plugin's own skill
file) being mistaken for a recorded decision.

`tests/host-smoke.sh` is the part a contract test cannot reach: whether the host actually
fires the hooks, and whether the model then reaches for `kg`. It runs one real task in a
throwaway project and reads the verdict out of the **store** — a model that says it
recorded a decision and a model that recorded one produce identical output; only the
graph knows the difference. Two tasks: `struct` must produce exactly one decision,
`trivial` must produce none.

It stages the engine under test (a fresh build of `src/`, not the released `~/.kgai`) in
a per-run home, and stamps a matching `.srcver` so the SessionStart installer does not
replace it with the released download mid-test.

## Verified

Smoke-tested end to end, real model, decision read back from the store — all three via
the same Go marker path (`kg turn mark` while the turn runs, `kg turn take` at the end,
hook log `source=marker`):

- **Claude Code** — `struct` → 1 decision, `edited=3 recorded=true`.
- **Gemini CLI** — `struct` → 1 decision, `edited=2 recorded=true`. Gemini passes the
  hook its environment, so `KGAI_*` reach the scripts.

What each host quirk cost, learned here rather than in production:

- **Gemini** no longer accepts a personal Google login for the CLI ("migrate to
  Antigravity") — a `GEMINI_API_KEY` (AI Studio) is the way in, named in
  `security.auth.selectedType`. Its free tier is 5 requests/min, so multi-run smokes
  crawl behind 40–50 s backoffs.
- **Codex does NOT pass its own process environment to plugin hooks** — only the
  host-injected `CLAUDE_PLUGIN_ROOT`/`PLUGIN_ROOT` (+`_DATA`) and standard vars. A hook
  that needs a value must read it from those or from its own defaults, never from a
  variable the launching shell exported. The kgai hooks already do (`KGAI_HOME` defaults
  to `$HOME/.kgai`), so this bites the *smoke harness* — which staged the engine via
  `KGAI_HOME` — not the plugin. Codex hook firing itself is reliable once a session
  starts (5/5), and the real manifest's `bash "${CLAUDE_PLUGIN_ROOT}/hooks/…"` form works.

## Codex: the root `plugin.json` suppresses hook loading (unresolved)

Isolated probes settle where Codex looks for hooks:

| plugin shape | hooks fired? |
| --- | --- |
| `.claude-plugin/plugin.json` + `hooks/hooks.json` (Claude shape) | **yes** |
| the above **plus** a root Agent-Plugins `plugin.json` (kgai's current shape) | **no — from neither location** |

With the root `plugin.json` present, Codex fires nothing — not the `extensions.com.openai.hooks`
pointer at `hosts/codex/hooks.json`, not even the default `hooks/hooks.json`. Remove the
root manifest and the default `hooks/hooks.json` fires. So on Codex today the kgai skill
and commands load and the model records on its own, but the deterministic lifecycle hooks
(session-start, turn-state, auto-capture) never run.

The likely fix is to drop the root `plugin.json` for the Codex distribution and let Codex
read `.claude-plugin/` + a single `hooks/hooks.json` — Codex shares Claude's event names
(SessionStart / PostToolUse / Stop / SessionEnd) and accepts the same JSON hook output, so
one manifest can serve both. That collapses `hosts/codex/` into the default location and
removes the Agent-Plugins manifest that publishing may later want back — a structural
call, not yet made.

Still unconfirmed underneath it: whether Codex passes `HOME` to hooks. If it does not,
`scripts/install.sh` stops at its `HOME` guard and the engine never installs on Codex, so
the SessionStart install may need to anchor on `CLAUDE_PLUGIN_DATA` instead.

## Known gaps
- **Codex drops argument-taking commands.** It migrates bundled Claude commands into
  skills, but only the ones that take no arguments — `kg-sync` and `kg-trust` survive,
  the other six do not. Their content is not lost (the knowledge-graph skill carries the
  same instructions), but `/kg-ask …` does not exist there.
- **Nothing is published yet.** Codex's plugin directory has no self-serve publishing,
  and the Gemini extension needs its own repository root before
  `gemini extensions install <github url>` can work.
- **No MCP server.** Both CLIs here have a shell, so the `kg` CLI is enough. Editors that
  do not (Cursor, Windsurf, the ChatGPT desktop app) would need one.

- **Codex drops argument-taking commands.** It migrates bundled Claude commands into
  skills, but only the ones that take no arguments — `kg-sync` and `kg-trust` survive,
  the other six do not. Their content is not lost (the knowledge-graph skill carries the
  same instructions), but `/kg-ask …` does not exist there.
- **Nothing is published yet.** Codex's plugin directory has no self-serve publishing,
  and the Gemini extension needs its own repository root before
  `gemini extensions install <github url>` can work.
- **No MCP server.** Both CLIs here have a shell, so the `kg` CLI is enough. Editors that
  do not (Cursor, Windsurf, the ChatGPT desktop app) would need one.
