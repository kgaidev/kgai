---
description: Show the full immutable evolution of a domain element — the chain of decisions that shaped it — in the kgai knowledge graph.
---

Show the evolution of: **$ARGUMENTS**

1. If unsure of the exact element, first `kg search "$ARGUMENTS"` to find its name/id.
2. Run `kg history "<element kind:name or id>"`. A bare name matching several
   elements returns `candidates` — pick one and re-run. If the result lists
   `same_name` elements, mention them (same name under another kind, with their own
   history); `merged` elements are ALIAS_OF twins already folded into this timeline.
3. Present the decisions oldest→newest as a timeline: title, rationale (why), author,
   when, and what each one changed (`mutation`). Mark the current head. Explain *how
   and why* the element changed over time.
4. To see the whole graph's shape at a past point, use `kg as-of <YYYY-MM-DD>`.
