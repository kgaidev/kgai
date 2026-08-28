// The graph page, run inside the webview: every element as a node coloured by its kind
// and sized by the decisions that shaped it, every live link as an edge, and — on
// request — the decisions themselves hanging off the elements they shaped. d3-force
// lays it out, a canvas draws it. Hovering shows a node's neighbours, clicking opens
// it in the detail panel. Positions survive a refresh, so a new decision moves little.

import {
  forceCollide,
  forceLink,
  forceManyBody,
  forceSimulation,
  forceX,
  forceY,
  type ForceLink,
  type Simulation,
  type SimulationLinkDatum,
  type SimulationNodeDatum,
} from "d3-force";
import type { Graph, GraphDecision, GraphNode, State } from "../reader";

declare function acquireVsCodeApi(): { postMessage(msg: unknown): void };
const vscode = acquireVsCodeApi();

interface Node extends SimulationNodeDatum {
  id: string;
  label: string;
  kind: string; // an element kind, or "decision"
  r: number;
  element?: GraphNode;
  decision?: GraphDecision;
}

interface Edge extends SimulationLinkDatum<Node> {
  source: Node;
  target: Node;
  kind: string; // a link kind, or shapes | governs | supersedes
}

// ---- colours: the editor's own, read from the theme variables ---------------------------

const palette = ["charts-blue", "charts-red", "charts-green", "charts-orange", "charts-purple", "charts-yellow"];

function hash(s: string): number {
  let h = 0;
  for (const c of s) {
    h = (h * 31 + c.charCodeAt(0)) >>> 0;
  }
  return h;
}

function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

function readColors() {
  const kinds = new Map<string, string>();
  return {
    fg: cssVar("--vscode-foreground", "#cccccc"),
    bg: cssVar("--vscode-editor-background", "#1e1e1e"),
    muted: cssVar("--vscode-descriptionForeground", "#999999"),
    red: cssVar("--vscode-charts-red", "#f14c4c"),
    green: cssVar("--vscode-charts-green", "#89d185"),
    blue: cssVar("--vscode-charts-blue", "#3794ff"),
    grey: cssVar("--vscode-disabledForeground", "#888888"),
    focus: cssVar("--vscode-focusBorder", "#007fd4"),
    font: cssVar("--vscode-font-family", "sans-serif"),
    fontPx: parseFloat(cssVar("--vscode-font-size", "13px")) || 13,
    /** The same colour the detail panel gives the kind's chip. */
    kind(kind: string): string {
      let c = kinds.get(kind);
      if (!c) {
        c = cssVar("--vscode-" + palette[hash("kind:" + kind) % palette.length], "#3794ff");
        kinds.set(kind, c);
      }
      return c;
    },
  };
}

let colors = readColors();

function stateColor(state: State): string {
  return state === "head" ? colors.green : state === "note" ? colors.blue : colors.grey;
}

// ---- state ------------------------------------------------------------------------------

const canvas = document.getElementById("c") as HTMLCanvasElement;
const ctx = canvas.getContext("2d")!;
const tip = document.getElementById("tip")!;
const q = document.getElementById("q") as HTMLInputElement;
const matchesBox = document.getElementById("matches")!;
const kindsBox = document.getElementById("kinds")!;
const decisionsToggle = document.getElementById("decisions") as HTMLInputElement;
const decisionsCount = document.getElementById("decisions-n")!;
const countsBox = document.getElementById("counts")!;
const projectBox = document.getElementById("project")!;
const empty = document.getElementById("empty")!;
const card = document.querySelector(".card") as HTMLElement;

let data: Graph = { nodes: [], links: [], decisions: [], kinds: [] };
let nodes: Node[] = [];
let edges: Edge[] = [];
let neighbours = new Map<string, Set<string>>();
const positions = new Map<string, { x: number; y: number }>();
const hidden = new Set<string>();
let showDecisions = false;
let selected: Node | undefined;
let hover: Node | undefined;
let first = true;
let width = 0;
let height = 0;
let dpr = 1;
/** The view transform: screen = world * k + (x, y). */
const t = { k: 1, x: 0, y: 0 };

const sim: Simulation<Node, Edge> = forceSimulation<Node, Edge>()
  .force(
    "link",
    forceLink<Node, Edge>()
      .id((d) => d.id)
      .distance((e) => (e.kind === "shapes" || e.kind === "governs" ? 24 : e.kind === "supersedes" ? 32 : 70))
      .strength((e) => (e.kind === "shapes" ? 0.3 : e.kind === "governs" ? 0.7 : e.kind === "supersedes" ? 0.4 : 0.5)),
  )
  .force(
    "charge",
    forceManyBody<Node>()
      .strength((n) => (n.kind === "decision" ? -25 : -140))
      .distanceMax(500),
  )
  .force(
    "collide",
    forceCollide<Node>((n) => n.r + 3).strength(0.8),
  )
  .force("x", forceX<Node>(0).strength(0.07))
  .force("y", forceY<Node>(0).strength(0.07))
  .on("tick", render)
  .stop();

// ---- building the simulation from the reader's graph -----------------------------------

function radius(e: GraphNode): number {
  return 4 + Math.sqrt(e.decisions) * 2.2;
}

/** Rebuilds nodes and edges from the data and the filters; known nodes keep their place. */
function build(): void {
  for (const n of nodes) {
    if (n.x !== undefined && n.y !== undefined) {
      positions.set(n.id, { x: n.x, y: n.y });
    }
  }
  const byId = new Map<string, Node>();
  nodes = [];
  edges = [];
  for (const e of data.nodes) {
    if (hidden.has(e.kind)) {
      continue;
    }
    const n: Node = { id: e.id, label: e.name, kind: e.kind, r: radius(e), element: e, ...positions.get(e.id) };
    nodes.push(n);
    byId.set(n.id, n);
  }
  for (const l of data.links) {
    const s = byId.get(l.from);
    const d = byId.get(l.to);
    if (s && d) {
      edges.push({ source: s, target: d, kind: l.kind });
    }
  }
  if (showDecisions) {
    for (const d of data.decisions) {
      const shaped = d.elements.filter((id) => byId.has(id));
      if (shaped.length === 0) {
        continue;
      }
      const n: Node = { id: d.id, label: d.title, kind: "decision", r: 3, decision: d, ...positions.get(d.id) };
      nodes.push(n);
      byId.set(n.id, n);
      for (const id of shaped) {
        edges.push({ source: n, target: byId.get(id)!, kind: d.governs.includes(id) ? "governs" : "shapes" });
      }
    }
    for (const d of data.decisions) {
      for (const older of d.supersedes) {
        const a = byId.get(d.id);
        const b = byId.get(older);
        if (a && b) {
          edges.push({ source: a, target: b, kind: "supersedes" });
        }
      }
    }
  }
  // A node seen for the first time starts next to a neighbour that has a place, so it
  // does not fly in from the centre.
  for (const e of edges) {
    const place = (n: Node, near: Node) => {
      if (n.x === undefined && near.x !== undefined) {
        n.x = near.x + (Math.random() - 0.5) * 30;
        n.y = near.y! + (Math.random() - 0.5) * 30;
      }
    };
    place(e.source, e.target);
    place(e.target, e.source);
  }
  neighbours = new Map();
  for (const e of edges) {
    if (!neighbours.has(e.source.id)) {
      neighbours.set(e.source.id, new Set());
    }
    if (!neighbours.has(e.target.id)) {
      neighbours.set(e.target.id, new Set());
    }
    neighbours.get(e.source.id)!.add(e.target.id);
    neighbours.get(e.target.id)!.add(e.source.id);
  }
  if (selected && !byId.has(selected.id)) {
    selected = undefined;
  } else if (selected) {
    selected = byId.get(selected.id);
  }
  hover = undefined;
  sim.nodes(nodes);
  (sim.force("link") as ForceLink<Node, Edge>).links(edges);
  if (first && nodes.length > 0) {
    // Settle before the first frame: the reader sees a finished picture, not a bloom.
    const ticks = nodes.length > 1500 ? 80 : 300;
    sim.alpha(1);
    for (let i = 0; i < ticks; i++) {
      sim.tick();
    }
    fit();
    sim.alpha(0.15).restart();
    first = false;
  } else {
    sim.alpha(0.5).restart();
  }
  paintCounts();
  render();
}

// ---- drawing ------------------------------------------------------------------------------

function focusSet(): Set<string> | undefined {
  const f = hover ?? selected;
  if (!f) {
    return undefined;
  }
  return new Set([f.id, ...(neighbours.get(f.id) ?? [])]);
}

function isElementLink(kind: string): boolean {
  return kind !== "shapes" && kind !== "governs" && kind !== "supersedes";
}

function render(): void {
  if (width === 0 || height === 0) {
    return;
  }
  ctx.save();
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, width, height);
  ctx.translate(t.x, t.y);
  ctx.scale(t.k, t.k);
  const focus = focusSet();
  const px = 1 / t.k; // one screen pixel, in world units
  ctx.lineCap = "round";

  for (const e of edges) {
    const dim = focus !== undefined && !(focus.has(e.source.id) && focus.has(e.target.id));
    const link = isElementLink(e.kind);
    ctx.globalAlpha = dim ? 0.05 : e.kind === "shapes" ? 0.22 : link ? 0.5 : 0.4;
    ctx.strokeStyle = e.kind === "supersedes" ? colors.grey : link ? colors.fg : colors.muted;
    ctx.lineWidth = (e.kind === "shapes" ? 0.7 : link ? 1.2 : 1) * px * Math.sqrt(t.k);
    ctx.setLineDash(e.kind === "supersedes" ? [4 * px, 4 * px] : []);
    ctx.beginPath();
    ctx.moveTo(e.source.x!, e.source.y!);
    ctx.lineTo(e.target.x!, e.target.y!);
    ctx.stroke();
    if (link && !dim) {
      // An arrowhead at the target: links are directed (from PART_OF to).
      const dx = e.target.x! - e.source.x!;
      const dy = e.target.y! - e.source.y!;
      const len = Math.hypot(dx, dy) || 1;
      const ux = dx / len;
      const uy = dy / len;
      const tipX = e.target.x! - ux * (e.target.r + 2);
      const tipY = e.target.y! - uy * (e.target.r + 2);
      const s = 5 * px * Math.sqrt(t.k);
      ctx.fillStyle = colors.fg;
      ctx.beginPath();
      ctx.moveTo(tipX, tipY);
      ctx.lineTo(tipX - ux * s * 2 - uy * s, tipY - uy * s * 2 + ux * s);
      ctx.lineTo(tipX - ux * s * 2 + uy * s, tipY - uy * s * 2 - ux * s);
      ctx.closePath();
      ctx.fill();
    }
  }
  ctx.setLineDash([]);

  for (const n of nodes) {
    const dim = focus !== undefined && !focus.has(n.id);
    ctx.globalAlpha = dim ? 0.15 : 1;
    ctx.beginPath();
    ctx.arc(n.x!, n.y!, n.r, 0, Math.PI * 2);
    ctx.fillStyle = n.decision ? stateColor(n.decision.state) : colors.kind(n.kind);
    ctx.fill();
    if (n.element && n.element.heads > 1) {
      ctx.lineWidth = 2 * px;
      ctx.strokeStyle = colors.red;
      ctx.beginPath();
      ctx.arc(n.x!, n.y!, n.r + 3 * px, 0, Math.PI * 2);
      ctx.stroke();
    }
    if (n === selected || n === hover) {
      ctx.lineWidth = 2 * px;
      ctx.strokeStyle = colors.focus;
      ctx.beginPath();
      ctx.arc(n.x!, n.y!, n.r + 6 * px, 0, Math.PI * 2);
      ctx.stroke();
    }
  }

  // Labels keep their screen size; how many show depends on zoom and on the crowd. The
  // biggest nodes label first, and a label that would sit on another is skipped, so the
  // crowd stays readable; the hovered node's own label always shows.
  const fontPx = colors.fontPx * px;
  ctx.font = `${fontPx}px ${colors.font}`;
  ctx.textBaseline = "middle";
  ctx.lineWidth = 3 * px;
  ctx.strokeStyle = colors.bg;
  ctx.fillStyle = colors.fg;
  // The crowd that matters for labels is the elements; decision nodes carry none by default.
  const elements = nodes.reduce((n, x) => n + (x.decision ? 0 : 1), 0);
  const threshold = elements <= 80 ? 0 : 4 + 6 / t.k;
  const candidates: { n: Node; label: string; inFocus: boolean }[] = [];
  for (const n of nodes) {
    const inFocus = focus !== undefined && focus.has(n.id);
    let label = n.label;
    if (n.decision) {
      if (!inFocus && t.k < 2.5) {
        continue;
      }
      label = label.length > 48 ? label.slice(0, 47) + "…" : label;
    } else if (!inFocus && n.r < threshold && n !== selected) {
      continue;
    }
    candidates.push({ n, label, inFocus });
  }
  candidates.sort((a, b) => Number(b.n === hover) - Number(a.n === hover) || b.n.r - a.n.r);
  const taken: { x0: number; y0: number; x1: number; y1: number }[] = [];
  const h = fontPx * 1.25;
  for (const { n, label, inFocus } of candidates) {
    const x = n.x! + n.r + 4 * px;
    const box = { x0: x, y0: n.y! - h / 2, x1: x + ctx.measureText(label).width, y1: n.y! + h / 2 };
    if (n !== hover && taken.some((b) => b.x0 < box.x1 && box.x0 < b.x1 && b.y0 < box.y1 && box.y0 < b.y1)) {
      continue;
    }
    taken.push(box);
    ctx.globalAlpha = focus !== undefined && !inFocus ? 0.15 : n.decision ? 0.8 : 1;
    ctx.strokeText(label, x, n.y!);
    ctx.fillText(label, x, n.y!);
  }
  ctx.restore();
}

// ---- the view: fit, zoom, pan ----------------------------------------------------------

function fit(): void {
  if (nodes.length === 0 || width === 0) {
    return;
  }
  // The box around the crowd, not around every outlier: the 4th to 96th percentile of
  // the positions, so a few far-flung nodes do not shrink the rest to dust.
  const xs = nodes.map((n) => n.x!).sort((a, b) => a - b);
  const ys = nodes.map((n) => n.y!).sort((a, b) => a - b);
  const trim = nodes.length > 80 ? 0.04 : 0; // a small graph fits whole
  const lo = Math.floor(nodes.length * trim);
  const hi = Math.max(lo, Math.ceil(nodes.length * (1 - trim)) - 1);
  const x0 = xs[lo];
  const x1 = xs[hi];
  const y0 = ys[lo];
  const y1 = ys[hi];
  // The card sits over the left edge: fit into the room to its right.
  const left = Math.min(width * 0.5, card.getBoundingClientRect().right + 12);
  const pad = 60;
  const room = width - left;
  t.k = Math.min(2, (room - 2 * pad) / Math.max(1, x1 - x0), (height - 2 * pad) / Math.max(1, y1 - y0));
  t.x = left + room / 2 - ((x0 + x1) / 2) * t.k;
  t.y = height / 2 - ((y0 + y1) / 2) * t.k;
  render();
}

function centerOn(n: Node): void {
  t.k = Math.max(t.k, 1.3);
  t.x = width / 2 - n.x! * t.k;
  t.y = height / 2 - n.y! * t.k;
  render();
}

function toWorld(sx: number, sy: number): [number, number] {
  return [(sx - t.x) / t.k, (sy - t.y) / t.k];
}

function nodeAt(sx: number, sy: number): Node | undefined {
  const [wx, wy] = toWorld(sx, sy);
  const n = sim.find(wx, wy, 16 / t.k);
  if (n && Math.hypot(n.x! - wx, n.y! - wy) <= Math.max(n.r + 4 / t.k, 8 / t.k)) {
    return n;
  }
  return undefined;
}

function resize(): void {
  width = canvas.clientWidth;
  height = canvas.clientHeight;
  dpr = window.devicePixelRatio || 1;
  canvas.width = Math.round(width * dpr);
  canvas.height = Math.round(height * dpr);
  render();
}

let drag: { node?: Node; sx: number; sy: number; tx: number; ty: number; moved: boolean } | undefined;

canvas.addEventListener("pointerdown", (e) => {
  if (e.button !== 0) {
    return;
  }
  canvas.setPointerCapture(e.pointerId);
  const node = nodeAt(e.offsetX, e.offsetY);
  drag = { node, sx: e.offsetX, sy: e.offsetY, tx: t.x, ty: t.y, moved: false };
  if (node) {
    node.fx = node.x;
    node.fy = node.y;
    sim.alphaTarget(0.3).restart();
  }
  canvas.classList.add("grabbing");
});

canvas.addEventListener("pointermove", (e) => {
  if (drag) {
    if (Math.hypot(e.offsetX - drag.sx, e.offsetY - drag.sy) > 4) {
      drag.moved = true;
    }
    if (drag.node) {
      const [wx, wy] = toWorld(e.offsetX, e.offsetY);
      drag.node.fx = wx;
      drag.node.fy = wy;
    } else {
      t.x = drag.tx + (e.offsetX - drag.sx);
      t.y = drag.ty + (e.offsetY - drag.sy);
      render();
    }
    return;
  }
  const n = nodeAt(e.offsetX, e.offsetY);
  if (n !== hover) {
    hover = n;
    canvas.classList.toggle("point", !!n);
    render();
  }
  if (n) {
    tip.textContent = n.decision
      ? `${n.decision.day} · ${n.decision.actor} · ${n.decision.state}\n${n.decision.title}`
      : `${n.kind}: ${n.label}\n${n.element!.decisions} decision${n.element!.decisions === 1 ? "" : "s"}${n.element!.heads > 1 ? " · contested" : ""}`;
    tip.style.display = "block";
    tip.style.left = `${Math.min(e.offsetX + 14, width - tip.offsetWidth - 8)}px`;
    tip.style.top = `${Math.min(e.offsetY + 14, height - tip.offsetHeight - 8)}px`;
  } else {
    tip.style.display = "none";
  }
});

function endDrag(e: PointerEvent): void {
  if (!drag) {
    return;
  }
  canvas.classList.remove("grabbing");
  const d = drag;
  drag = undefined;
  if (d.node) {
    d.node.fx = null;
    d.node.fy = null;
    sim.alphaTarget(0);
    if (!d.moved) {
      selected = d.node;
      vscode.postMessage({ type: "open", kind: d.node.decision ? "decision" : "element", id: d.node.id });
      render();
    }
  } else if (!d.moved) {
    selected = undefined;
    render();
  }
  canvas.releasePointerCapture(e.pointerId);
}
canvas.addEventListener("pointerup", endDrag);
canvas.addEventListener("pointercancel", endDrag);
canvas.addEventListener("pointerleave", () => {
  if (!drag && hover) {
    hover = undefined;
    tip.style.display = "none";
    render();
  }
});

canvas.addEventListener(
  "wheel",
  (e) => {
    e.preventDefault();
    const k = Math.min(10, Math.max(0.05, t.k * Math.exp(-e.deltaY * 0.0015)));
    t.x = e.offsetX - ((e.offsetX - t.x) * k) / t.k;
    t.y = e.offsetY - ((e.offsetY - t.y) * k) / t.k;
    t.k = k;
    render();
  },
  { passive: false },
);

// ---- the HUD: search, kinds, layers ------------------------------------------------------

function paintCounts(): void {
  const els = nodes.filter((n) => n.element).length;
  const links = edges.filter((e) => isElementLink(e.kind)).length;
  countsBox.textContent = `${els} of ${data.nodes.length} elements · ${links} links · ${data.decisions.length} decisions`;
  decisionsCount.textContent = String(data.decisions.length);
  empty.style.display = data.nodes.length === 0 ? "flex" : "none";
}

function paintKinds(): void {
  kindsBox.replaceChildren();
  for (const k of data.kinds) {
    const row = document.createElement("div");
    row.className = "kind" + (hidden.has(k.key) ? " off" : "");
    row.title = hidden.has(k.key) ? `show ${k.key} elements` : `hide ${k.key} elements`;
    const dot = document.createElement("span");
    dot.className = "dot";
    const c = colors.kind(k.key);
    dot.style.background = hidden.has(k.key) ? "transparent" : c;
    dot.style.boxShadow = `inset 0 0 0 2px ${c}`;
    const name = document.createElement("span");
    name.textContent = k.key;
    const n = document.createElement("span");
    n.className = "n";
    n.textContent = String(k.n);
    row.append(dot, name, n);
    row.addEventListener("click", () => {
      if (hidden.has(k.key)) {
        hidden.delete(k.key);
      } else {
        hidden.add(k.key);
      }
      paintKinds();
      build();
    });
    kindsBox.append(row);
  }
}

let matches: Node[] = [];
let matchIndex = 0;

function paintMatches(): void {
  matchesBox.replaceChildren();
  const text = q.value.trim().toLowerCase();
  matches = text ? nodes.filter((n) => n.element && n.label.toLowerCase().includes(text)).sort((a, b) => b.r - a.r).slice(0, 8) : [];
  matchIndex = 0;
  matches.forEach((n, i) => {
    const row = document.createElement("div");
    row.className = "m" + (i === matchIndex ? " sel" : "");
    const dot = document.createElement("span");
    dot.className = "dot";
    dot.style.background = colors.kind(n.kind);
    const name = document.createElement("span");
    name.textContent = n.label;
    const kind = document.createElement("span");
    kind.className = "n";
    kind.textContent = n.kind;
    row.append(dot, name, kind);
    row.addEventListener("click", () => pick(n));
    matchesBox.append(row);
  });
  if (text && matches.length === 0) {
    const none = document.createElement("div");
    none.className = "none";
    none.textContent = "no element with that name";
    matchesBox.append(none);
  }
}

function pick(n: Node): void {
  selected = n;
  centerOn(n);
  q.value = n.label;
  matchesBox.replaceChildren();
}

q.addEventListener("input", paintMatches);
q.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && matches[matchIndex]) {
    pick(matches[matchIndex]);
  } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
    e.preventDefault();
    matchIndex = (matchIndex + (e.key === "ArrowDown" ? 1 : matches.length - 1)) % Math.max(1, matches.length);
    matchesBox.querySelectorAll(".m").forEach((el, i) => el.classList.toggle("sel", i === matchIndex));
  } else if (e.key === "Escape") {
    q.value = "";
    selected = undefined;
    matchesBox.replaceChildren();
    render();
  }
});

decisionsToggle.addEventListener("change", () => {
  showDecisions = decisionsToggle.checked;
  build();
});
document.getElementById("fit")!.addEventListener("click", fit);
document.getElementById("help")!.addEventListener("click", () => vscode.postMessage({ type: "help" }));

// ---- messages from the extension ----------------------------------------------------------

window.addEventListener("message", (e: MessageEvent<{ type: string; graph?: Graph; project?: string; note?: string }>) => {
  if (e.data.type !== "graph" || !e.data.graph) {
    return;
  }
  data = e.data.graph;
  projectBox.textContent = e.data.project ?? "";
  empty.textContent = e.data.note ?? "No elements yet — the first decision recorded with kg draws the first node.";
  paintKinds();
  build();
  paintMatches();
});

new ResizeObserver(() => {
  const wasEmpty = width === 0;
  resize();
  if (wasEmpty && nodes.length > 0) {
    fit();
  }
}).observe(canvas);

// The theme can change under the page: read the colours again.
const themeWatch = new MutationObserver(() => {
  colors = readColors();
  paintKinds();
  render();
});
themeWatch.observe(document.documentElement, { attributes: true, attributeFilter: ["style", "class"] });
themeWatch.observe(document.body, { attributes: true, attributeFilter: ["class"] });

resize();
vscode.postMessage({ type: "ready" });
