#!/usr/bin/env python3
"""SDE-bench, step 2: run every task through the harness with kgai's memory in the prompt.

The harness's "provided" arm appends an external memory file to the bug report — how any
AMB memory system runs the benchmark generically. Ours is the output of
`kg context --about "<bug report>"` over the task's store (capture.py memory).

  run.py TASKLIST OUTDIR

Env: as capture.py, plus CLAUDE_CODE_OAUTH_TOKEN for the agent. The harness skips a
memory file it cannot find without a word, so every task is checked: if the memory is not
in the agent's prompt, the run stops.
"""
import json, os, subprocess, sys, time
from pathlib import Path

tasklist, outdir = sys.argv[1], Path(sys.argv[2]).resolve()
outdir.mkdir(parents=True, exist_ok=True)
AMB = Path(os.environ["AMB_DIR"])
HERE = Path(__file__).resolve().parent
MODEL = os.environ.get("SDE_CLAUDE_MODEL", "claude-sonnet-5")

for tj in [l.strip() for l in open(tasklist) if l.strip()]:
    tid = json.load(open(tj))["task_id"]
    if (outdir / f"{tid}.json").exists():
        continue
    mem = outdir / f"{tid}.memory.json"
    mem.write_text(subprocess.run([sys.executable, str(HERE / "capture.py"), "memory", tid],
                                  capture_output=True, text=True, check=True).stdout)
    run_id = f"kgai-{int(time.time())}"
    p = subprocess.run([sys.executable, str(AMB / "sdebench/harness/run.py"), "--task", tj, "--agent", "claude-code",
                        "--model", MODEL, "--run-id", run_id, "--history", "provided", "--external-memory", str(mem)],
                       capture_output=True, text=True, cwd=str(AMB))
    work = Path("/tmp/sdebench/run") / f"{tid}_provided_{run_id}"
    trace = (work / "trace.json").read_text() if (work / "trace.json").exists() else ""
    if "Relevant memory (surfaced" not in trace:
        sys.exit(f"{tid}: memory did not reach the agent's prompt — stopping\n{(p.stderr or p.stdout)[-800:]}")
    result = json.loads((work / "result.json").read_text())
    result["task_id"] = tid
    (outdir / f"{tid}.json").write_text(json.dumps(result, indent=1))
    print(f"{tid:34s} solved={result.get('solved')} interventions={result.get('interventions')} "
          f"cost=${result.get('cost_usd')}", flush=True)
