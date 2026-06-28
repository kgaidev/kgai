---
name: knowledge-graph
description: >-
  Read from and write to the kgai knowledge graph — a small, stable graph of DOMAIN
  ELEMENTS (application & business things, e.g. Invoice, Pricing, Checkout) shaped by
  an immutable log of DECISIONS. Elements are the nodes; decisions define how the graph
  looks and carry the history of every change. Use it (1) BEFORE a non-trivial change,
  to see the current shape of the area and the decisions that made it that way, and
  (2) AFTER deciding something structural, to record the decision as graph mutations.
  Triggers: reviewing/preparing a task, "why is X like this", "how does X relate to Y",
  choosing to split/merge/move a feature, changing how components depend on each other.
---

# kgai knowledge graph

`kg` is on your PATH (a shim; the engine lives in `~/.kgai`). Every command prints JSON.

**Mental model.** The live graph is a small set of **Elements** (domain things) joined
by **links** (PART_OF, DEPENDS_ON, RENDERS, …). It is NOT a pile of decision records.
A **Decision** is an immutable event that *mutates* that graph — adds an element, adds
or retires a link, sets a property — and records who/why/when. The chain of decisions
is the **history**; the live graph is always just the current shape.

## 1. READ before you change code

```bash
kg search "invoice"                   # substring lookup to find the exact element/decision name first
kg context --about "Invoice"          # the element + its current links + decisions that shaped it
kg context --paths "src/billing/*"    # elements whose `paths` property matches what you're editing
kg history "feature:Invoice"          # full decision chain that shaped one element (the why, over time)
kg as-of 2026-01-01                   # what the graph looked like at a past date (exact, via replay)
kg conflicts --about "Invoice"        # elements with two competing head decisions (unresolved)
```

`kg context` returns, per matched element: its current **links** and the **decisions**
that shaped it (newest = head, with rationale). If a decision constrains what you're
about to do, respect it — or supersede it with a new decision (§2).

If `kg` prints `{"ok":false,...}`, is not installed, or returns no items, **say so
plainly** ("the knowledge graph has no record of this yet") — never invent elements,
links or decisions that the commands didn't return.

## 2. WRITE a decision as graph mutations

When a **structural** choice is made about the domain — split/merge/move a feature,
change a dependency, mark how something is rendered/owned — record ONE decision whose
`mutations` reshape the graph. Confirm with the human first; capture at the end of the
task, consolidated.

```bash
kg ingest <<'JSON'
{
  "decision": {
    "title": "Invoice se zobrazuje samostatně, mimo Ceník",
    "rationale": "Invoice je samostatná doména; nemá viset na ceníku",
    "author": "Vašek",
    "refs": [{"system": "clickup", "url": "https://app.clickup.com/t/CU-1234"}],
    "mutations": [
      {"op": "upsert_element", "kind": "feature", "name": "Invoice", "props": {"paths": "src/billing/invoice/*"}},
      {"op": "upsert_element", "kind": "feature", "name": "Ceník"},
      {"op": "retire_link", "from": "feature:Invoice", "link": "PART_OF", "to": "feature:Ceník"},
      {"op": "set_prop", "element": "feature:Invoice", "key": "display", "value": "standalone"}
    ]
  }
}
JSON
```

Mutation ops (required fields in **bold**):
- `upsert_element` — ensure a node exists: **`kind`** (e.g. `feature`, `business`,
  `service`, `component`, `concept`) + **`name`**. Optional `props` (e.g. `paths` →
  makes `kg context --paths` find it).
- `add_link` / `retire_link` — **`from`**, **`to`** (element refs `kind:name`),
  **`link`** (the relationship kind, e.g. `PART_OF`, `DEPENDS_ON`, `RENDERS`).
- `set_prop` — **`element`**, **`key`**, **`value`**.

How it behaves:
- **Element identity is deterministic** (hash of kind+name, normalized to lowercase +
  collapsed spaces) — reuse the exact same `name` to refer to the same element; two
  people converge on one node, no duplicates. But **diacritics and distinct words still
  fork the node**: `Ceník` ≠ `Cenik`, `Invoice` ≠ `Faktura`. Pick one canonical name per
  element and reuse it. Unsure if it exists? `kg resolve "feature:Invoice"` first.
- The decision **automatically supersedes** the previous head decision(s) of every
  element it changes → the element's history chains, and concurrent edits surface as a
  conflict (§3). You usually don't set supersession by hand.
- **Retiring a link removes it from the live graph but never from history** — the
  decision that retired it is permanent, and `kg as-of <date>` reconstructs the old
  shape.
- Several decisions at once: send `{"decisions": [ {...}, {...} ]}`.

### When to record (keep it structural, not noise)
- **DO:** split/merge/move a feature or component; change a dependency or ownership;
  decide how something is rendered/exposed; deprecate/replace a prior structural choice.
- **DON'T:** behavior-preserving refactors, renames, formatting, pure implementation
  details with no effect on how elements relate. When in doubt, don't.

## 3. Conflicts = two head decisions on one element

If two people changed the same element concurrently, it has two head decisions —
a branch. `kg conflicts` lists them. Resolve by recording one decision that changes
that element again (it supersedes both heads), with a rationale for the resolution.

## 4. Sync

`kg sync` commits, pulls + union-merges teammates' decisions (per-install log shards,
never a rebase), replays, pushes, and reports any new conflicts.

## Schema (for `kg query`)
Nodes: `Element(id, kind, name, props)`, `Decision(id, title, rationale, author,
recorded_at, lamport)`. Rels: `LINK(kind, created_by)` Element→Element (current state),
`SHAPES` Decision→Element (provenance), `SUPERSEDES` Decision→Decision (evolution).
A decision is a **head** for an element if no other decision that also `SHAPES` it
supersedes it.
