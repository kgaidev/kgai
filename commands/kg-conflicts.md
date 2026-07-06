---
description: List and help resolve elements shaped by two competing head decisions (concurrent edits) in the kgai knowledge graph.
---

Find and help resolve conflicting decisions. Optional filter: **$ARGUMENTS**

1. Run `kg conflicts` (add `--about "$ARGUMENTS"` if a topic was given). Each result is
   an **element** with two or more competing head decisions (two people changed it
   concurrently without one superseding the other).
2. For each, run `kg history "<element kind:name>"` to show the competing decisions and
   how they diverged.
3. Explain each branch and recommend a resolution.
4. On the user's decision, record ONE new decision that changes that element again — it
   automatically supersedes both heads — with a rationale for the resolution:

   ```bash
   kg ingest <<'JSON'
   { "decision": { "title": "…resolution…", "rationale": "why this wins / how they merge",
     "mutations": [ {"op":"set_prop","element":"feature:Invoice","key":"resolved","value":"true"} ] } }
   JSON
   ```

5. Confirm `kg conflicts` is clear.
