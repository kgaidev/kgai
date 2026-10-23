# kgai on SDE-bench

[SDE-bench](https://github.com/vectorize-io/sde-bench) is the coding-agent benchmark of the
[Agent Memory Benchmark](https://github.com/vectorize-io/agent-memory-benchmark) (AMB): 61 bug
fixes where the obvious fix fails a hidden test that encodes an earlier team decision. The score
is how many tasks the agent fixes on the first try, without being told its fix still fails.

We build kgai, so these are a vendor's numbers. The benchmark, the harness and the comparison
runs are not ours; everything we ran is here.

## Results

| 61 tasks, Claude Code + claude-sonnet-5 | First-try fixes | Interventions per task | Cost per run |
|---|---|---|---|
| No memory (AMB, 5 runs) | 21–25 | 0.80–0.92 | $26–28 |
| Hindsight (AMB, 5 runs) | 40–46 | 0.28–0.41 | $19–22 |
| **kgai (3 runs: 47, 48, 51)** | **47–51** | **0.23–0.25** | **$12.5–17.4** |

Cost is the API-equivalent Claude Code reports. kgai's excludes the one-time capture: $33 for
all 306 corpus documents, shared by the three runs (`results/capture-log.jsonl.gz`). kgai solved 182
of 183 tasks in the end. Per-task results, each with the exact memory the agent got
(`memory_injected`), are in `results/run-*.jsonl.gz`; totals in `results/summary.json`.

Comparison runs are the harness authors' own, published in AMB under `outputs/sdebench/`
(`vanilla-claude-*`, `full61-cc-none-*`, `hindsight-claude-*`, `full61-cc-hs-*`), same agent
and model: Claude Code, claude-sonnet-5.

## Method

1. **Capture** (`capture.py`). kgai stores decisions an agent records, not documents. The same
   model read every corpus document — the task's own chat or commit, 140 decoy chats, 100 host
   commits — as a past session and recorded what it decided, through kgai's stock
   `knowledge-graph` skill. No benchmark-specific instructions; it never sees the task policies
   or the hidden tests. The shared part is captured once and copied into each task's store.
2. **Run** (`run.py`). Each task goes through the harness's `provided` arm, which appends a
   memory file to the bug report. Ours is `kg context --about "<bug report>"` over the task's
   store, verbatim. The runner stops if the memory is not in the agent's prompt.

Engine: kgai at commit 2216ac6. Harness: AMB `16fcebb`, dataset `afcce15`.

## Run it

Needs Docker, Python 3.11+ with `rich` and `tiktoken` (the dataset loader imports them), a
Claude Code login, and a `kg` binary built from this repo. Run the commands from this directory.

```
git clone https://github.com/vectorize-io/agent-memory-benchmark amb
git clone https://github.com/vectorize-io/sde-bench amb/sdebench/datasets
git clone https://github.com/mahmoud/boltons boltons      # host-history noise, pinned in the dataset
(cd amb/sdebench && docker build -t sdebench-base -f Dockerfile . \
   && docker build -t sdebench-agent-claude -f Dockerfile.agent-claude .)
(cd amb && git apply ../harness-linux.patch)               # Linux only, see below

export AMB_DIR=$PWD/amb BENCH_DIR=$PWD/work KG=/path/to/kg KGAI_SKILL=/path/to/kgai/skills/knowledge-graph/SKILL.md
export SDEBENCH_BOLTONS_HOST=$PWD/boltons CLAUDE_CODE_OAUTH_TOKEN=...   # claude setup-token
ls amb/sdebench/datasets/boltons-*/tasks/main/task.json | sort > tasks.txt

python capture.py shared && python capture.py tasks tasks.txt
python run.py tasks.txt work/run-1
```

The harness calls `python`; make sure it is on PATH.

**Linux patch.** The harness's containers run as root and leave root-owned files (`.pyc`,
`.pytest_cache`) in its bind mounts, which it then cannot delete. `harness-linux.patch` hands
them back to the host user before each delete. It does not touch the agent or the grading.

## Known limits

- Three runs; results vary run to run (the published Hindsight runs span 40–46 of 61).
- kgai's skill does not record plain bug-fix commits, by design. Where a task's decision lives
  only in a `fix:` commit, the agent recorded nothing for it.
- Self-reported: AMB lists only results its maintainers reproduce, and SDE-bench is not on its
  public leaderboard.
