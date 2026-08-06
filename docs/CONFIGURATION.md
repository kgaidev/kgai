# Configuration, and what it is allowed to do

kgai reads settings from three files. One of them — `<repo>/.kgairc` — is committed, so
it arrives on your machine with `git clone`, written by whoever made that repository.
That single fact shapes everything on this page: **configuration that travels with a
clone is untrusted input, not configuration.**

This page is the whole model in one place: where settings live, which key may live where,
what a committed file can and cannot cause, and what is left as residual risk.

## The three layers

Resolution is most-specific-first, and every key **overrides** — nothing merges — so
`kg config` can always name the one layer a value came from.

| Layer | File | Written by |
|---|---|---|
| **session** | `<store>/kg.config.json` | this install (`kg init`, `kg remote`, `kg config`). Holds the cloud token, never committed |
| **project** | `<repo>/.kgairc` | **committed** — travels with the repo to everyone who clones it |
| **global** | `~/.kgai/config.json` | this machine, only when you ask for it |

`KGAI_STORE` in the environment beats all three, for a one-off run.

All three hold the same JSON shape; only the location decides the layer, the way npm
layers `.npmrc`. The name follows the rc convention, the content is JSON — one schema
across layers is the point.

`.kgairc` is looked for at the repository root and **nowhere else** — no search upward
from the working directory. One project has one config: a file in a subdirectory (or one
carried by a vendored tree) would otherwise win over it and the same repo would resolve to
two different stores depending on where the shell stood. The root is the *main* worktree's,
so every `git worktree` of a project — including one nested at `<repo>/.worktrees/x` —
reads the same file and records into the same graph. A change to it takes effect once it
is merged and checked out there, exactly as the store itself ignores branches.

## Which key may live where

This is enforced on **both** paths: a write to the wrong layer is refused, and a value
found in the wrong layer is ignored on read. The read side is the load-bearing half —
a committed file never went through `kg config set`.

| Key | What it does | Allowed layers | Why |
|---|---|---|---|
| `prompt` | capture rules given to the agent each session | session, project, global | the reason `.kgairc` exists: a team's conventions should arrive with the repo |
| `store` | where the decision log lives | project, global | the session file lives *inside* the store, so it cannot choose it |
| `remote` | sync target (and, by its scheme, the transport) | session, global | syncing belongs to the **store**, not to one repo: several repos can share one store, and one log cannot push to two places depending on which repo you started in. It also stops a cloned file from choosing where your decisions are uploaded |
| `cloud_url` | kgai cloud broker address | session | it is the address your install-local `cloud_token` authenticates against, so it stays beside the token |

Keys present but not allowed are listed by `kg config` under `ignored_keys`, so a
committed `remote` is visibly ignored rather than mysteriously ineffective.

Identity — `install_id`, `actor`, `machine`, `retired_installs` — and `cloud_token` do
not layer at all. They belong to one install and stay in `kg.config.json`.

### `remote` vs `cloud_url`

Not two versions of the same thing. `remote` says **where this store syncs**, and its
scheme picks the transport:

| `remote` | transport |
|---|---|
| `s3://bucket/prefix` | S3 (supported) |
| a git URL | git (experimental) |
| `kgai://org/project` | kgai cloud (closed beta) |

`cloud_url` is the **address of the server** for that last transport — `kgai://org/project`
names the project, not the broker hosting it — and it travels with `cloud_token`. For S3
and git it is unused. In npm terms: `remote` is like `git remote add origin`, `cloud_url`
is like `registry`.

## Approval: a committed config decides nothing until a person accepts it

`.kgairc` is the one layer nobody on this machine wrote. Until it is approved:

- its `store` is ignored — the project keeps its own `<project>/.kgai/store`
- its capture rules are **not** injected into the session
- `kg config`, `kg prompt` and the session hook all report `pending_approval`

Nothing is blocked meanwhile. You keep working; you just do not get the repo's settings.

### How you approve

**In the session, not in a terminal.** The hook tells the agent that a config is pending;
the agent runs `kg trust --show` (which approves nothing), shows you the store path and
the rules the file asks for as quoted content from the repository, and waits for your
answer. `/kgai:kg-trust` starts that on demand. The agent must never approve on its own
initiative — that is the entire point of the step.

By hand it is the same flow:

```bash
kg trust --show    # what this repo's config asks for; approves nothing
kg trust           # approve
kg trust --revoke  # withdraw
kg trust --list    # every configuration approved on this machine
```

### What exactly is approved

**The settings the file asks for — not its bytes.** The fingerprint is a hash of the
values of the keys this layer may set (`prompt`, `store`), canonically ordered. So:

- reformatting the file, reordering keys, adding a `_comment` → **no new question**
- changing a rule or the store path → **asks again**, whether you edited it or a
  teammate's commit did
- adding a `remote` to it → no new question; that key is ignored anyway, so it cannot
  change what approval means

Because the fingerprint is the configuration, one approval covers **every repository
asking for the same thing** — a company that standardizes one `.kgairc` across twenty
repos is asked once per machine, including for repos created later. When a repo becomes
live that way, it is announced once (`approval_inherited_from`) and then stays quiet:
an inherited approval nobody mentions is indistinguishable from no approval at all.
`kg trust --revoke` withdraws the configuration everywhere it was inherited.

### Where approvals are stored, and why not in the repo

`~/.kgai/trusted.json`, per machine and per user, never synced:

```json
{
  "sha256:9f86d0…": {
    "approved_at": "2026-08-06T09:12:44Z",
    "paths": ["/home/alex/work/shop-api/.kgairc"]
  }
}
```

`paths` is provenance, not identity — it records where a configuration was accepted from
so an inherited approval can be attributed.

It cannot live in `.kgairc` itself, and that is not an implementation detail:

1. **The file is committed.** An approval stored there would be committed too, and would
   then pre-approve the file for everyone who clones it — exactly the people the step
   protects. An attacker would simply write the approval into their own repo.
2. **It would sign itself.** The approval is a hash of the file's settings; storing it
   inside the file changes what it hashes.

Every member of a team therefore approves once on their own machine, which is the same
model as direnv (`~/.local/share/direnv/allow`) and git's `safe.directory`.

## What a committed config can and cannot cause

Approval is consent to a *configuration*, not a blank cheque. These limits hold whether
or not you approved:

- **`remote` and `cloud_url` are never taken from it.** So a cloned repo cannot choose
  where your decisions are uploaded, nor point your cloud token at a foreign broker.
- **A `store` value that would damage something is refused**, with the reason:
  - an unresolved `${VAR}` (it used to expand to `""`, resolve to the repository root,
    and let store init overwrite that repo's own `.gitignore`)
  - the repository root, `.`, or your home directory
  - a directory that already holds someone else's files
  - symlinks are resolved *before* these checks, so a link cannot point the checks at one
    directory while writes land in another
- **The store's own `.gitignore` is machinery, not a preference.** With a git remote it
  decides what `kg sync` sends, and `log/` is deliberately absent from it: the decision
  shards are the payload. kgai maintains that file — a line you add is kept, unless it
  would stop the shards syncing (`log/`, `*.ndjson`) or switch off one of the rules that
  keeps the install-local config and the derived graph out of the team's repo
  (`!kg.config.json`, `!graph.kuzu`). Those are dropped at the next sync, along with any
  conflict markers a merge left behind. S3 remotes never read it: they upload event
  segments, not the directory.
  If an earlier sync already committed something the scaffold excludes, a **git** sync
  untracks it on the next run — you do not have to. Two caveats worth knowing: it is
  removed from the repository's current tree, not from its history, so what an earlier
  sync pushed stays in the commits that carried it. **If you synced with an engine released
  before this one and the store's ignore file was damaged at the time, check whether
  `kg.config.json` is in your team repository's history** — those versions staged whatever
  the ignore file did not cover, and that file holds your cloud token. Untracking does not
  remove it from history: rotate the token. And an **S3**
  store never commits the directory at all, so nothing untracks a file a previous git
  remote left tracked — harmless, since nothing is pushed from there either, but it stays
  in the local repo until you run `git rm --cached` in it yourself.
- **The store never overwrites files it did not write.** Its scaffold (`.gitignore`,
  `.gitattributes`) refuses to replace a file without kgai's own marker.
- **A broken or conflicted `.kgairc` stops the command** instead of quietly resolving to
  a different store — a merge conflict in a committed file is an ordinary event, and
  silently recording into a freshly minted store is how decisions end up in a graph
  nobody reads. `kg config` still answers, with `store_error`, because it is the command
  you run to find out what is wrong.
- **Capture rules are framed as data.** They are injected inside a delimiter carrying a
  per-session random tag, so text inside them cannot close the block and pose as
  instructions; the boundary is restated after the data; and anything past 8,000 bytes is
  truncated. The cap is enforced on *read*, because a hand-written `.kgairc` never passes
  through the writing code.

## Residual risk, stated plainly

- **Rules are still text a model reads.** The framing is strong (unforgeable delimiter,
  data-before-instruction ordering, an explicit boundary) but it is not a sandbox. Rules
  you approve can still shape what gets recorded — that is what they are for. Read them
  before approving; `kg trust --show` exists for that.
- **One approval covers identical configurations.** If your standard `.kgairc` is public,
  someone can copy it into their own repo; cloning that repo would then enroll it into your
  shared store without asking, because it asks for exactly what you already accepted. It
  gains nothing beyond what you granted that configuration, but it is a real difference
  from per-file approval. Keep company store paths out of public repos, or approve per
  repo by keeping something repo-specific in the rules.
- **The agent can run `kg trust` if you tell it to.** The skill forbids doing it
  unprompted, and an unapproved file has no channel into the conversation to argue for
  itself — but if you say "approve it" without reading, it will.
- **A store is one machine and one user.** `kg.config.json` is written 0600 and carries a
  single install identity, so two people sharing one directory on a build box will hit
  permission errors or one shard with two writers. Teams converge through the remote
  (`kg sync`), not through a shared filesystem. A configured store that is not reachable
  now says so in the empty answer instead of reading as "the team has no history".
- **The first session on a new machine may miss the notice.** The hook needs the engine,
  which the same SessionStart group installs; if they run in parallel it exits silently
  and you see the notice from the next session on.

## Related

- [README — Configuration](../README.md#configuration) — the short version
- [SHARED-STORE.md](SHARED-STORE.md) — several repositories sharing one decision log
- [ARCHITECTURE.md](ARCHITECTURE.md) — where every file lives
