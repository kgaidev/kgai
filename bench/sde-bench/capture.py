#!/usr/bin/env python3
"""SDE-bench, step 1: turn each task's corpus into kgai decisions the way kgai is fed in
real use — an agent records decisions while it works. The benchmark's own model (Claude
Code, claude-sonnet-5) reads each corpus document as a past session or commit and records
what it decided through kgai's stock knowledge-graph skill. No benchmark-specific
instructions; the agent never sees task.json policies or the hidden tests.

The shared part of every corpus (140 decoy chats, 100 host commits) is captured once and
copied into each task's store; then each task's own documents are captured into it.

  capture.py shared            -> $BENCH_DIR/stores/_shared
  capture.py tasks TASKLIST    -> $BENCH_DIR/stores/<task_id>   (one task.json path per line)
  capture.py memory TASK_ID    -> prints what the agent gets: kg context --about "<bug report>"

Env: AMB_DIR (agent-memory-benchmark clone with the sde-bench dataset), BENCH_DIR (work dir),
KG (kg binary), KGAI_SKILL (kgai skills/knowledge-graph/SKILL.md), SDEBENCH_BOLTONS_HOST,
CLAUDE_CODE_OAUTH_TOKEN (a Claude Code login; `claude setup-token`).
Resumable: every capture is logged; a rerun skips what succeeded.
"""
import concurrent.futures as cf, json, os, shutil, subprocess, sys, time, types
from pathlib import Path

AMB = Path(os.environ["AMB_DIR"]) / "src"
for pkg in ("memory_bench", "memory_bench.dataset"):  # the dataset loader only, no provider deps
    m = types.ModuleType(pkg); m.__path__ = [str(AMB / pkg.replace(".", "/"))]; sys.modules[pkg] = m
from memory_bench.dataset.sdebench import SdebenchDataset  # noqa: E402

BENCH = Path(os.environ["BENCH_DIR"])
STORES, LOG = BENCH / "stores", BENCH / "capture-log.jsonl"
MODEL = os.environ.get("SDE_CLAUDE_MODEL", "claude-sonnet-5")
ds = SdebenchDataset()

PROMPT_CHAT = ("Below is the transcript of an earlier working session in this project. Record into the "
               "project's knowledge graph whatever that session decided, exactly as you would have at the "
               "end of that session, following the knowledge-graph skill. If it decided nothing that "
               "belongs in the graph, record nothing. Do not ask questions.\n\n<transcript>\n{doc}\n</transcript>")
PROMPT_COMMIT = ("Below is a commit from this project's git history. Record into the project's knowledge graph "
                 "the decision it documents, if any, following the knowledge-graph skill. If it documents "
                 "nothing that belongs in the graph, record nothing. Do not ask questions.\n\n<commit>\n{doc}\n</commit>")


def setup():
    """A private Claude config holding only kgai's skill, and `kg` on PATH."""
    cfg, bin_ = BENCH / "claude-config", BENCH / "bin"
    (cfg / "skills" / "knowledge-graph").mkdir(parents=True, exist_ok=True)
    shutil.copy(os.environ["KGAI_SKILL"], cfg / "skills" / "knowledge-graph" / "SKILL.md")
    (cfg / "settings.json").write_text(json.dumps({"permissions": {"allow": ["Bash(kg:*)", "Bash(kg *)", "Skill"]}}))
    bin_.mkdir(exist_ok=True)
    (bin_ / "kg").write_text(f'#!/bin/sh\nexec {os.environ["KG"]} "$@"\n'); (bin_ / "kg").chmod(0o755)
    return cfg, bin_


CFG, BIN = setup()


def kgenv(store, cwd):
    home = BENCH / "home"; home.mkdir(exist_ok=True)
    return {"PATH": f"{BIN}:{os.environ['PATH']}", "HOME": str(home), "KGAI_HOME": str(BENCH / "kgai-home"),
            "KGAI_STATE_DIR": str(BENCH / "kgai-state"), "KGAI_PROJECT": str(cwd), "KGAI_STORE": str(store)}


def ensure_store(store):
    if not (store / "kg.config.json").exists():
        store.mkdir(parents=True, exist_ok=True)
        subprocess.run([str(BIN / "kg"), "init", "--actor", "bench"], env={**os.environ, **kgenv(store, BENCH)},
                       capture_output=True, check=True)


def capture(store, doc):
    work = BENCH / "work" / doc.id.replace(":", "_")  # own cwd: parallel sessions write files
    work.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, **kgenv(store, work), "CLAUDE_CONFIG_DIR": str(CFG)}
    prompt = (PROMPT_COMMIT if doc.content.startswith("Git commit:") else PROMPT_CHAT).format(doc=doc.content)
    t0 = time.time()
    p = subprocess.run(["claude", "-p", prompt, "--model", MODEL, "--output-format", "json",
                        "--permission-mode", "acceptEdits", "--allowedTools", "Bash(kg:*)", "Skill", "Write", "Read",
                        "--max-turns", "15"], capture_output=True, text=True, env=env, cwd=str(work), timeout=600)
    try:
        out = json.loads(p.stdout)
    except Exception:
        out = {"error": (p.stderr or p.stdout)[-500:]}
    rec = {"store": store.name, "doc": doc.id, "rc": p.returncode, "s": round(time.time() - t0, 1),
           "cost": out.get("total_cost_usd"), "turns": out.get("num_turns"), "err": out.get("error") or out.get("is_error")}
    with LOG.open("a") as f:
        f.write(json.dumps(rec) + "\n")
    return rec


def run_many(store, docs, workers):
    ensure_store(store)
    done = {(r["store"], r["doc"]) for r in map(json.loads, LOG.open()) if r["rc"] == 0} if LOG.exists() else set()
    with cf.ThreadPoolExecutor(workers) as ex:
        for r in ex.map(lambda d: capture(store, d), [d for d in docs if (store.name, d.id) not in done]):
            if r["rc"]:
                print("capture failed:", r, flush=True)


def is_own(doc):
    return doc.id.split(":", 1)[1].startswith(("chat", "decision-commit"))


def main():
    cmd = sys.argv[1]
    if cmd == "shared":
        tid = json.loads(ds._task_files()[0].read_text())["task_id"]
        docs = [d for d in ds.load_documents("boltons", user_ids={tid}) if not is_own(d)]
        for d in docs:  # the shared pool, captured once
            d.id = "_shared:" + d.id.split(":", 1)[1]
        run_many(STORES / "_shared", docs, workers=4)
    elif cmd == "tasks":
        for tj in [l.strip() for l in open(sys.argv[2]) if l.strip()]:
            tid = json.loads(Path(tj).read_text())["task_id"]
            store = STORES / tid
            if not store.exists():  # the log only; kg builds the graph from it on first use
                shutil.copytree(STORES / "_shared", store, ignore=shutil.ignore_patterns("graph.kuzu*", "*.lock"))
            run_many(store, [d for d in ds.load_documents("boltons", user_ids={tid}) if is_own(d)], workers=1)
    elif cmd == "memory":
        tid = sys.argv[2]
        q = next(q for q in ds.load_queries("boltons") if q.user_id == tid)
        print(subprocess.run([str(BIN / "kg"), "context", "--about", q.query, "--max", "10"], capture_output=True,
                             text=True, env={**os.environ, **kgenv(STORES / tid, BENCH)}, check=True).stdout.strip())


if __name__ == "__main__":
    main()
