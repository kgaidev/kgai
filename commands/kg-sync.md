---
description: Sync the project's decision log with the team remote (S3 supported, git experimental) and rebuild the graph.
---

Run `kg sync` for the current project and report the result to the user.

1. Execute `kg sync` (the store's configured remote is used; if none is configured,
   say so and point to `kg init --remote s3://bucket/prefix`).
2. Summarize the JSON result in one line: pushed? pulled how many decisions? conflicts?
3. If `conflict_count` > 0, list the conflicted elements and suggest `/kgai:kg-conflicts`.
4. If sync fails with a shard-fork error, explain that the store was likely copied from
   another machine and that `kg rotate` gives it a fresh identity, then re-run sync.

Notes: S3 remotes are the supported path. Git remotes are experimental and untested —
warn the user if the remote is a git URL. Never run `kg rotate` without the user's
explicit confirmation.
