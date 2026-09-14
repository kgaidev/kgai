# kgai — shared decision memory

`kg` is on your PATH and prints JSON. It holds this project's **decision graph**: domain
elements (feature:Invoice, service:Billing) shaped by an immutable log of decisions that
carry who decided, why, and when.

- **Before** a non-trivial change: `kg context --paths "<files you are about to edit>"`
  or `kg search "<the task in the team's words>"`. Respect what it returns, or supersede
  it deliberately.
- **After** a structural choice — splitting/merging/moving a feature, changing a
  dependency or ownership, changing how something is exposed, deprecating a prior
  choice, renaming a domain element — record it with one `kg ingest`. No permission
  needed. Behavior-preserving refactors, bug fixes and code-level renames are not
  decisions; do not record them.
- **"Why is X like this?"** → `kg history "feature:X"`.

The `knowledge-graph` skill carries the full rules and the `kg ingest` payload shape.
Activate it before recording anything.
