# kgai — Knowledge Graph plugin for Claude Code

A **local-first, historically-immutable knowledge graph** of engineering **decisions**
and **knowledge** — the things that normally live only in developers' heads. It is built
**for the AI**: Claude queries it to ground work in past decisions, and writes new
decisions (with all their relationships) back into it so future sessions inherit them.

- **Immutable & historical.** Nothing is overwritten. A change is a new *decision* that
  *supersedes* the prior one, so the full **evolution of every element** is queryable
  forever (`kg history`, `kg as-of <date>`).
- **Local + synced.** Reads/writes are local and fast. `kg sync` pushes/pulls to a
  dedicated git remote (its own cycle, independent of any project repo).
- **Conflict-aware.** Concurrent edits become **branches** (two heads), surfaced by
  `kg conflicts` and resolved by a merge decision — never silently lost.
- **Graph engine.** An embedded **LadybugDB/Kuzu** property graph (Cypher), used as a
  rebuildable read-model over an append-only event log.

## Mental model

The live graph is a small, stable set of **domain elements** (application & business
things — Invoice, Pricing, Checkout…) joined by **links** (PART_OF, DEPENDS_ON, …). It
is not a pile of decision records. A **decision** is an immutable event that *mutates*
that graph (adds an element, adds/retires a link, sets a property) and records
who/why/when. The chain of decisions is the history; the live graph is always the
current shape.

## Architecture in one paragraph

The **append-only decision log** (per-install NDJSON shards) is the single source of
truth; every event is content-addressed and Lamport-stamped. The **Kuzu graph** is a
*derived projection* — a live element graph plus a decision/provenance plane —
rebuildable at any time (`kg rebuild`). Sync is a **union merge of log shards**
(distinct files per install → conflict-free; never a rebase or force-push). Element
identity is a *deterministic hash of kind+name*, so two people recording the same
element converge on one node (automatic dedup). A decision **auto-supersedes** the head
decision(s) of every element it changes; two competing heads on one element is a
*conflict*, resolved by a new decision that supersedes both. Retiring a link removes it
from the live graph but never from history — `kg as-of <date>` replays the log to
reconstruct any past shape. See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Install

```bash
# from this repo (local marketplace)
claude plugin marketplace add /opt/git/kgai
claude plugin install kgai@kgai-marketplace
# or for development:
claude --plugin-dir /opt/git/kgai
```

On first session the plugin's `SessionStart` hook runs `scripts/install.sh`, which puts
the `kg` engine in a stable home (`~/.kgai`). Two ways the engine is obtained:

1. **Prebuilt (recommended for users):** set `KG_RELEASE_BASE` to a GitHub Release
   download base; the hook downloads `kg-<os>-<arch>` + `libkuzu-<os>-<arch>.so`. No Go
   or C compiler needed. Releases are produced by `.github/workflows/build.yml`.
2. **Build from source (fallback):** requires `go` ≥ 1.22 and a C compiler. The hook
   fetches the native lib and `go build`s the binary.

> Note on the engine: LadybugDB's own Go binding currently requires Go ≥ 1.26. Its early
> tags are byte-identical to the **Kuzu** binding, and LadybugDB is an API/Cypher-compatible
> fork of Kuzu, so we build against the proven `go-kuzu` binding, isolated behind
> `internal/graph` for a one-file swap later.

## Usage

| You want to… | Command / slash |
|---|---|
| See the current shape of an area + decisions that shaped it | `/kgai:kg-ask` · `kg context --about X` / `--paths a,b` |
| Record a structural decision as graph mutations | `/kgai:kg-decision` · `kg ingest` |
| Knowledge-graph-aware task review (read → review → capture) | `/kgai:kg-review` |
| See how an element evolved | `/kgai:kg-history` · `kg history "feature:Invoice"` |
| See the graph at a past date | `kg as-of 2026-01-01` |
| Resolve concurrent-edit branches | `/kgai:kg-conflicts` |
| Share / pull decisions | `/kgai:kg-sync` · `kg sync` |
| Raw Cypher | `/kgai:kg-query` · `kg query "…"` |

The AI follows the bundled **`knowledge-graph` skill**, which tells it to read before
non-trivial changes and to capture only structural choices (no noise), at the end of a
task, as one atomic `kg ingest`.

### Automatic capture (no need to ask)

Decisions are captured **automatically**, without you having to run a command, via two
layers that back each other up:

1. The skill's description compels the model to record a structural decision on its own.
2. A **`Stop` hook** ([hooks/auto-capture-stop.sh](hooks/auto-capture-stop.sh)) fires at
   the end of any turn that edited code and — if the model forgot to record a structural
   decision — makes it do so before finishing. It is loop-safe (honors `stop_hook_active`)
   and stays quiet on trivial work (renames, formatting, bug fixes record nothing).

Measured on headless runs: structural refactors auto-record reliably across opus and
sonnet; when the model was deliberately stopped from self-recording, the hook still
captured the decision every time; trivial edits recorded nothing even when nudged.

### Record a decision (the primary write path)

A decision reshapes the element graph via `mutations`:

```bash
kg ingest <<'JSON'
{ "decision": {
  "title": "Invoice se zobrazuje samostatně, mimo Ceník",
  "rationale": "Invoice je samostatná doména; nemá viset na ceníku",
  "author": "Vaclav", "refs": [{"system":"clickup","url":"CU-1234"}],
  "mutations": [
    {"op":"upsert_element","kind":"feature","name":"Invoice","props":{"paths":"src/billing/*"}},
    {"op":"retire_link","from":"feature:Invoice","link":"PART_OF","to":"feature:Ceník"},
    {"op":"set_prop","element":"feature:Invoice","key":"display","value":"standalone"}
  ] } }
JSON
```

## Configuration

| Env | Meaning | Default |
|---|---|---|
| `KGAI_HOME` | runtime + store home | `~/.kgai` |
| `KGAI_STORE` | override store location | `$KGAI_HOME/store` |
| `KGAI_ACTOR` | author name for this install | git user / `$USER` |
| `KG_RELEASE_BASE` | prebuilt download base URL | (unset → build from source) |

Set the sync remote once: `kg init --remote <git-url>`.

## License

MIT — see [LICENSE](LICENSE). Bundles the MIT-licensed Kuzu binding and `libkuzu`.
