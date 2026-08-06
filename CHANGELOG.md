# Changelog

All notable changes to the kgai plugin are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions match the
git tags (`vX.Y.Z`) and `.claude-plugin/plugin.json`.

## [Unreleased]

### Added
- **Three-layer configuration, with a custom capture prompt as one of its keys.**
  Settings resolve session (`<store>/kg.config.json`) → project (`<repo>/.kgairc`,
  committed) → global (`~/.kgai/config.json`), most specific wins, same shape in every
  file. The new `prompt` key holds capture rules — what counts as a decision in this
  repo, how elements are named — and a SessionStart hook hands them to the agent in
  front of the knowledge-graph skill, so a repo's conventions arrive with a clone
  instead of living in someone's head. Every key overrides rather than merges, so
  `kg config` always names the single layer a value came from. Identity and the cloud
  token deliberately do not layer. New `kg config` (get/set/unset, `--session` /
  `--project` / `--global`, values from stdin or `--from-file`) and `kg prompt`;
  `kg remote` is now a narrower view of the same resolution and reports every layer.
- **A committed `.kgairc` is approved before it takes effect (`kg trust`).** It is the
  one config layer nobody on the machine wrote — it arrives with `git clone`, from
  whoever made that repository — and two of its keys used to act on that authority
  alone. Reproduced before this landed: a committed `store` path overwrote a *different*
  repository's `.gitignore` and scattered the log through its working tree, and with a
  committed `remote` the SessionStart hooks pushed the developer's decisions (and, with
  the store pointed at `$HOME`, their `.ssh` and `.env`) to a remote the repo's author
  chose, then pulled that author's fabricated "team decisions" back into the log where
  `kg search` returns them as genuine history. Now: the file decides nothing until
  `kg trust`, the approval is bound to its content so any later commit asks again,
  `kg trust --show` prints what it asks for, and a pending file is reported by
  `kg config`, `kg prompt` and the session hook rather than silently ignored. Approving
  happens in the session — Claude shows the store path and the rules and waits for an
  answer (`/kgai:kg-trust`), and never approves on its own initiative — so nobody has to
  leave the conversation for a terminal. Approvals are stored per machine in
  `~/.kgai/trusted.json`, never in the repo, where one person's approval would travel to
  everyone who clones it. What is approved is the SETTINGS the file asks for, not its
  bytes — reformatting asks nothing new, a changed rule asks again, and one approval
  covers every repo asking for the same thing, so a company standard is accepted once per
  machine (inherited approvals are announced once). The whole model is documented in
  [docs/CONFIGURATION.md](docs/CONFIGURATION.md).
- **`remote` and `cloud_url` cannot be set in a committed file at all.** Syncing belongs
  to the store, not to one repository — several repos can share one store, and one log
  cannot push to two places depending on which repo the session started in — and
  `cloud_url` is the address the install-local cloud token authenticates against. Both
  are refused on write and ignored on read, and `kg config` lists them under
  `ignored_keys` so the reason is visible.
- **A `store` value that would damage something is refused, not obeyed.** An unset
  `${VAR}` used to expand to `""`, resolve to the repository root, and let store init
  overwrite that repo's own `.gitignore`/`.gitattributes`. Now an unresolved variable is
  an error naming the variable; the repository root, the home directory, and any
  directory already holding someone else's files are refused; symlinks are resolved
  before the checks so a link cannot point them at one directory while writes land in
  another; and the store's scaffold refuses to overwrite a `.gitignore` it did not write.
- **A broken store setting stops the command instead of quietly moving the graph.**
  A corrupt or conflicted `.kgairc` (a routine state for a committed file) used to be
  swallowed, and decisions were recorded into a freshly minted per-project store that
  nobody reads. `kg config` still answers — it is the command you run to find out what is
  wrong — and reports `store_error`.
- **Capture rules reach the model inside a delimiter the file cannot forge.** The fence
  around injected rules was a fixed string, so rules text containing that string put the
  rest of itself outside the data block, next to the instructions. The delimiter now
  carries a per-session random tag, the boundary is restated after the data, and the
  8,000-byte cap is enforced where the cost is actually paid — on read, since a
  hand-written `.kgairc` never goes through `kg config set` (a 40 KB value was being
  injected verbatim into every session).
- **Several repositories can share one knowledge graph, without per-repo setup.** The
  store location became a layered key too: committing `store` in a repo's `.kgairc`
  enrolls it in a shared decision log, and every clone follows without per-developer
  setup — previously only `KGAI_STORE`, which each developer had to export in every
  shell, could do this. Repos that are not enrolled keep their own log, so a side
  project never lands in the company graph; the global layer can still redirect every
  repo on a machine, which suits a managed dev box and little else. Values take `~`,
  `${VARS}` and repo-relative paths (anchored to the repository root), so one committed
  value resolves on every machine. `store` is refused in the session layer, which lives
  inside the store it would point at.
  `kg config` reports the resolved `store_root` alongside the layer that decided it, and
  the session-start installer now asks the engine whether a store exists instead of
  guessing the per-project path. Worktrees keep sharing one graph AND one configuration:
  the `.kgairc` that governs is the main worktree's, so a branch editing it cannot
  repoint its worktree at another store. See [docs/SHARED-STORE.md](docs/SHARED-STORE.md).
- **`kg help` explains the model, not just the verbs** — what elements, decisions,
  supersession and conflicts are, the read-before/record-after flow, what belongs in the
  log and what does not, where files live, and the JSON output contract. An agent that
  runs `kg help` without the skill loaded now gets enough to use the tool correctly.

### Fixed
- **The `kg` launcher reaches your terminal's PATH on macOS.** v1.4.0 decided whether
  `~/.local/bin` needed a profile line by looking at the PATH of the process running the
  hook. That is Claude Code's environment, not your Terminal's: it already carries
  `~/.local/bin` (that is where the Claude Code CLI installs itself), so the installer
  concluded there was nothing to do and wrote no profile line — and a fresh Terminal, which
  builds its PATH from the login files, still answered `command not found: kg`. The
  question is now asked of the files that actually build a terminal's PATH — `/etc/paths`,
  `/etc/paths.d`, the system profiles, and the login shell's own rc files — matching
  `~/…` and `$HOME/…` as well as the expanded path, so nothing is written twice.
- **The profile line lands in the file your terminals read.** The target file was chosen
  from `$SHELL`, which is merely whatever shell started Claude Code — on a Mac whose
  Terminal windows are zsh it is often `/bin/bash`, and the line went to `.bash_profile`,
  which zsh never reads. The login shell now comes from the account record (`dscl` on
  macOS, the passwd entry elsewhere), with `$SHELL` as the fallback.
- **An engine that cannot run is no longer reported as ready.** The installer announced
  "engine ready" for a binary it had never executed, so a Mac whose engine could not load
  `libkuzu.dylib` — or was refused by the OS — looked healthy while every `kg` command
  failed silently behind the hooks. Each install now runs `kg version` (the cheapest
  command that still loads the native library) before saying it is ready: a prebuilt that
  will not run falls back to a source build, and an engine that stops running is reported
  loudly, once per session, with the one-line repair.

## [1.4.0] - 2026-08-04

### Fixed
- **`kg` now works in your own terminal, not only inside Claude Code.** The plugin's
  `bin/kg` shim is only on the PATH Claude Code hands its Bash tool, so every `kg init` /
  `kg sync` / `kg context` the README asks you to run was `command not found` outside a
  session — on every platform. The installer now writes a launcher to `~/.local/bin/kg`
  (a script, not a symlink: the macOS rpath is `@loader_path/../lib` and resolves against
  the running binary's own directory) and, only when that directory is missing from
  `PATH`, appends one marked line to the shell profile the login shell actually reads.
  An unrelated `kg` already there is left alone. `KGAI_USER_BIN` overrides the location.
- **The engine updates again on macOS.** The install fingerprint was hashed with
  `sha256sum`, which does not exist on macOS — it came out empty, matched the empty file
  the previous run had written, and the "already current" fast path then skipped every
  reinstall forever. Macs kept running whatever engine they first downloaded, so plugin
  updates never reached them. Hashing now falls back to `shasum`/`openssl`, and the
  fingerprint always carries the plugin version, so it can never be empty again. Stuck
  installations repair themselves on the next session start.
- **Source builds on macOS produce a loadable engine.** The build passed
  `-rpath,$ORIGIN/../lib` on every platform; dyld has no `$ORIGIN`, so a Mac that fell
  back to building from source (prebuilt download blocked or unavailable) got a binary
  that could not load `libkuzu.dylib` at all. macOS now builds with `@loader_path`.
- **Automatic team sync runs on macOS.** The Stop/SessionStart hook spawned the sync via
  `setsid`, which is util-linux only; on a Mac the spawn failed silently and background
  sync never happened. Falls back to `nohup` where `setsid` is absent.

### Added
- **By-hand install for the CLI**, documented in the README and on kgai.dev:
  `curl -fsSL https://raw.githubusercontent.com/kgaidev/kgai/main/scripts/install.sh | bash`
  installs the same engine and launcher without Claude Code.

## [1.3.0] - 2026-08-03

### Changed
- **Analyses and reports are not decisions.** The skill's DON'T list — and the Stop
  hook — now explicitly exclude analyses, research findings, cost/status reports and
  recommendations nobody has acted on. When an analysis produces a real choice, the
  model records THE CHOICE with a short why, not the analysis; volatile figures
  (prices, counts, billing) stay out of the immutable log. The capture rules stay
  topic-agnostic: they describe kinds of change, and a decision about any kind of
  element (a feature, a service, a business object) was and remains in scope.
- **`kg ingest` rejects unknown fields instead of silently dropping them.** A model
  that invents an input field — typically mirroring the ingest OUTPUT shape, e.g.
  `"elements": [...]` — used to record a decision with no mutations and never learn
  why it was unfindable. Unknown fields anywhere in the payload now fail with a
  message listing the valid fields and showing how elements are attached (via
  `mutations`), so the model corrects itself on the spot.

## [1.2.0] - 2026-08-03

### Added
- **Automatic background team sync.** With a remote configured, a plugin hook now
  fires `kg sync --auto` at session start and after every turn — detached and
  fire-and-forget (~1 ms in the hook), so neither the user nor the model ever waits
  on the network. The auto mode is engineered to be a safe no-op everywhere else:
  no store or no remote → silent exit in a few milliseconds (never creates a store);
  a sync attempted less than 60 s ago → skipped (`--cooldown` to tune); the store
  lock held by another write → skipped via a new non-blocking `TryLock`, never
  queued. Real attempts record their outcome in `<store>/last-autosync.json`
  (including soft failures like expired SSO, which the S3 transport reports as
  `ok:true` + detail); a sync that isn't actually syncing surfaces once per session
  in the install status line — never louder. The store's `.gitignore` learns the new
  stamp/result files, and sync re-writes the scaffold so stores created by older
  engines pick that up before the git transport's `add -A` could commit them.

## [1.1.0] - 2026-08-03

### Added
- **`ok` in every successful JSON output.** `ingest`, `context`, `history`, `as-of`,
  `resolve` and `export` now carry `"ok": true` like the other commands, so an agent
  can uniformly check one field (the skill teaches exactly that).
- **Release checksums.** The build workflow publishes a `<asset>.sha256` next to every
  release asset, and `install.sh` verifies downloads against it — a mismatch discards
  the download and falls back to the source build. Releases without checksums (and
  machines without a sha256 tool) skip verification with a note.
- **Ingest warning for element-less decisions.** A decision whose mutations attach no
  element is recorded and searchable, but invisible to `kg context`/`kg history`
  (element-centric recall); ingest now says so. `kg search` additionally indexes ALL
  decisions — previously a decision with no shaped element was unfindable by every
  read command.

### Fixed
- **`kg context --paths` matches nested files and overlapping globs.** A stored
  `paths` prop ending in `/*` (the convention the skill itself teaches) was compared
  with the `*` as a literal byte in the prefix fallback, so `src/billing/invoice/*`
  missed `src/billing/invoice/sub/x.ts` and never overlapped `src/billing/*`. A
  trailing `/*` or `/**` now compares as its directory prefix.
- **Recording a note on an existing element no longer mints a false conflict.** The
  projection granted a targets-less decision authority over everything it shaped (a
  legacy-compatibility rule), while ingest computed supersession only from explicit
  targets — so a bare `upsert_element` of an existing element (the recorded-dead-end
  pattern) created a second head and a phantom branch. Authority now follows intent:
  an explicit upsert takes authority when it **creates** the element (its first head)
  or carries `props`; a bare upsert of an existing element is provenance-only —
  visible in search and history, supersedes nothing, cannot conflict. New events
  carry `provenance_only` so replay distinguishes them from legacy events; existing
  logs verify and replay unchanged. `kg context --about` now also matches the
  vocabulary of provenance-only decisions (a dead end is often exactly what the
  question is about).
- **`kg as-of <YYYY-MM-DD>` now means the END of that day.** A bare date was parsed as
  midnight UTC, so asking about today silently dropped everything recorded today.
- **`kg history` orders by real time.** The timeline is ordered by `recorded_at`
  (lamport breaks ties), so decisions imported with a back-dated `date` appear where
  they belong instead of at their import position.
- **`set_prop` values and `props` accept any JSON scalar.** `"value": 3` or
  `"visible": true` no longer fail with a Go unmarshal error; numbers, booleans and
  null are stored in canonical string form.
- **`kg query` refuses file/database I/O.** The projection was already opened
  read-only (graph writes fail), but Kuzu's `COPY … TO` could still write arbitrary
  files. COPY/LOAD/EXPORT/IMPORT/ATTACH/DETACH/INSTALL statements are now rejected;
  string literals are ignored by the check, so querying *about* those words works.
- **One rename rule everywhere.** The skill's description said "renaming" records a
  decision while its DON'T list said renames don't — and the Stop hook contradicted
  itself within one paragraph. Now uniformly: renaming a *domain element* (its
  canonical name changes) records; code-level renames (files, functions, variables)
  don't. The skill also states precisely which mutations take authority (and thus
  supersede/conflict): `set_prop`, an upsert that creates the element or carries
  `props`, and the `from` side of links — bare upserts of existing elements and link
  targets are provenance only.

### Changed
- **Read commands no longer create a store.** Previously any `kg` command lazily
  initialized `<cwd>/.kgai/store` when none existed — so a read run in the wrong
  directory (outside any git project) silently minted a new empty graph there and
  answered "no record", forking the project's memory. Reads (`search`, `context`,
  `history`, `as-of`, `conflicts`, `resolve`, `query`, `export`, `status`, `doctor`,
  `rebuild`, `rotate`, and `kg remote` without a URL) now return their empty result
  shape with `"ok": true` and a `note` explaining that nothing is recorded there yet.
  Nothing changes for plugin users: the store is still created automatically — at
  session start, by the first recorded decision (`kg ingest`), by `kg sync`, by
  setting a remote, or by an explicit `kg init`. A corrupt store still fails loudly
  rather than reading as empty.

## [1.0.0] - 2026-07-29

kgai is **stable**. The jump from 0.1.x is deliberate: the plugin has been running in
real daily use — recording and recalling decisions on active projects, including this
repository itself, whose own knowledge graph is maintained with kgai — and the core has
held up in practice: the event model, the deterministic projection, S3 team sync
(exercised up to 1,000,000-decision stores), conflict detection and the Claude Code
integration all work as designed. A 0.x version signals "expect breakage"; that no
longer describes this software, so the version now says what the usage already shows.

Stability promise from here on: the on-disk log format, the `kg` CLI surface and the
JSON output shapes follow semver — breaking changes to any of them mean a major
version bump. (Schema/log compatibility was already guaranteed before; now the version
number carries that promise too.)

### Added
- **Global default sync remote** — `kg remote --global "s3://bucket/kg/{project}"` sets a
  machine-wide default in `~/.kgai/config.json`, used by every project that has no remote
  of its own; `{project}` expands to the project directory's name so each project keeps
  its own prefix (omit it deliberately to share one graph). A project's local remote
  always wins, and the local sentinel `kg remote none` opts a project out entirely. New
  `kg remote` command shows/sets/unsets both levels; `kg status` reports the effective
  remote and its source (`remote_source: local | global | disabled`).

## [0.1.12] - 2026-07-29

### Fixed
- **`git worktree` no longer starts an empty graph** (#4). A linked worktree resolved to
  itself as the project, so `git worktree add ../feature-x` produced a second, empty
  store in that directory: `/kgai:kg-ask` returned nothing and decisions recorded there
  were stranded, reachable only through `kg sync`. Worktrees now resolve to the main
  worktree, so every worktree of a project reads and writes one graph — matching the
  design, where the KG is per project and deliberately branch-agnostic. Submodules are
  unaffected: they remain their own project. `scripts/install.sh` resolves the root the
  same way, so it no longer re-initializes the store on every run inside a worktree.

  *Upgrading:* if you already recorded decisions while working inside a worktree, they
  are in `<worktree>/.kgai/store` and the plugin will now look in the main worktree
  instead. Point `KGAI_STORE` at the old path to read it, or `kg sync` both stores
  against one remote to merge them.

## [0.1.11] - 2026-07-28

### Added
- **`kg context --about` reads the decision texts, not just element names.** A question
  phrased in the words of what was decided — "should I hide drafts from the list?" —
  now surfaces the element that decision shaped (superseded dead ends included), even
  when it shares no word with the element's name. Same deterministic lexical scorer as
  `kg search`, no embeddings; naming the element directly remains the strongest signal.
  Costs one scan of the decision texts, paid only by `--about` queries (+56 ms at 10k
  decisions, ~+0.6 s at 100k).
- `warmbench` dev tool — times the individual Cypher reads behind `context`/`search`
  with the graph held open, separating query cost from CLI startup cost.

### Changed
- **`kg context` is ~2× faster at scale.** Head decisions are resolved after ranking,
  for the returned elements only, instead of graph-wide on every read (the head query
  alone: 633 ms → 43 ms at 1,000,000 decisions; cold `kg context` 1.7 s → 0.9 s).
  Output is byte-identical.
- The skill now tells the model it is the semantic layer: matching is word overlap by
  design, so rephrase with the recorded vocabulary before concluding "no record".

### Fixed
- **`kg conflicts` output is deterministically ordered** — competing heads newest-first,
  elements by id. Previously the order was whatever the scan produced, so the same store
  could describe a branch two ways on consecutive reads. (Canonical export was never
  affected.)

## [0.1.10] - 2026-07-24

### Added
- **`kg status` command** (alias `kg info`) — a fast, at-a-glance snapshot of the
  store: identity, whether a sync remote / cloud token is configured
  (`remote_configured`, `sync_transport`, `cloud_configured`), and live graph counts.
  Distinct from `kg doctor`, which stays the integrity/health check (it verifies hash
  chains); `status` skips that work so it's cheap on large stores.
- **AWS/SSO profile per S3 remote** — the `s3://` remote URL now accepts
  `?profile=NAME&region=REGION`. `profile` pins a named shared-config profile (including
  an SSO profile — run `aws sso login --profile NAME` first) to *this* store instead of a
  global `AWS_PROFILE`; `region` overrides the profile/env region. An empty profile keeps
  the full standard AWS credential chain. No new dependency.

### Changed
- **`kg version` reports the release version.** The plugin version from
  `.claude-plugin/plugin.json` is stamped into the binary at build time (`-ldflags -X
  main.version`) and shown alongside `schema_version`. CI now fails a release if the
  pushed tag does not match `plugin.json`, so binaries can never ship with a stale version.
- Sync documentation converged on verified behavior (S3 supported, git experimental).

[0.1.11]: https://github.com/kgaidev/kgai/releases/tag/v0.1.11
[0.1.10]: https://github.com/kgaidev/kgai/releases/tag/v0.1.10
