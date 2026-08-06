# One knowledge graph across many repositories

By default each project keeps its own decision log in `<project>/.kgai/store`. That is
the right default for open source and for teams whose repos are genuinely separate
products.

It is the wrong default for a company where the same people work across `shop-api`,
`billing`, `web` and `infra`, and the decisions cross those lines constantly — *"payments
moved out of shop-api into billing"* is one decision about two repositories. Splitting it
into two logs means nobody's agent ever sees the whole story.

This page is about that setup: **the repositories that belong together record into one
graph in `/opt/kgai`, each developer's checkout picks that up by itself, and the repos
that don't belong keep their own log.**

The trust model behind that approval — what a committed config may ask for and what it
can never cause — is [CONFIGURATION.md](CONFIGURATION.md).

## The setting

`store` is a normal configuration key, so it resolves through the [three
layers](../README.md#configuration) like everything else — most specific first:

```
session   <store>/kg.config.json     (cannot set `store` — that file lives inside it)
project   <repo>/.kgairc             committed: every clone of this repo follows it,
                                     once the person on that machine approves it
global    ~/.kgai/config.json        every repo on this machine — see the warning below
```

`KGAI_STORE` in the environment still beats all of them, for a one-off run.

## Set it in the repositories that belong to the graph

Do it once per repository, and commit it:

```bash
cd shop-api
kg config set --project store '${HOME}/kgai'
git add .kgairc && git commit -m "kgai: record into the shared company graph"
```

Anyone who clones `shop-api` then approves it once. That happens inside their Claude
session — the agent shows the store path and the capture rules the file asks for and waits
for an answer (`/kgai:kg-trust` on demand; `kg trust --show` / `kg trust` by hand) — and
from then on their sessions record into the shared graph with nothing else to configure.
The approval is bound to the file's content, so a later commit that changes it asks again.
It is stored per machine in `~/.kgai/trusted.json`, never in the repo: an approval that
travelled with the clone would approve the file for everyone and defeat the step.

That one confirmation is the whole reason `.kgairc` can carry a store path at all: it is
the only layer nobody on the machine wrote. Until it is approved it decides nothing, and
`kg config` reports it as `pending_approval` rather than quietly falling back.

Repositories that were never told to join are untouched: your side project keeps its own
log in `<project>/.kgai/store`, which is what you want, because a shared company graph is
no place for it.

Verify from inside the repo:

```bash
$ kg config
{
  "effective": { "store": "${HOME}/kgai", … },
  "sources":   { "store": "project", … },
  "store_root": "/home/alex/kgai"
}
```

`store_root` is the resolved answer — placeholders expanded, `KGAI_STORE` honored. If it
does not say what you expect, `sources` names the layer that decided.

Because the value is committed, it has to resolve on **every** machine. Absolute paths
like `/opt/kgai` only work if your team standardizes on them (fine for managed dev boxes
and devcontainers); otherwise use the expansions:

| In `.kgairc` | Resolves to |
|---|---|
| `${HOME}/kgai` | the user's home directory, whatever it is |
| `${COMPANY_KG}/logs` | any environment variable |
| `~/kgai` | the home directory |
| `../shared-kg` | relative to the **repository root**, so sibling repos can share it |

### The global layer is rarely what you want

`kg config set --global store /opt/kgai` also works, and it catches **every** repository
on the machine — including the ones you never meant to enroll. Clone anything, start a
session, and its decisions land in the company graph; a personal side project ends up in
a log the whole team syncs. There is no opt-out short of setting `store` back per
repository.

It is defensible on a machine that exists for one purpose — a managed dev box, a
devcontainer, CI — where every checkout genuinely belongs to the same graph. Anywhere
else, enroll repositories one at a time.

## What sharing a store actually means

- **One log, one identity.** A shared store has a single `kg.config.json` and a single
  install id, so every enrolled repo writes into the same shard. That is exactly what
  makes the merged view work.
- **The store stops living inside the project.** Nothing is added to the project's
  `.gitignore`, because there is nothing to ignore there any more.
- **Team sync is per store, not per repo** — and enforced, not just advised. `remote`
  cannot be set in `.kgairc` at all: one physical log cannot push to a different place
  depending on which enrolled repo the session started in, and a cloned file must never
  choose where a developer's decisions are uploaded. Configure it once for the store
  (`kg remote s3://bucket/company-kg`).
- **Worktrees follow the main worktree.** `git worktree` checkouts of an enrolled repo
  read the `.kgairc` of the main worktree, not their own copy — the graph is
  branch-agnostic, so a branch must not be able to point its worktree at a different
  store. A change to `.kgairc` applies once it is merged and checked out in the main
  worktree.
- **`paths` properties become ambiguous.** `kg context --paths "src/billing/*"` matches
  on the pattern recorded with the element, and two repos may both have `src/`. In a
  merged store, record repo-qualified patterns (`billing/src/*`) so path lookup stays
  precise.

## Moving an existing project into the shared store

The setting only changes where the engine looks; it does not move data. A project that
already recorded decisions keeps them in `<project>/.kgai/store`, and after the switch
its agent sees the shared graph instead. To carry the history over, export the old log
and re-ingest it into the shared store:

```bash
cd your-project
kg export --canonical > /tmp/old-log.json    # from the old store, before switching
kg config set --project store /opt/kgai      # switch this repo over
# then replay the decisions into the shared store (see `kg ingest`; give each its real date)
```

Keep the old store directory until you have verified the shared graph answers the
questions you care about — nothing deletes it for you.

## Going back

```bash
kg config unset --project store    # this repo only; --global if you set it machine-wide
```

The repository returns to `<project>/.kgai/store`, and the per-project store that was
there before the switch is still exactly where it was.
