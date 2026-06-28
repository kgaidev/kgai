---
description: Query the kgai knowledge graph for prior decisions and knowledge relevant to a topic, file, or the current task — before making a change.
---

The user wants to know what the knowledge graph already says about: **$ARGUMENTS**

Use the `kg` CLI (JSON output) to answer:

1. If `$ARGUMENTS` names files/paths, run `kg context --paths "<comma-separated paths>"`.
   If it's a topic, run `kg context --about "<topic>"` and `kg search "<topic>"`.
   If empty, infer the paths from the current task / recent diff and use `kg context --paths …`.
2. For any element that looks central, run `kg history "<element kind:name>"` to show how it evolved.
3. Run `kg conflicts --about "<topic>"` to surface unresolved branches.

Then summarize for the user:
- the relevant **elements** and their current links (the shape of the area),
- the **head decisions** that shaped them, each with its rationale,
- explicitly call out any decision that would **constrain or contradict** the change
  they're considering — they must respect it or consciously supersede it,
- note any unresolved conflict branches.

Do not record anything; this is read-only.
