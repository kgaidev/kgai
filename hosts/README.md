# kgai on hosts other than Claude Code

The engine (`kg`) has never cared which agent calls it — it is a CLI that prints JSON.
What is host-specific is the wiring around it: how the skill is discovered, what the
slash commands look like, and which lifecycle events the host fires in what shape.

That wiring now covers three hosts. **Claude Code and Codex CLI install this repository
directly** — both read `.claude-plugin` plugins and share the same event names, so one
`hooks/hooks.json` serves both and there is nothing to build. **Gemini CLI** is the outlier
and gets a built package (`dist/gemini/`).

| | Claude Code | Codex CLI | Gemini CLI |
|---|---|---|---|
| manifest | `.claude-plugin/plugin.json` | same (installs the repo) | `gemini-extension.json` |
| marketplace | `.claude-plugin/marketplace.json` | same | — (`extensions install <url>`) |
| skill | `skills/knowledge-graph/` | same | same |
| commands | `commands/*.md` | `commands/*.md` (argument-taking ones dropped) | `commands/*.toml` |
| always-on context | the skill description | the skill description | `GEMINI.md` |
| session start | `SessionStart` | `SessionStart` | `SessionStart` |
| tool observed | `PostToolUse` | `PostToolUse` | `AfterTool` |
| end of turn | `Stop` | `Stop` | `AfterAgent` |
| hook stdout | text or JSON | JSON (`{}` when nothing to report) | **JSON only** |
| hook timeout unit | seconds | seconds | **milliseconds** |

## What is shared, and why

`hooks/*.sh`, `scripts/install.sh` and `hooks/hooks.json` are the same files on Claude and
Codex. Three things made that possible:

**The turn is observed as it happens.** Whether a turn edited code and whether the model
already recorded a decision used to be read out of Claude Code's transcript JSONL at end
of turn — unportable, since Codex writes an unrelated rollout format and Gemini's
`AfterAgent` event carries no transcript at all. `hooks/turn-state.sh` marks each edit and
each `kg ingest` while the turn runs, through `kg turn mark`; `hooks/auto-capture-stop.sh`
reads it back with `kg turn take`. Claude's transcript parse remains a fallback. Logic +
tests in `internal/turn`.

**One output shape.** All three hosts accept `{"hookSpecificOutput": {"additionalContext":
…}}` at session start and `{"decision": "block", "reason": …}` at end of turn (Gemini
spells it "deny", documents "block" as its alias). The "nothing to report" reply is `{}`
— Claude and Gemini also honor `{"suppressOutput": true}` but Codex rejects that key.

**Hooks don't depend on the launching shell's environment.** Codex does pass `HOME` and
`PATH`, but strips custom vars; the hooks and `install.sh` derive `HOME` from the passwd
database when it is unset and fall back to the `kg` launcher on `PATH`, so a stripped
environment can't leave them unable to find the engine.

## Two Codex traps, both learned the hard way

- **A root Agent-Plugins `plugin.json` makes Codex 0.154 load NO hooks for the plugin** —
  not the `extensions.com.openai.hooks` pointer, not even the default `hooks/hooks.json`.
  A plugin with only `.claude-plugin/` fires hooks correctly. So this repo does **not**
  carry a root `plugin.json`; a test (`tests/hooks-contract.sh`) fails if one appears.
- **Codex's tools are named `apply_patch` (edit) and `exec` (shell)**, which the original
  Claude matcher (`Edit|Write|…|Bash`) never named — so PostToolUse never fired. The
  unified `hooks/hooks.json` PostToolUse matcher names every host's tools, and the engine's
  classifier knows `exec`.

## Installing

**Claude Code** — the repository's own plugin system.

**Codex CLI** (needs ≥ 0.150 for the `plugin` subcommand):

```bash
codex plugin marketplace add kgaidev/kgai      # or a local path, for development
codex plugin add kgai@kgai-marketplace
```

Codex will not run a plugin's hooks until they are reviewed and trusted — the first
session asks. Until then the skill and commands work and auto-capture does not.

**Gemini CLI** installs the built extension:

```bash
bash scripts/build-host-pkg.sh                 # → dist/gemini/
gemini extensions link  ./dist/gemini          # development: edits picked up live
gemini extensions install ./dist/gemini        # or a GitHub URL, for a real install
```

Gemini asks before running each `kg` command, since they go through `run_shell_command`;
adding `kg` to the workspace policy removes the prompting. A personal Google login is no
longer eligible for the CLI ("migrate to Antigravity") — a `GEMINI_API_KEY` from AI Studio
is the way in.

## Testing

```bash
bash tests/hooks-contract.sh                    # no host, no model, no network — CI
bash tests/host-smoke.sh codex struct           # a real session on a real host — costs tokens
```

`tests/hooks-contract.sh` feeds each host's documented event payload to the hook scripts
and checks the exact output that host accepts, plus the traps that are silent in
production: Gemini's millisecond timeouts, a matcher that misses Codex's tools, a root
`plugin.json` reappearing, `{"suppressOutput": true}` creeping back, and `kg ingest`
appearing in tool *output* (it is in this plugin's own skill file) being mistaken for a
recorded decision.

`tests/host-smoke.sh` reaches what a contract test cannot: whether the host actually fires
the hooks and the model reaches for `kg`. It runs one real task in a throwaway project and
reads the verdict out of the **store**. `struct` must produce exactly one decision,
`trivial` none. It stages a fresh build of `src/` (not the released `~/.kgai`) and stamps a
matching `.srcver` so the installer does not swap it mid-test.

## Verified

Auto-capture confirmed end to end, real model, decision (or its correct absence) read from
the store, via the `kg turn` marker path — on **all three hosts**: Claude Code, Codex CLI,
and Gemini CLI.

## Known gaps

- **Codex drops argument-taking commands.** It migrates bundled Claude commands into
  skills, but only the ones that take no arguments — `kg-sync` and `kg-trust` survive, the
  other six do not. Their content is not lost (the knowledge-graph skill carries the same
  instructions), but `/kg-ask …` does not exist there.
- **No official directory listing yet.** Codex's plugin directory is approval-only, and
  the Gemini extension needs its own repository root before
  `gemini extensions install <github url>` can work. Both install today from a git repo.
- **No MCP server.** All three hosts here have a shell, so the `kg` CLI is enough. Editors
  that do not (Cursor, Windsurf, the ChatGPT desktop app) would need one.
