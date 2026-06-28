---
description: Sync the kgai knowledge graph with the remote — commit, pull/merge teammates' decisions, replay, push. Reports new conflicts.
---

Synchronize the knowledge graph with the team remote.

1. Run `kg sync`.
2. Report: how many remote events were applied, whether the push succeeded, and the
   remote used. If `detail` mentions offline/no-remote, say so plainly.
3. If `conflict_count > 0`, list the conflicting elements and recommend running
   `/kgai:kg-conflicts` to resolve them.

If no remote is configured yet, tell the user they can set one with:
`kg init --remote <git-url>` (the store is a dedicated git repo, independent of any
project repo).
