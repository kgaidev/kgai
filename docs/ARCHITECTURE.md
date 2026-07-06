# kgai architecture

## Goal

Capture engineering **decisions and knowledge** that otherwise live only in developers'
heads, **immutably and historically** (the evolution of each decision is preserved),
**local-first**, and primarily **for an AI** to read from and write to. (Team sync to a
shared remote is designed in — see below — but not yet exposed as a supported feature.)

## Two planes, one log

```
            write (kg ingest: a decision + mutations)
                          │
                          ▼
   ┌──────────────────────────────────────────┐
   │  Decision log  =  SOURCE OF TRUTH         │   append-only, immutable,
   │  log/<installId>.ndjson  (per install)    │   content-addressed, Lamport-stamped
   └──────────────────────────────────────────┘
                          │ replay (deterministic projection)
                          ▼
   ┌──────────────────────────────────────────┐
   │  graph.kuzu  =  DERIVED READ-MODEL        │
   │  • LIVE element graph: Element + LINK      │   current shape (small, stable)
   │  • DECISION plane: Decision, SHAPES,       │   immutable history / provenance
   │    SUPERSEDES                              │
   └──────────────────────────────────────────┘
                          │ read
                          ▼
        kg context / history / as-of / search / conflicts / query
```

The log is authoritative; the graph is a projection you can throw away and rebuild
(`kg rebuild`). Sync is therefore about *merging logs*, not *merging databases*.

## The model: elements shaped by decisions

The nodes are **domain elements** (application & business things: a feature, a service,
a business object). They are few and stable. A **decision** is an immutable event that
*mutates* the element graph and carries who/why/when. The decisions are the history;
the live element graph is always just the current shape.

- **Live graph stays clean.** `retire_link` deletes the live edge; `set_prop` updates a
  property. Nothing is lost: the decision that made the change is permanent, and replay
  reconstructs any past state.
- **History on demand = the decision plane.** Each decision `SHAPES` the elements it
  touched. "How did Invoice get this way?" = the decisions shaping Invoice, ordered —
  fast, with the *why*, no replay needed.
- **Exact past structure = replay.** `kg as-of <t>` replays every decision recorded at
  or before `t` into an ephemeral graph and returns its shape.

## Mutations

A decision carries a list of structural ops, applied atomically and idempotently:

| op | effect (projection Cypher) |
|---|---|
| `upsert_element` | `MERGE (e:Element {id}) ON CREATE SET kind,name` |
| `add_link` | `MERGE (a)-[:LINK {kind}]->(b) ON CREATE SET created_by` |
| `retire_link` | `MATCH (a)-[r:LINK {kind}]->(b) DELETE r` |
| `set_prop` | read-modify-write of a small `props` blob (keys kept sorted) |

After mutations, the decision `SHAPES` every element it touched, and `SUPERSEDES` the
prior head decision(s) of every element it structurally changed.

## Deterministic identity (automatic dedup)

`ElementID = "el_" + hash(normalize(kind), normalize(name))`. Independent of author and
time, so two people who record `feature:Invoice` mint the **same** id and the graph
`MERGE`s them — no coordination, no duplicate islands. A `Decision`'s id is a content
hash, so identical content is idempotent and any change is a new immutable decision.

## Sync and conflicts

> **Status:** the sync mechanisms below are implemented in the engine (`kg sync`) but team
> sharing is **not yet exposed as a supported feature** — no slash command ships for it
> and the docs don't advertise it. It's on the roadmap; the design is recorded here so
> the storage decisions (shards, union merge) make sense.

Transports are pluggable (`internal/remote`, chosen by the remote URL scheme):

- **git** (any git URL): the store dir is a git repo; sync = commit → fetch → union
  merge → push.
- **s3** (`s3://bucket/prefix`): a **stateless segment protocol** for object stores.
  Each push uploads a write-once object `segments/<install>/<seq>-<count>.ndjson`
  holding that install's next batch of events. What to push/pull is derived by
  comparing local shard lengths with the cumulative counts encoded in the keys — no
  client-side sync state to lose. Downloads are verified (content hash + shard
  hash-chain continuity) before landing in the local log. The kgai cloud will speak
  this same protocol via presigned URLs.
- After every sync the projection is **fully rebuilt** in canonical (lamport, hash)
  order rather than incrementally patched: pulled events may sort before already-
  projected ones, and last-writer-wins mutations (e.g. `set_prop` on the same key)
  would otherwise diverge between installs. Rebuild is cheap because the live graph
  is small by design.

- The log is split into **per-install NDJSON shards** (`log/<installId>.ndjson`,
  installId minted at `kg init`). One writer per file ⇒ git merges are a **conflict-free
  union**. (`*.ndjson merge=union` is set as a belt-and-suspenders.)
- `kg sync` = commit → `git fetch` → **fast-forward / union merge** (with
  `--allow-unrelated-histories`, since each install inits its own repo) → replay new
  events → push. **Never `rebase`, never `--force`** — that would rewrite append-only
  history.
- Install-local state (`kg.config.json`, the `graph.kuzu*` cache, native `*.so`) is
  gitignored so clones never conflict on it.
- A **real conflict** is two competing head decisions on one element (two people changed
  it concurrently without one superseding the other). It is a *branch*, not an error:
  `kg conflicts` lists it; a new decision on that element supersedes both heads and
  resolves it. All auditable.

## Replay invariants

1. **Total order:** events sorted by `(lamport, hash)` — deterministic, hash embeds the
   install. Two stores that replayed the same events produce identical graphs; verified
   by `kg export --canonical` (matching digests).
2. **Idempotent:** every projection write is a `MERGE` keyed by primary key + an
   `_Applied(hash)` watermark, so re-applying an event is a no-op.
3. **Dependency-tolerant:** an edge/supersedes target that hasn't arrived yet (partial
   pull) is attached to a **stub node** via `MERGE`, filled in when the real fact
   replays. `kg rebuild` is robust to incomplete logs.

## Engine choice

`go-kuzu` (CGO over native `libkuzu`). LadybugDB is an API/Cypher-compatible fork of
Kuzu; its own Go binding currently needs Go ≥ 1.26, and its early tags are byte-identical
to `go-kuzu`. All engine calls are isolated in `internal/graph`, so swapping to
`go-ladybug` later is a single-file change. Reads open the DB read-only; writes take a
store-wide `flock` so concurrent sessions never corrupt the single-writer cache.

## Code map

| Path | Responsibility |
|---|---|
| `src/internal/event` | event/fact types, canonical hashing, deterministic ids, normalize |
| `src/internal/store` | log shards, config, flock, Lamport, git sync |
| `src/internal/graph` | Kuzu schema, idempotent MERGE projection, stub nodes, queries |
| `src/internal/engine` | resolution, ingest, rebuild, context scoring, history/as-of/conflicts/search/doctor/export |
| `src/main.go` | `kg` CLI (JSON I/O) |
| `scripts/` | `fetch-libs.sh` (native lib), `install.sh` (idempotent engine install) |
| `bin/kg` | PATH shim → stable `~/.kgai` engine |
