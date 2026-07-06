---
description: Knowledge-graph-aware task review — first pull relevant prior decisions, then at the end capture any decisions made, with relationships.
---

Run a knowledge-graph-aware review/preparation of: **$ARGUMENTS** (or the current task/diff).

**Phase 1 — ground in the current shape (read):**
1. Determine the files/areas/elements in scope (from `$ARGUMENTS`, the task, or the diff).
2. `kg context --paths "<those paths>"` and/or `kg context --about "<element/topic>"` —
   see the relevant elements, their current links, and the decisions that shaped them.
3. `kg conflicts --about "<topic>"` to spot unresolved branches.
4. Summarize the current shape and flag any decision the work would contradict —
   respect it or plan to supersede it.

**Phase 2 — do the review** as usual, keeping that structure in mind.

**Phase 3 — capture (write, at the end, consolidated):**
- Identify **structural** choices made during the review: split/merge/move a feature,
  change a dependency or ownership, change how something is rendered/exposed,
  deprecate a prior structural choice.
- Skip noise (refactors, renames, formatting, anything derivable from code).
- Record ONE `kg ingest` decision whose `mutations` reshape the graph
  (`upsert_element` with a `paths` prop, `add_link`/`retire_link`, `set_prop`) —
  **automatically, without asking permission**. The decision auto-supersedes the prior
  head decisions of the elements it changes. Then tell the user in one line what you
  recorded (they can adjust or retract it).

Use the `knowledge-graph` skill for the exact `kg ingest` schema and rules.
