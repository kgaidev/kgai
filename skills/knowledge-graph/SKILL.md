---
name: knowledge-graph
description: >-
  Records and retrieves engineering decisions in the kgai knowledge graph (a small,
  stable graph of domain ELEMENTS — Invoice, Pricing, Checkout… — shaped by an immutable
  log of DECISIONS). INVOKE THIS SKILL AUTOMATICALLY, WITHOUT WAITING TO BE ASKED,
  WHENEVER you make or implement a structural/architectural choice: splitting, merging,
  moving, or renaming a module/feature; changing a dependency, ownership boundary, or how
  something is exposed/rendered; deprecating or replacing a prior decision; or finishing
  ANY task in which such a choice was made. The skill then records it for you (no
  confirmation needed) — capturing the decision is part of completing the task, not
  optional bookkeeping, so do not end your turn with an uncaptured structural decision.
  ALSO invoke it BEFORE a non-trivial change to read prior decisions you must respect,
  and to answer "why is X like this", "how does X relate to Y", "what changed and when".
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
change a dependency, mark how something is rendered/owned — **record it automatically.
You do NOT need to ask permission first.** At the end of the task, run ONE consolidated
`kg ingest` whose `mutations` reshape the graph, then tell the user in one line what you
recorded (and that they can adjust or retract it). The only gate is the DO/DON'T rules
below — apply them strictly so you capture genuine structural decisions and nothing else.

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

If the same element was changed concurrently (e.g. from two sessions), it has two head
decisions — a branch. `kg conflicts` lists them. Resolve by recording one decision that
changes that element again (it supersedes both heads), with a rationale for the resolution.

## Schema (for `kg query`)
Nodes: `Element(id, kind, name, props)`, `Decision(id, title, rationale, author,
recorded_at, lamport)`. Rels: `LINK(kind, created_by)` Element→Element (current state),
`SHAPES` Decision→Element (provenance), `SUPERSEDES` Decision→Decision (evolution).
A decision is a **head** for an element if no other decision that also `SHAPES` it
supersedes it.
