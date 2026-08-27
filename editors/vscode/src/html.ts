// HTML for the detail panel: a decision, an element, a person, a contested element,
// the overview, and Help — built from the reader's data with the words kg uses, in
// the editor's own colours (the webview inherits VS Code's theme variables).

import type {
  Conflict,
  Count,
  Decision,
  DecisionRef,
  Element,
  ElementCount,
  Mutation,
  Overview,
  Person,
  State,
  Store,
} from "./reader";

export function esc(s: string | number | undefined): string {
  return String(s ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/** A link the panel turns into navigation (kind + id). */
export function nav(kind: "decision" | "element" | "person" | "conflict" | "decisions", id: string, text: string, extra = ""): string {
  return `<a href="#" class="nav" data-kind="${kind}" data-id="${esc(id)}" ${extra}>${esc(text)}</a>`;
}

const stateWords: Record<State, string> = {
  head: "in force",
  superseded: "replaced",
  note: "context only",
};

export function chip(text: string, cls: string): string {
  return `<span class="chip ${cls}">${esc(text)}</span>`;
}

export function stateChip(state: State): string {
  return `<span class="chip state-${state}" title="${esc(stateWords[state])}">${esc(state)}</span>`;
}

function kindChip(kind: string): string {
  return chip(kind, "kind");
}

function section(title: string): string {
  return `<h2>${esc(title)}</h2>`;
}

function mutationRows(ms: Mutation[]): string {
  if (ms.length === 0) {
    return `<p class="muted">Nothing structural — a note.</p>`;
  }
  return ms
    .map((m) => `<div class="mutation"><span class="chip op-${esc(m.op)}">${esc(m.label)}</span><pre>${esc(m.text)}</pre></div>`)
    .join("");
}

function decisionRefRow(r: DecisionRef): string {
  if (!r.inLog) {
    return `<div class="row">${chip("not in this log", "neutral")} <code>${esc(r.id)}</code></div>`;
  }
  return `<div class="row"><code class="day">${esc(r.day)}</code> <span class="dot" data-person="${esc(r.actor)}"></span> ${nav("decision", r.id, r.title)}</div>`;
}

/** Bars: a tally as label, proportional bar, count. */
export function bars(counts: Count[], opts: { nav?: "element" | "person" | "decisions"; ids?: Map<string, string>; limit?: number; cls?: (key: string) => string } = {}): string {
  if (counts.length === 0) {
    return `<p class="muted">nothing yet</p>`;
  }
  const shown = opts.limit && counts.length > opts.limit ? counts.slice(0, opts.limit) : counts;
  const max = Math.max(...shown.map((c) => c.n), 1);
  const rows = shown
    .map((c) => {
      const label = c.key === "" ? "(none)" : c.key;
      const id = opts.ids?.get(c.key) ?? c.key;
      const text = opts.nav ? nav(opts.nav, id, label) : `<span>${esc(label)}</span>`;
      const cls = opts.cls ? opts.cls(c.key) : "";
      return `<div class="bar-row"><div class="bar-label">${text}</div><div class="bar-track"><div class="bar ${cls}" style="width:${Math.max(1, Math.round((100 * c.n) / max))}%"></div></div><div class="bar-n">${c.n}</div></div>`;
    })
    .join("");
  const more = counts.length > shown.length ? `<p class="muted">… and ${counts.length - shown.length} more</p>` : "";
  return `<div class="bars">${rows}</div>${more}`;
}

export function elementBars(counts: ElementCount[], limit = 10): string {
  const ids = new Map<string, string>();
  const cs: Count[] = counts.map((c) => {
    const label = c.kind ? `${c.kind}: ${c.name}` : c.name;
    ids.set(label, c.id);
    return { key: label, n: c.n };
  });
  return bars(cs, { nav: "element", ids, limit, cls: () => "kind-bar" });
}

function panel(title: string, body: string): string {
  return `<section class="panel"><h3>${esc(title)}</h3>${body}</section>`;
}

function technical(rows: string[]): string {
  return `<details class="technical"><summary>Technical details</summary>${rows.join("")}</details>`;
}

function idRow(label: string, id: string): string {
  return `<div class="kv"><span class="k">${esc(label)}</span><span class="v"><code>${esc(id)}</code> <button class="copy" data-copy="${esc(id)}" title="Copy">copy</button></span></div>`;
}

function kv(label: string, value: string): string {
  return `<div class="kv"><span class="k">${esc(label)}</span><span class="v">${esc(value)}</span></div>`;
}

function who(actor: string, author: string): string {
  let s = `<span class="dot" data-person="${esc(actor)}"></span> by ${nav("person", actor, actor)}`;
  if (author !== actor) {
    s += `, credited to ${esc(author)}`;
  }
  return s;
}

// ---- pages ------------------------------------------------------------------------------

export function decisionPage(d: Decision): string {
  const refs = d.refs
    .map((r) => (r.url ? `<div class="row">${chip(r.system, "neutral")} <a href="${esc(r.url)}">${esc(r.url)}</a></div>` : `<div class="row"><code>${esc(r.text)}</code></div>`))
    .join("");
  const roles: Record<string, string> = { governs: "governs", superseded: "superseded", mentioned: "mentioned" };
  const elements = d.elements
    .map((e) => `<div class="row">${kindChip(e.kind)} ${nav("element", e.id, e.name)} <span class="chip role-${e.role}">${esc(roles[e.role])}</span></div>`)
    .join("");
  return `
<h1>${esc(d.title)}</h1>
<p class="meta">Recorded ${esc(d.time)} ${who(d.actor, d.author)} · ${stateChip(d.state)} ${esc(d.stateSentence)}</p>
${d.rationale ? section("Rationale") + `<div class="box">${esc(d.rationale)}</div>` : ""}
${d.summary ? section("Summary") + `<p>${esc(d.summary)}</p>` : ""}
${d.refs.length ? section("References") + refs : ""}
${section("What it changed")}
${mutationRows(d.mutations)}
${section("Elements it shaped")}
${elements || `<p class="muted">None.</p>`}
${d.supersedes.length ? section("Replaces") + d.supersedes.map(decisionRefRow).join("") : ""}
${d.supersededBy.length ? section("Replaced by") + d.supersededBy.map(decisionRefRow).join("") : ""}
${technical([idRow("Decision id", d.id), idRow("Event hash", d.eventHash), kv("Install", d.installId), kv("Sequence", `#${d.lamport} in that install's log`)])}`;
}

export function elementPage(e: Element): string {
  const props = e.props.length ? e.props.map((p) => kv(p.key, p.value)).join("") : `<p class="muted">None recorded.</p>`;
  const links = e.links.length
    ? e.links
        .map(
          (l) =>
            `<div class="row">${chip(l.kind, "link")} <code>${l.direction === "out" ? "→" : "←"}</code> ${kindChip(l.other.kind)} ${nav("element", l.other.id, l.other.name)}</div>`,
        )
        .join("")
    : `<p class="muted">None.</p>`;
  const history = e.history
    .map((h) => `<div class="row"><code class="day">${esc(h.day)}</code> <span class="dot" data-person="${esc(h.actor)}"></span> ${nav("decision", h.id, h.title)} ${stateChip(h.state)}</div>`)
    .join("");
  return `
<h1>${kindChip(e.kind)} ${esc(e.name)}</h1>
<p class="meta">${esc(e.standing)} · shaped by ${e.shapedBy} decision${e.shapedBy === 1 ? "" : "s"} · ${nav("decisions", e.id, "Show its decisions")}</p>
${section("Properties")}
${props}
${section("Links")}
${links}
${section("History — decisions that shaped it, oldest first")}
${history || `<p class="muted">None.</p>`}
${technical([idRow("Element id", e.id)])}`;
}

export function conflictPage(c: Conflict): string {
  const heads = c.heads
    .map(
      (h, i) => `
<section class="panel head">
  <p class="meta">${chip(`decision ${i + 1} of ${c.heads.length}`, "conflict")} <span class="dot" data-person="${esc(h.actor)}"></span> Recorded ${esc(h.time)} by ${nav("person", h.actor, h.actor)}</p>
  <h3>${nav("decision", h.id, h.title)}</h3>
  ${h.rationale ? `<div class="box">${esc(h.rationale)}</div>` : ""}
  ${mutationRows(h.mutations)}
</section>`,
    )
    .join("");
  return `
<h1>${kindChip(c.kind)} ${nav("element", c.elementId, c.name)} — ${c.heads.length} competing decisions</h1>
<p class="meta">Each of these still governs the element; none of them replaced the others. To resolve it, record a new decision on this element (<code>/kgai:kg-decision</code>) — this view only reads.</p>
${heads}`;
}

export function personPage(p: Person, by: "actor" | "author"): string {
  const recent = p.recent
    .map(
      (d) =>
        `<div class="row two"><code class="day">${esc(d.day)}</code> <span>${nav("decision", d.id, d.title)}<br><span class="muted small">${esc(d.elements.slice(0, 3).join(", "))}${d.elements.length > 3 ? ", …" : ""}</span></span></div>`,
    )
    .join("");
  const other = by === "author" ? "Installs that recorded for this name" : "Names they were credited as";
  const filter = by === "author" ? `author=${encodeURIComponent(p.name)}` : `actor=${encodeURIComponent(p.name)}`;
  return `
<h1><span class="dot big" data-person="${esc(p.name)}"></span> ${esc(p.name)}</h1>
<p class="meta">
  ${chip(`${p.decisions} decision${p.decisions === 1 ? "" : "s"} · ${Math.round(p.share)}% of the log`, "primary")}
  ${chip(`${p.heads} heads`, "state-head")} ${chip(`${p.superseded} superseded`, "state-superseded")} ${chip(`${p.notes} notes`, "state-note")}
  ${chip(`${p.elements} elements shaped`, "kind")}
  ${p.conflicts ? chip(`involved in ${p.conflicts} conflict${p.conflicts === 1 ? "" : "s"}`, "conflict") : ""}
</p>
<p class="meta">First decision ${esc(p.first)}, latest ${esc(p.last)} · active on ${p.activeDays} days · ${p.last30} in the last 30 days, ${p.last90} in the last 90 · ${nav("decisions", filter, "Show their decisions")}</p>
<div class="grid">
  ${panel("Decisions per month" + (p.byMonth.length > 12 ? ` (last 12 of ${p.byMonth.length})` : ""), bars(p.byMonth.slice(-12)))}
  ${panel("Whose decisions they replaced", bars(p.overrode, { nav: "person", cls: () => "person-bar" }))}
  ${panel("Elements they shaped most", elementBars(p.mostShaped))}
  ${panel("Who replaced theirs", bars(p.overriddenBy, { nav: "person", cls: () => "person-bar" }))}
  ${panel("Kinds of elements", bars(p.kinds, { cls: () => "kind-bar" }))}
  ${panel("Installs they recorded from", bars(p.installs))}
  ${panel("Kinds of change", bars(p.changes, { cls: (k) => "op-" + k.replace(/\s+/g, "-") }))}
  ${panel(other, bars(p.creditedAs, { cls: () => "person-bar" }))}
</div>
${section(`Latest decisions (${p.recent.length})`)}
${recent || `<p class="muted">None.</p>`}`;
}

export function overviewPage(o: Overview): string {
  const s = o.store;
  const tiles = [
    ["decisions", s.counts.decisions, "primary"],
    ["elements", s.counts.elements, "kind"],
    ["links", s.counts.links, "link"],
    ["conflicts", s.counts.conflicts, s.counts.conflicts ? "conflict" : "neutral"],
    ["people", s.counts.people, "person"],
    ["installs", s.counts.installs, "install"],
  ]
    .map(([label, n, cls]) => `<div class="tile ${cls}"><div class="n">${n}</div><div class="label">${label}</div></div>`)
    .join("");
  const installs = o.installs.length
    ? o.installs
        .map(
          (i) =>
            `<div class="kv"><span class="k"><code>${esc(i.id)}</code></span><span class="v muted">${i.decisions} decision${i.decisions === 1 ? "" : "s"} · latest ${esc(i.last)} · ${esc(i.actors.map((a) => `${a.key} (${a.n})`).join(", "))}</span></div>`,
        )
        .join("")
    : `<p class="muted">nothing yet</p>`;
  const where = [
    kv("Store folder", s.root),
    kv("Chosen by", s.rule + " — the rules are in Help"),
    kv("This install", `${s.installId ?? "?"}, recording as ${s.actor ?? "?"}`),
    kv("Sync", s.remote ? `${s.sync}  ${s.remote}` : "not configured — decisions stay on this machine"),
    kv("Decisions", `${o.first} to ${o.last}`),
    kv("Log read", s.loaded.replace("T", " ").replace("Z", " UTC") + " — the view follows the log"),
  ].join("");
  return `
<h1>${esc(s.project.split(/[\\/]/).pop() ?? s.project)}</h1>
<div class="tiles">${tiles}</div>
<div class="grid">
  ${panel("Where this store lives", where)}
  ${panel("Installs", installs)}
  ${panel("Elements by kind", bars(o.elementsByKind, { cls: () => "kind-bar" }))}
  ${panel("People — decisions recorded", bars(o.decisionsByActor, { nav: "person", cls: () => "person-bar", limit: 8 }))}
  ${panel("Decisions per month" + (o.decisionsByMonth.length > 12 ? ` (last 12 of ${o.decisionsByMonth.length})` : ""), bars(o.decisionsByMonth.slice(-12)))}
  ${panel("Most-shaped elements", elementBars(o.mostShaped))}
  ${panel("Links by kind", bars(o.linksByKind, { cls: () => "link-bar", limit: 12 }))}
</div>
${technical([
  kv("Log events", `${o.events} (one per decision; more only when a decision was re-emitted after an install rotation)`),
  kv("Property changes", `${o.setProps} set_prop operations`),
  kv("Links removed", `${o.retiredLinks}`),
  kv("Notes", `${o.notes} decisions that govern nothing (provenance only)`),
  ...(o.stubs ? [kv("Missing decisions", `${o.stubs} referenced by others but not in this log (a partial sync)`)] : []),
])}`;
}

export function emptyPage(s: Store): string {
  return `
<h1>No knowledge graph here</h1>
<p>${esc(s.hint ?? s.error ?? "")}</p>
<p class="muted">kgai keeps a project's decisions in <code>.kgai/store</code> (or where <code>KGAI_STORE</code>, an approved <code>.kgairc</code> or <code>~/.kgai/config.json</code> point). Record the first one with the kgai plugin for Claude Code, or open another project folder.</p>`;
}

export function helpPage(): string {
  const word = (term: string, text: string) => `<div class="kv"><span class="k">${term}</span><span class="v">${text}</span></div>`;
  const key = (k: string, text: string) => `<div class="kv"><span class="k">${chip(k, "neutral")}</span><span class="v">${esc(text)}</span></div>`;
  return `
<h1>kgai in this editor</h1>
<p>This view shows one kgai store: the log of decisions about a project's elements. It only reads. Recording, syncing and resolving conflicts happen through <code>kg</code> and the Claude Code plugin.</p>
<p>The sidebar lists <b>Decisions</b> (newest first; search and filters on the view's toolbar), <b>Elements</b> grouped by kind, <b>Conflicts</b>, and <b>People</b>. Selecting anything opens it here; blue text is a link; <b>Back</b> returns from every jump.</p>
${section("Words")}
${word("Decision", "An immutable record: a title, a rationale, and the changes it made to the element graph. It is never edited; a later decision supersedes it.")}
${word("Element", "A named thing of a kind — feature, component, service, concept, … Decisions shape elements.")}
${word("Link", "A relation between two elements (PART_OF, DEPENDS_ON, …), added or removed by decisions.")}
${word(stateChip("head"), "A decision that still governs an element: nothing later replaced it there.")}
${word(stateChip("superseded"), "A decision that governed an element once and has been replaced by a later one.")}
${word(stateChip("note"), "A decision that governs nothing: it records context or a rejected idea (provenance only).")}
${word(chip("conflict", "conflict"), "An element with two or more heads: two people changed it concurrently and neither decision replaced the other. Recording a new decision on that element resolves it.")}
${word("Recorded by", "The identity of the install that wrote the decision — the actor in kg.config.json, one per person per machine.")}
${word("Credited to", "The decision's author field, when it names someone else: an import, a pair, an assistant recording for a person.")}
${word("Install", "One copy of kg on one machine, with its own shard of the log. A person with two machines has two installs.")}
${word("Sequence", "The position of a decision in its install's log (the Lamport number). It breaks ties; it is not a global order.")}
${word("Governs", "A decision governs an element when it took authority over it: set a property, defined it, or linked from it. A bare mention (a link's target, a re-stated element) is provenance only.")}
${section("Which store is shown")}
<p>The one <code>kg</code> would use in the workspace folder (or the folder in the <code>kgai.projectFolder</code> setting). The rules, in order:</p>
${word("1. KGAI_STORE", "an explicit store folder in the environment")}
${word("2. .kgairc", "the project's committed configuration, if it has been approved on this machine (<code>kg trust</code>). A pending one decides nothing and is reported.")}
${word("3. config.json", "~/.kgai/config.json, the machine's own configuration")}
${word("4. default", "&lt;project&gt;/.kgai/store, the project being the git worktree root")}
${section("Commands")}
${key("kgai: Search Decisions", "type to search title and rationale; pick one to open it")}
${key("kgai: Filter Decisions", "recorded by, credited to, state, element kind, element, install, dates, order, regular expression")}
${key("kgai: Clear Filters", "back to every decision, newest first")}
${key("kgai: Overview", "counts, where the store lives, charts")}
${key("kgai: Refresh", "re-read the log now (it is followed every two seconds anyway)")}
${key("kgai: Open Project Folder…", "show another project's store, resolved there as kg would")}
${section("What this view promises")}
<p>It only reads the log files. It never opens the graph database (which allows one writer or many readers, so an open reader could block <code>kg</code>), never takes the store's lock, never syncs, never calls the network, and ranks nothing: every list has one fixed order and every filter is an exact rule. The view follows the log: a decision recorded by <code>kg</code>, a sync, or a rebuild shows up within two seconds.</p>`;
}

/** The whole document around a page. */
export function document(body: string, title: string, nonce: string, cspSource: string, canGoBack: boolean): string {
  return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${cspSource} 'nonce-${nonce}'; script-src 'nonce-${nonce}';">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>${esc(title)}</title>
<style nonce="${nonce}">${css}</style>
</head>
<body>
<div class="toolbar"><button id="back" ${canGoBack ? "" : "disabled"} title="Back (Alt+Left)">← Back</button><span class="grow"></span><button id="help" title="What the words mean">Help</button></div>
<main>${body}</main>
<script nonce="${nonce}">${script}</script>
</body>
</html>`;
}

const css = `
:root { --pad: 12px; }
body { font-family: var(--vscode-font-family); font-size: var(--vscode-font-size); color: var(--vscode-foreground); background: var(--vscode-editor-background); padding: 0 var(--pad) var(--pad); line-height: 1.45; }
main { max-width: 1100px; }
.toolbar { display: flex; gap: 8px; align-items: center; padding: 8px 0; position: sticky; top: 0; background: var(--vscode-editor-background); border-bottom: 1px solid var(--vscode-panel-border, rgba(128,128,128,.3)); }
.toolbar .grow { flex: 1; }
button { font: inherit; color: var(--vscode-button-secondaryForeground); background: var(--vscode-button-secondaryBackground); border: none; border-radius: 3px; padding: 3px 10px; cursor: pointer; }
button:hover { background: var(--vscode-button-secondaryHoverBackground); }
button:disabled { opacity: .5; cursor: default; }
button.copy { padding: 0 6px; font-size: 90%; margin-left: 6px; }
h1 { font-size: 1.35em; font-weight: 600; margin: 12px 0 4px; line-height: 1.3; }
h2 { font-size: 1em; font-weight: 600; margin: 18px 0 6px; padding-left: 8px; border-left: 3px solid var(--vscode-textLink-foreground); }
h3 { font-size: 1em; font-weight: 600; margin: 0 0 6px; }
a { color: var(--vscode-textLink-foreground); text-decoration: none; }
a:hover { text-decoration: underline; }
code { font-family: var(--vscode-editor-font-family); font-size: 92%; }
code.day { opacity: .8; margin-right: 4px; }
pre { font-family: var(--vscode-editor-font-family); font-size: 92%; margin: 0; white-space: pre-wrap; }
p { margin: 4px 0; }
.meta, .muted { color: var(--vscode-descriptionForeground); }
.small { font-size: 90%; }
.box { background: var(--vscode-textBlockQuote-background); border-left: 3px solid var(--vscode-textBlockQuote-border); border-radius: 4px; padding: 8px 12px; white-space: pre-wrap; }
.row { display: flex; gap: 8px; align-items: baseline; padding: 3px 0; flex-wrap: wrap; }
.row.two { align-items: flex-start; }
.mutation { display: flex; gap: 10px; align-items: flex-start; padding: 4px 0; }
.kv { display: flex; gap: 12px; padding: 3px 0; }
.kv .k { flex: 0 0 150px; color: var(--vscode-descriptionForeground); }
.kv .v { flex: 1; min-width: 0; word-break: break-word; }
.chip { display: inline-block; font-size: 85%; font-weight: 600; padding: 1px 8px; border-radius: 9px; background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); white-space: nowrap; }
.chip.state-head, .chip.role-governs { background: color-mix(in srgb, var(--vscode-charts-green) 25%, transparent); color: var(--vscode-charts-green); }
.chip.state-superseded, .chip.role-superseded { background: color-mix(in srgb, var(--vscode-descriptionForeground) 22%, transparent); color: var(--vscode-foreground); }
.chip.state-note, .chip.role-mentioned { background: color-mix(in srgb, var(--vscode-charts-blue) 25%, transparent); color: var(--vscode-charts-blue); }
.chip.conflict { background: color-mix(in srgb, var(--vscode-charts-red) 25%, transparent); color: var(--vscode-charts-red); }
.chip.kind { background: color-mix(in srgb, var(--vscode-charts-purple) 25%, transparent); color: var(--vscode-charts-purple); }
.chip.link { background: color-mix(in srgb, var(--vscode-charts-yellow) 30%, transparent); color: var(--vscode-foreground); }
.chip.primary { background: color-mix(in srgb, var(--vscode-textLink-foreground) 25%, transparent); color: var(--vscode-textLink-foreground); }
.chip.op-upsert_element { background: color-mix(in srgb, var(--vscode-charts-blue) 25%, transparent); color: var(--vscode-charts-blue); }
.chip.op-set_prop { background: color-mix(in srgb, var(--vscode-charts-purple) 25%, transparent); color: var(--vscode-charts-purple); }
.chip.op-add_link { background: color-mix(in srgb, var(--vscode-charts-green) 25%, transparent); color: var(--vscode-charts-green); }
.chip.op-retire_link { background: color-mix(in srgb, var(--vscode-charts-orange) 25%, transparent); color: var(--vscode-charts-orange); }
.dot { display: inline-block; width: 9px; height: 9px; border-radius: 50%; background: var(--vscode-charts-blue); vertical-align: middle; }
.dot.big { width: 12px; height: 12px; }
.tiles { display: flex; flex-wrap: wrap; gap: 10px; margin: 10px 0; }
.tile { flex: 0 0 150px; padding: 8px 10px 8px 12px; border-radius: 6px; background: var(--vscode-sideBar-background, var(--vscode-editorWidget-background)); border-left: 4px solid var(--vscode-charts-blue); }
.tile .n { font-size: 1.6em; font-weight: 700; line-height: 1.2; }
.tile .label { color: var(--vscode-descriptionForeground); }
.tile.kind { border-color: var(--vscode-charts-purple); } .tile.link { border-color: var(--vscode-charts-yellow); } .tile.conflict { border-color: var(--vscode-charts-red); } .tile.neutral { border-color: var(--vscode-descriptionForeground); } .tile.person { border-color: var(--vscode-charts-red); } .tile.install { border-color: var(--vscode-charts-orange); }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(340px, 1fr)); gap: 10px; margin: 10px 0; }
.panel { border: 1px solid var(--vscode-panel-border, rgba(128,128,128,.3)); border-radius: 6px; padding: 8px 12px; background: var(--vscode-sideBar-background, transparent); }
.panel.head { margin: 8px 0; }
.bars { display: flex; flex-direction: column; gap: 3px; }
.bar-row { display: grid; grid-template-columns: minmax(80px, 36%) 1fr 44px; gap: 8px; align-items: center; }
.bar-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bar-track { height: 12px; }
.bar { height: 12px; border-radius: 3px; background: var(--vscode-charts-blue); min-width: 2px; }
.bar.kind-bar { background: var(--vscode-charts-purple); } .bar.person-bar { background: var(--vscode-charts-red); } .bar.link-bar { background: var(--vscode-charts-yellow); }
.bar.op-defined-element { background: var(--vscode-charts-blue); } .bar.op-set-property { background: var(--vscode-charts-purple); } .bar.op-added-link { background: var(--vscode-charts-green); } .bar.op-removed-link { background: var(--vscode-charts-orange); }
.bar-n { text-align: right; font-variant-numeric: tabular-nums; }
details.technical { margin-top: 16px; }
details.technical summary { cursor: pointer; color: var(--vscode-descriptionForeground); }
`;

const script = `
const vscode = acquireVsCodeApi();
const palette = ['charts-blue','charts-red','charts-green','charts-orange','charts-purple','charts-yellow'];
function hash(s){ let h = 0; for (const c of s) { h = (h * 31 + c.charCodeAt(0)) >>> 0; } return h; }
document.querySelectorAll('.dot[data-person]').forEach(el => { el.style.background = 'var(--vscode-' + palette[hash('person:' + el.dataset.person) % palette.length] + ')'; });
document.querySelectorAll('.chip.kind').forEach(el => { const c = palette[hash('kind:' + el.textContent) % palette.length]; el.style.color = 'var(--vscode-' + c + ')'; el.style.background = 'color-mix(in srgb, var(--vscode-' + c + ') 25%, transparent)'; });
document.addEventListener('click', e => {
  const a = e.target.closest('a.nav');
  if (a) { e.preventDefault(); vscode.postMessage({ type: 'open', kind: a.dataset.kind, id: a.dataset.id }); return; }
  const c = e.target.closest('button.copy');
  if (c) { vscode.postMessage({ type: 'copy', text: c.dataset.copy }); c.textContent = 'copied'; setTimeout(() => c.textContent = 'copy', 1200); return; }
  if (e.target.closest('#back')) { vscode.postMessage({ type: 'back' }); return; }
  if (e.target.closest('#help')) { vscode.postMessage({ type: 'help' }); return; }
});
document.addEventListener('keydown', e => { if (e.altKey && e.key === 'ArrowLeft') { vscode.postMessage({ type: 'back' }); } });
`;
