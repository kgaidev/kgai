#!/usr/bin/env bash
# tests/host-smoke.sh — does auto-capture actually fire, on a real host, with a real model?
#
#   bash tests/host-smoke.sh claude            # one run, the structural task
#   bash tests/host-smoke.sh codex trivial     # …and the task that must record NOTHING
#   bash tests/host-smoke.sh gemini struct 5   # five runs, for a capture rate
#
# COSTS MODEL TOKENS and uses whatever account the host CLI is logged in as. Nothing here
# runs from tests/run.sh; it is invoked by hand, deliberately.
#
# What it measures. The contract suite proves the hooks answer correctly when fed an
# event. It cannot prove the host FIRES them — that a Codex Stop hook is reached, that
# Gemini's AfterAgent block actually turns into another model turn, that the model then
# reaches for `kg`. Those only show up against the real thing, and they are exactly the
# failures that are invisible from the inside: nothing errors, decisions just stop being
# recorded.
#
# The measurement is the STORE, never the transcript. A model that says "recorded the
# decision" and a model that recorded it read identically in the output; only the graph
# knows the difference.
#
#   struct   a real structural move (invoice leaves pricing) → expect exactly 1 decision
#   trivial  a cosmetic rename                                → expect 0 decisions
#
# Isolation: a throwaway project (so the store is the project's own .kgai/store), a
# private config home per host, and the engine from the real ~/.kgai. It writes nothing
# into the repository and nothing into the user's real project stores. It DOES copy the
# host's credential file into the throwaway config home, because that is what letting a
# CLI run non-interactively takes.
set -uo pipefail
REPO="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"

HOST="${1:-}"
TASK_TYPE="${2:-struct}"
RUNS="${3:-1}"
case "$HOST" in
  claude|codex|gemini) ;;
  *) sed -n '2,12p' "${BASH_SOURCE[0]}"; exit 2 ;;
esac
case "$TASK_TYPE" in struct|trivial) ;; *) echo "task type must be struct or trivial" >&2; exit 2 ;; esac

CLAUDE_BIN="${KGAI_SMOKE_CLAUDE:-claude}"
CODEX_BIN="${KGAI_SMOKE_CODEX:-codex}"
GEMINI_BIN="${KGAI_SMOKE_GEMINI:-gemini}"
REAL_ENGINE_HOME="${KGAI_HOME:-$HOME/.kgai}"
WORK="${KGAI_SMOKE_WORK:-${TMPDIR:-/tmp}/kgai-host-smoke}"
TIMEOUT="${KGAI_SMOKE_TIMEOUT:-600}"

# ---- the engine under test ------------------------------------------------------------
# What sits in the real ~/.kgai is the RELEASED engine; measuring against it would test
# yesterday's binary against today's hooks. Same resolution as tests/hooks-contract.sh:
# an explicit KGAI_TEST_ENGINE, else a build of src/ cached in dist/test-engine.
TEST_KG="${KGAI_TEST_ENGINE:-}"
if [ -z "$TEST_KG" ]; then
  TEST_KG="$REPO/dist/test-engine/kg"
  if [ ! -x "$TEST_KG" ] || [ -n "$(find "$REPO/src" -name '*.go' -newer "$TEST_KG" 2>/dev/null | head -n1)" ]; then
    command -v go >/dev/null 2>&1 || { echo "no engine build available: set KGAI_TEST_ENGINE, or install go" >&2; exit 1; }
    mkdir -p "$(dirname "$TEST_KG")"
    ( cd "$REPO/src" && go build -o "$TEST_KG" . ) || exit 1
  fi
fi
# The native lib the staged engine loads: the installed one when there is an install,
# else the checkout's own copy — the same one the source build links against.
LIB_SRC="$REAL_ENGINE_HOME/lib"
if [ ! -e "$LIB_SRC/libkuzu.so" ] && [ ! -e "$LIB_SRC/libkuzu.dylib" ]; then
  case "$(uname -s)-$(uname -m)" in
    Linux-x86_64)  LIB_SRC="$REPO/third_party/go-kuzu/lib/dynamic/linux-amd64" ;;
    Linux-aarch64) LIB_SRC="$REPO/third_party/go-kuzu/lib/dynamic/linux-arm64" ;;
    Darwin-*)      LIB_SRC="$REPO/third_party/go-kuzu/lib/dynamic/darwin" ;;
  esac
fi
[ -e "$LIB_SRC/libkuzu.so" ] || [ -e "$LIB_SRC/libkuzu.dylib" ] ||
  { echo "no libkuzu found (looked in $REAL_ENGINE_HOME/lib and the checkout) — run scripts/fetch-libs.sh or a Claude Code session once" >&2; exit 1; }

# Stage a private engine home for one run: the built binary, the native lib, and the
# fingerprint the PACKAGE's installer would compute. The last part is what keeps the
# SessionStart hook honest — without a matching .srcver it decides the engine is stale and
# replaces the build under test with the released download, mid-test, silently.
stage_engine_home() { # <dir> <plugin root>
  local home="$1" root="$2"
  mkdir -p "$home/bin" "$home/lib" "$home/run"
  cp "$TEST_KG" "$home/bin/kg"
  cp "$LIB_SRC"/libkuzu.* "$home/lib/" 2>/dev/null
  KGAI_INSTALL_LIB=1 KGAI_HOME="$home" CLAUDE_PLUGIN_ROOT="$root" \
    bash -c '. "$CLAUDE_PLUGIN_ROOT/scripts/install.sh"; srcver' > "$home/.srcver" 2>/dev/null
}

# ---- the two tasks --------------------------------------------------------------------
# Both edit code, so both reach the end-of-turn hook. Only one of them is a decision —
# which is the point: a capture nudge that fires on everything is noise, and a plugin that
# records the trivial task is worse than one that records nothing.
seed_project() { # <dir>
  mkdir -p "$1/src/pricing"
  printf 'export function renderInvoice(order){return `<invoice>${order.id} ${order.total}</invoice>`;}\n' > "$1/src/pricing/invoice.js"
  printf "import {renderInvoice} from './invoice.js';\nexport function pricingView(o){return renderInvoice(o);}\n" > "$1/src/pricing/index.js"
  printf '# demo\n' > "$1/README.md"
  ( cd "$1" && git init -q && git add -A && git -c user.email=t@t -c user.name=t commit -qm init ) >/dev/null 2>&1
}

task_text() {
  if [ "$TASK_TYPE" = struct ]; then
    printf '%s' "Invoice rendering currently lives inside the pricing module (src/pricing/invoice.js, imported by src/pricing/index.js). We have decided invoices should be a standalone module independent of pricing. Refactor: create src/invoice/invoice.js with the rendering code, update src/pricing/index.js to import from the new location, and remove the invoice code from pricing. Do the file edits now; do not just describe the plan."
  else
    printf '%s' "In src/pricing/invoice.js rename the parameter 'order' to 'o' (cosmetic only, no behavior change) and update the body. Do the edit now."
  fi
}

# ---- per-host wiring ------------------------------------------------------------------
# Each host installs the plugin its own way. The packages come from scripts/build-host-pkg.sh
# so that what is measured here is what a user would actually install.

prepare_host() { # <run dir>  → sets RUN_CMD, echoes what it wired
  local run="$1" proj="$1/proj"
  case "$HOST" in
    claude)
      stage_engine_home "$run/engine" "$REPO"
      export KGAI_HOME="$run/engine"
      RUN_CMD=("$CLAUDE_BIN" -p "$(task_text)" --plugin-dir "$REPO" --dangerously-skip-permissions)
      [ -n "${KGAI_SMOKE_MODEL:-}" ] && RUN_CMD+=(--model "$KGAI_SMOKE_MODEL")
      echo "claude: --plugin-dir $REPO, engine $run/engine"
      ;;
    codex)
      stage_engine_home "$run/engine" "$REPO"
      # Codex does NOT pass the launching shell's environment to hooks (only its own
      # injected CLAUDE_PLUGIN_ROOT/PLUGIN_ROOT and standard vars), so exporting KGAI_HOME
      # would never reach turn-state.sh / auto-capture-stop.sh — they would fall back to
      # the default $HOME/.kgai, i.e. the RELEASED engine, and the run would silently test
      # the wrong binary. Instead put the staged engine AT that default: override HOME and
      # symlink its .kgai. auth still comes from CODEX_HOME, which is set independently.
      cp "$OLDHOME/.codex/auth.json" "$run/auth.json" 2>/dev/null ||
        echo "  ! no ~/.codex/auth.json — run 'codex login' first" >&2
      export HOME="$run/home"; mkdir -p "$HOME"
      ln -snf "$run/engine" "$HOME/.kgai"
      export KGAI_HOME="$run/engine"   # for the parent-side verdict reads below
      export CODEX_HOME="$run/codex-home"
      mkdir -p "$CODEX_HOME"
      cp "$run/auth.json" "$CODEX_HOME/auth.json" 2>/dev/null
      "$CODEX_BIN" plugin marketplace add "$REPO" >/dev/null 2>&1
      "$CODEX_BIN" plugin add kgai@kgai-marketplace >/dev/null 2>&1 ||
        echo "  ! codex plugin add failed" >&2
      # --dangerously-bypass-hook-trust: Codex will not run a plugin's hooks until a human
      # has reviewed them in the TUI, and a non-interactive run has nobody to ask. Without
      # it the session is green and every kgai hook silently never fires — which would make
      # this harness measure the model's memory instead of the plugin.
      RUN_CMD=("$CODEX_BIN" exec --cd "$proj"
               --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust)
      [ -n "${KGAI_SMOKE_MODEL:-}" ] && RUN_CMD+=(-m "$KGAI_SMOKE_MODEL")
      RUN_CMD+=("$(task_text)")
      echo "codex: plugin kgai@kgai-marketplace in $CODEX_HOME"
      ;;
    gemini)
      bash "$REPO/scripts/build-host-pkg.sh" gemini --out "$run/dist" >/dev/null || exit 1
      stage_engine_home "$run/engine" "$run/dist/gemini"
      export KGAI_HOME="$run/engine"
      export HOME="$run/home"
      mkdir -p "$HOME/.gemini"
      # Gemini sanitizes the environment its extension hooks run in, so KGAI_HOME may not
      # survive the trip — but $HOME does, and the hooks default to $HOME/.kgai. Same
      # engine either way. The STORE still lands in the project.
      ln -snf "$run/engine" "$HOME/.kgai"
      cp "$OLDHOME/.gemini/oauth_creds.json" "$HOME/.gemini/" 2>/dev/null
      cp "$OLDHOME/.gemini/google_accounts.json" "$HOME/.gemini/" 2>/dev/null
      # The credential file alone is not enough: a headless run has nobody to pick an auth
      # method from the menu, and without one Gemini stops before it ever loads the
      # extension. Naming it in settings.json is what the interactive picker would write.
      python3 - "$HOME/.gemini/settings.json" "$OLDHOME/.gemini/settings.json" <<'PY'
import json, sys
dest, src = sys.argv[1], sys.argv[2]
try:
    cfg = json.load(open(src, encoding="utf-8"))
except Exception:
    cfg = {}
import os
# A personal Google login is no longer eligible for gemini-cli on the individual tier
# ("migrate to Antigravity"), so an AI Studio key is the way in when one is provided.
kind = "gemini-api-key" if os.environ.get("GEMINI_API_KEY") else "oauth-personal"
cfg.setdefault("security", {}).setdefault("auth", {})["selectedType"] = kind
cfg.pop("mcpServers", None)   # the user's own servers have no business in a test sandbox
json.dump(cfg, open(dest, "w", encoding="utf-8"), indent=2)
PY
      "$GEMINI_BIN" extensions link "$run/dist/gemini" --consent >/dev/null 2>&1 ||
        echo "  ! gemini extensions link failed" >&2
      # --skip-trust: an untrusted folder silently downgrades --yolo back to prompting,
      # and a headless run then hangs on the first approval instead of editing anything.
      RUN_CMD=("$GEMINI_BIN" -p "$(task_text)" --yolo --skip-trust)
      [ -n "${KGAI_SMOKE_MODEL:-}" ] && RUN_CMD+=(-m "$KGAI_SMOKE_MODEL")
      echo "gemini: extension linked from $run/dist/gemini"
      ;;
  esac
}

# ---- run ------------------------------------------------------------------------------
OLDHOME="$HOME"
rm -rf "$WORK"; mkdir -p "$WORK"
echo "kgai host smoke — $HOST / $TASK_TYPE / ${RUNS} run(s)"
echo "engine under test: $TEST_KG   work: $WORK"

recorded_total=0
for i in $(seq 1 "$RUNS"); do
  run="$WORK/run$i"; proj="$run/proj"
  mkdir -p "$proj"
  seed_project "$proj"

  ( # a subshell per run: HOME and CODEX_HOME overrides never leak into the next one
    export KGAI_HOOK_DEBUG="$run/hooks.log"
    export KGAI_STATE_DIR="$run/state"
    # NOT piped into sed: a pipeline runs in its own subshell, and RUN_CMD set there
    # never reaches this one — `timeout` would then be handed an empty command.
    prepare_host "$run" > "$run/wiring.txt" 2>&1
    sed 's/^/  /' "$run/wiring.txt"
    # An empty RUN_CMD means prepare_host ran somewhere its assignment could not escape —
    # it did exactly that once, from a pipeline. `timeout` then printed its usage and
    # every run came back rc=125 with no decisions, which reads like the plugin failing
    # rather than the harness never having started the host.
    [ "${#RUN_CMD[@]}" -gt 0 ] || { echo "  ! prepare_host produced no command for $HOST" >&2; exit 3; }
    cd "$proj" || exit 1
    timeout "$TIMEOUT" "${RUN_CMD[@]}" > "$run/out.txt" 2> "$run/err.txt"
    echo "$?" > "$run/rc"
  )

  # The verdict comes from the store, not from what the model said it did.
  EH="$run/engine"
  decisions="$(cd "$proj" && KGAI_HOME="$EH" LD_LIBRARY_PATH="$EH/lib" \
    "$EH/bin/kg" doctor 2>/dev/null |
    python3 -c 'import sys,json;print(json.load(sys.stdin).get("decisions",0))' 2>/dev/null || echo "?")"
  titles="$(cd "$proj" && KGAI_HOME="$EH" LD_LIBRARY_PATH="$EH/lib" \
    "$EH/bin/kg" search "" --limit 20 2>/dev/null |
    python3 -c 'import sys,json;print(" | ".join(h["name"] for h in (json.load(sys.stdin).get("hits") or []) if h["kind"]=="decision"))' 2>/dev/null || echo "")"
  # grep -c prints its count even when it is 0 (and then exits 1), so `|| echo 0` would
  # append a SECOND zero — take the count as printed and default only a missing file.
  hooks_fired="$(grep -c . "$run/hooks.log" 2>/dev/null | head -n1)"; hooks_fired="${hooks_fired:-0}"
  blocked="$(grep -c "BLOCK issued" "$run/hooks.log" 2>/dev/null | head -n1)"; blocked="${blocked:-0}"

  want=1; [ "$TASK_TYPE" = trivial ] && want=0
  verdict=FAIL
  [ "$decisions" = "$want" ] && verdict=ok
  printf '  run%-2s %-4s decisions=%s (want %s)  hook-lines=%s blocks=%s  rc=%s\n' \
    "$i" "$verdict" "$decisions" "$want" "$hooks_fired" "$blocked" "$(cat "$run/rc" 2>/dev/null)"
  [ -n "$titles" ] && printf '        %s\n' "$titles"
  [ "$verdict" = ok ] && recorded_total=$((recorded_total + 1))
done

printf '\n%s/%s runs matched the expectation. Artifacts under %s\n' "$recorded_total" "$RUNS" "$WORK"
[ "$recorded_total" = "$RUNS" ]
