# kgai — give your codebase a memory

> An AI-first knowledge graph that automatically captures the **decisions** behind your
> code — *what* changed, *why*, and *how it evolved* — so the reasoning never lives only
> in someone's head.

Codebases record *what* the code is. They rarely record *why* it's that way: why invoicing
was split out of pricing, why this service owns sessions, which approach you rejected and
what you'd break by going back. That reasoning lives in people's heads and in old chat
threads — and it walks out the door when they do.

**kgai** captures it as you work. While you and Claude change code, it records the
structural decisions into a small, shared knowledge graph. Before touching an area, your
AI checks what was already decided; after making a change, it captures the new decision —
**automatically, without you asking**. Nothing is ever overwritten, so you can always ask
*how did this get this way?* and get the full story.

## See it in action

You tell Claude: *"invoices should be their own module, not part of pricing — refactor it."*
Claude does the refactor **and**, on its own, records the decision:

```
✓ Recorded: "Invoice is a standalone module, independent of Pricing"
  Invoice ──DEPENDS_ON──> (removed PART_OF)  ·  Pricing now depends on Invoice
```

Three months later, someone (or their AI) is about to fold invoicing back into pricing.
They ask first:

```bash
$ kg history "feature:Invoice"
  2026-02  Invoice rendered inside the Pricing module
  2026-05  Invoice split into a standalone module   ← why: "billing is its own domain…"  (current)
```

The boundary was intentional, and the reasoning is right there. No archaeology, no guessing.

## Quick start

```bash
# install from GitHub (public marketplace lives in this repo)
claude plugin marketplace add vasekd/kgai
claude plugin install kgai@kgai-marketplace
```

On first run the plugin sets itself up automatically (downloads a small prebuilt engine to
`~/.kgai`; falls back to building from source if needed). Then just work normally — Claude
reads and records decisions on its own. To record or query by hand:

```bash
/kgai:kg-ask "Invoice"        # what's decided about this area, and why
/kgai:kg-decision             # record a decision yourself
/kgai:kg-history              # how something evolved
```

## Share it with your team

The knowledge graph is a store with its own sync cycle, independent of any project repo.
Point it at a git remote once, and decisions flow to the whole team:

```bash
kg init --remote git@github.com:your-org/kg-store.git
/kgai:kg-sync                 # commit, pull teammates' decisions, push yours
```

Concurrent edits never clobber each other — if two people change the same thing, it becomes
a visible **branch** to resolve, not a silent overwrite.

### Importing past decisions

Seeding the graph with decisions that were really made earlier? Give each one a `date`
(`YYYY-MM-DD` or RFC3339) so the history and `kg as-of <date>` reflect the real timeline,
not the import time:

```json
{ "decision": { "title": "…", "date": "2025-03-15", "mutations": [ … ] } }
```

## What you can do

| You want to… | Command / slash |
|---|---|
| See what's decided about an area, and why | `/kgai:kg-ask` · `kg context --about X` / `--paths a,b` |
| Record a decision | `/kgai:kg-decision` · `kg ingest` |
| Review a task, graph-aware (read → review → capture) | `/kgai:kg-review` |
| See how something evolved | `/kgai:kg-history` · `kg history "feature:Invoice"` |
| See the whole picture at a past date | `kg as-of 2026-01-01` |
| Resolve concurrent-edit branches | `/kgai:kg-conflicts` |
| Share / pull decisions | `/kgai:kg-sync` |
| Raw query (power users) | `/kgai:kg-query` · `kg query "…"` |

### Automatic capture — and no noise

Capture is hands-off, backed by two layers: the bundled **knowledge-graph skill** makes the
model record structural decisions on its own, and a **`Stop` hook** catches the case where
it edits code but forgets — nudging it to record before finishing. Trivial work (renames,
formatting, bug fixes) records **nothing**, so the graph stays signal, not noise.

In headless testing this held up across models: structural refactors auto-recorded reliably;
when the model was blocked from recording on its own, the hook still captured every time;
trivial edits recorded nothing even when nudged.

## Under the hood

The nodes are **domain elements** (features, services, business objects) joined by links; a
**decision** is an immutable event that reshapes that graph and carries who/why/when. The
chain of decisions is the history; the live graph is always the current shape.

It's event-sourced: an append-only, content-addressed **decision log** is the source of
truth, projected into an embedded **[LadybugDB](https://ladybugdb.com)/Kuzu** property graph
(queryable with Cypher) that can be rebuilt from the log at any time. Sync is a conflict-free
merge of per-person log shards. Identity is a deterministic hash of an element's kind+name,
so two people recording the same thing converge on one node with no coordination.

Full design: **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)**.

## Configuration

| Env | Meaning | Default |
|---|---|---|
| `KGAI_STORE` | knowledge-graph store location | `<project>/.kgai/store` (per-project) |
| `KGAI_PROJECT` | project root used to locate the store | git top-level of the working dir |
| `KGAI_HOME` | engine binary + native lib home | `~/.kgai` |
| `KGAI_ACTOR` | your name on recorded decisions | git user / `$USER` |
| `KG_RELEASE_BASE` | prebuilt download base | this repo's latest release |

By default the KG is **per-project**: each project gets its own graph in
`<project>/.kgai/store` (auto-created on first use and added to the project's
`.gitignore`). The engine binary itself is shared in `~/.kgai`. Point `KGAI_STORE` at a
shared path if you want several projects to write into one graph.

## Roadmap

- Prebuilt binaries for Linux (x86_64, aarch64) via GitHub Releases — see
  [`.github/workflows/build.yml`](.github/workflows/build.yml).
- macOS prebuilds (needs `@loader_path` linking + a DYLD-aware launcher).
- Optional decision signing for zero-trust team remotes.

## License

MIT — see [LICENSE](LICENSE). Bundles the MIT-licensed Kuzu binding and `libkuzu`.
