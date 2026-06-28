---
description: Run a raw Cypher query against the kgai knowledge graph (power use / debugging).
---

Run a raw Cypher query: **$ARGUMENTS**

1. If `$ARGUMENTS` is Cypher, run `kg query "$ARGUMENTS"`. Otherwise translate the
   request into Cypher against this schema and run it:
   - Nodes: `Element(id, kind, name, props)`, `Decision(id, title, rationale, author,
     recorded_at, lamport)`.
   - Rels: `LINK(kind, created_by)` Element→Element (current links),
     `SHAPES` Decision→Element (provenance), `SUPERSEDES` Decision→Decision (history).
   - A decision is a **head** for an element when no other decision that also
     `SHAPES` it supersedes it.
2. Present the rows readably.

Examples:
- Current shape: `kg query "MATCH (a:Element)-[r:LINK]->(b:Element) RETURN a.name, r.kind, b.name"`
- Why an element is the way it is: `kg query "MATCH (d:Decision)-[:SHAPES]->(e:Element {name:'Invoice'}) RETURN d.title, d.rationale ORDER BY d.lamport"`
- Elements with no decisions yet: `kg query "MATCH (e:Element) WHERE NOT EXISTS { MATCH (:Decision)-[:SHAPES]->(e) } RETURN e.name"`
