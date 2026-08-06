---
description: Sync the project's decision log with the team remote (S3 supported, git experimental) and rebuild the graph.
---

Run `kg sync` for the current project and report the result to the user.

1. Execute `kg sync` (the effective remote is used — this store's own, or the machine-wide
   default; if neither is configured, say so and point to `kg remote s3://bucket/prefix`,
   or `kg remote --global "s3://bucket/kg/{project}"` to cover every project at once).
   Flags come BEFORE the URL: `kg remote --global <url>`, never `kg remote <url> --global`.
2. Summarize the JSON result in one line: pushed? pulled how many decisions? conflicts?
3. If `conflict_count` > 0, list the conflicted elements and suggest `/kgai:kg-conflicts`.
4. If sync fails with a shard-fork error, explain that the store was likely copied from
   another machine and that `kg rotate` gives it a fresh identity, then re-run sync.

Notes: with a remote configured, sync also runs automatically in the background (at
session start and after each turn) — this command is for syncing on demand and for
seeing the result. S3 remotes are the supported path. Git remotes are experimental and
untested — warn the user if the remote is a git URL. Never run `kg rotate` without the user's
explicit confirmation. If S3 sync fails with an AWS credential/SSO error, the remote can
pin a profile — `kg init --remote "s3://bucket/prefix?profile=NAME"` — and the user may
need `aws sso login --profile NAME` first.
