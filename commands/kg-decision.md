---
description: Record a structural decision into the kgai knowledge graph as graph mutations (add/retire links, upsert elements, set properties). Immutable; supersedes prior decisions on the same elements.
---

Record a decision that reshapes the domain element graph. Topic: **$ARGUMENTS**

Follow the `knowledge-graph` skill. Steps:

1. **Identify the elements involved** (domain things: features, business objects,
   services, components) and check whether they already exist:
   `kg resolve "feature:Invoice"`. Reuse exact existing names — identity is by name.
2. **Express the change as mutations** — what becomes true about the graph:
   `upsert_element`, `add_link` / `retire_link` (with a relationship `link` kind like
   PART_OF / DEPENDS_ON / RENDERS), `set_prop`. Set a `paths` prop on elements so
   `kg context --paths` can find them later.
3. **Gather** title, rationale (why, what was rejected), author, and any external ref.
4. **Propose ONE `kg ingest`** (show it), and on confirmation run it:

   ```bash
   kg ingest <<'JSON'
   { "decision": {
       "title": "…", "rationale": "…", "author": "…",
       "refs": [{"system":"clickup","url":"…"}],
       "mutations": [
         {"op":"upsert_element","kind":"feature","name":"Invoice","props":{"paths":"src/billing/*"}},
         {"op":"retire_link","from":"feature:Invoice","link":"PART_OF","to":"feature:Ceník"},
         {"op":"set_prop","element":"feature:Invoice","key":"display","value":"standalone"}
       ] } }
   JSON
   ```

5. Report the decision id and the elements it shaped. The decision automatically
   supersedes the prior head decision(s) of those elements (history chains itself).

Only record genuinely structural choices (see the skill's DO/DON'T).
