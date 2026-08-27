// kgai for VS Code (and the editors built on it): the decisions of the open project,
// read from the store exactly as kg would find it, through the kgread reader shipped
// with the extension. Four trees in the sidebar, one detail panel, a status bar item.
// Read-only: nothing here records, syncs or resolves anything.

import * as path from "node:path";
import * as vscode from "vscode";
import { DetailPanel, type DetailHost, type Page } from "./detail";
import { Reader, readerPath, type DecisionFilter, type Decisions, type PeopleBy, type State, type Store } from "./reader";
import { ConflictItem, ConflictsProvider, DecisionsProvider, ElementsProvider, GroupItem, PeopleProvider, type Host } from "./trees";

export async function activate(context: vscode.ExtensionContext): Promise<App> {
  const app = new App(context);
  context.subscriptions.push(app);
  await app.start();
  return app;
}

export function deactivate(): void {}

export class App implements vscode.Disposable, Host, DetailHost {
  filter: DecisionFilter = {};
  peopleBy: PeopleBy = "actor";
  elementsText = "";
  elementsKind = "";
  readonly collapsedKinds = new Set<string>();
  readonly collapsedConflicts = new Set<string>();

  readonly decisionsProvider = new DecisionsProvider(this);
  readonly elementsProvider = new ElementsProvider(this);
  readonly conflictsProvider = new ConflictsProvider(this);
  readonly peopleProvider = new PeopleProvider(this);
  readonly detail = new DetailPanel(this);

  private rd: Reader | undefined;
  private status: Store | undefined;
  private readonly output = vscode.window.createOutputChannel("kgai");
  private readonly statusBar = vscode.window.createStatusBarItem("kgai", vscode.StatusBarAlignment.Left, 50);
  private readonly decisionsView: vscode.TreeView<vscode.TreeItem>;
  private readonly elementsView: vscode.TreeView<vscode.TreeItem>;
  private readonly conflictsView: vscode.TreeView<vscode.TreeItem>;
  private readonly peopleView: vscode.TreeView<vscode.TreeItem>;
  private readonly disposables: vscode.Disposable[] = [];
  /** Resolves once the first status has been read (tests wait for it). */
  readonly ready: Promise<void>;
  private markReady!: () => void;

  constructor(private readonly context: vscode.ExtensionContext) {
    this.ready = new Promise((resolve) => (this.markReady = resolve));
    this.decisionsView = vscode.window.createTreeView("kgai.decisions", { treeDataProvider: this.decisionsProvider, showCollapseAll: false });
    // Collapse All / Expand All are the extension's own pair (VS Code only offers the first).
    this.elementsView = vscode.window.createTreeView("kgai.elements", { treeDataProvider: this.elementsProvider, showCollapseAll: false });
    this.conflictsView = vscode.window.createTreeView("kgai.conflicts", { treeDataProvider: this.conflictsProvider });
    this.peopleView = vscode.window.createTreeView("kgai.people", { treeDataProvider: this.peopleProvider });
    // What the reader folds by hand stays folded through every refresh of the tree.
    this.elementsView.onDidCollapseElement((e) => {
      if (e.element instanceof GroupItem) {
        this.collapsedKinds.add(e.element.group.kind);
        this.updateFoldContext();
      }
    });
    this.elementsView.onDidExpandElement((e) => {
      if (e.element instanceof GroupItem) {
        this.collapsedKinds.delete(e.element.group.kind);
        this.updateFoldContext();
      }
    });
    this.conflictsView.onDidCollapseElement((e) => e.element instanceof ConflictItem && this.collapsedConflicts.add(e.element.conflict.elementId));
    this.conflictsView.onDidExpandElement((e) => e.element instanceof ConflictItem && this.collapsedConflicts.delete(e.element.conflict.elementId));
    this.statusBar.name = "kgai";
    this.statusBar.command = "kgai.overview";
    this.disposables.push(this.output, this.statusBar, this.decisionsView, this.elementsView, this.conflictsView, this.peopleView, this.detail);
    this.registerCommands();
    this.disposables.push(
      vscode.workspace.onDidChangeConfiguration((e) => {
        if (e.affectsConfiguration("kgai")) {
          void this.restart();
        }
      }),
      vscode.workspace.onDidChangeWorkspaceFolders(() => void this.restart()),
    );
  }

  // ---- lifecycle ------------------------------------------------------------------------

  log(line: string): void {
    this.output.appendLine(line);
  }

  reader(): Reader | undefined {
    return this.rd;
  }

  store(): Store | undefined {
    return this.status;
  }

  hasStore(): boolean {
    return !!this.status && !this.status.error;
  }

  private projectDir(): string {
    const configured = vscode.workspace.getConfiguration("kgai").get<string>("projectFolder", "").trim();
    if (configured) {
      return path.isAbsolute(configured) ? configured : path.join(vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? "", configured);
    }
    return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? "";
  }

  async start(): Promise<void> {
    const exe = readerPath(this.context.extensionPath, vscode.workspace.getConfiguration("kgai").get<string>("readerPath"));
    const dir = this.projectDir();
    const rd = new Reader(exe, dir, (line) => this.log(`kgread: ${line}`));
    this.rd = rd;
    try {
      await rd.start();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      this.log(`cannot start ${exe}: ${msg}`);
      this.rd = undefined;
      await vscode.commands.executeCommand("setContext", "kgai.readerMissing", true);
      await vscode.commands.executeCommand("setContext", "kgai.hasStore", false);
      this.statusBar.text = "$(warning) kgai: reader missing";
      this.statusBar.tooltip = `The reader could not be started: ${exe}\n${msg}`;
      this.statusBar.show();
      this.markReady();
      return;
    }
    await vscode.commands.executeCommand("setContext", "kgai.readerMissing", false);
    rd.on("changed", (st: Store) => void this.applyStatus(st, true));
    rd.on("exit", (why: string) => {
      if (this.rd === rd) {
        this.log(why);
        this.statusBar.text = "$(warning) kgai: reader stopped";
        this.statusBar.tooltip = `${why}\nRun "kgai: Refresh" to start it again.`;
      }
    });
    try {
      await this.applyStatus(await rd.status());
    } catch (e) {
      this.log(`status: ${e instanceof Error ? e.message : String(e)}`);
    }
    this.markReady();
  }

  private async restart(): Promise<void> {
    this.rd?.dispose();
    this.rd = undefined;
    await this.start();
  }

  private async applyStatus(st: Store, changed = false): Promise<void> {
    this.status = st;
    await vscode.commands.executeCommand("setContext", "kgai.hasStore", !st.error);
    const project = path.basename(st.project) || st.project;
    if (st.error) {
      this.statusBar.text = "$(circle-slash) kgai";
      this.statusBar.tooltip = st.hint ?? st.error;
    } else {
      const c = st.counts;
      this.statusBar.text = `$(git-commit) kg ${c.decisions}` + (c.conflicts ? `  $(warning) ${c.conflicts}` : "");
      this.statusBar.tooltip = new vscode.MarkdownString(
        `**${project}** — ${c.decisions} decisions, ${c.elements} elements, ${c.conflicts} conflicts, ${c.people} people\n\n${st.root}\n\n${st.rule}` +
          (st.pending ? `\n\n⚠ ${st.pending} is waiting for approval and decides nothing here (kg trust --show)` : ""),
      );
    }
    this.statusBar.show();
    if (st.pending && !changed) {
      this.log(`The committed configuration ${st.pending} is waiting for approval and decides nothing here. Review it with kg trust --show, approve it with kg trust.`);
    }
    this.refreshTrees();
    if (changed) {
      void this.detail.refresh();
    }
  }

  private refreshTrees(): void {
    this.decisionsProvider.refresh();
    this.elementsProvider.refresh();
    this.conflictsProvider.refresh();
    this.peopleProvider.refresh();
    this.peopleView.title = this.peopleBy === "author" ? "People — credited to" : "People — recorded by";
    const conflicts = this.status && !this.status.error ? this.status.counts.conflicts : 0;
    this.conflictsView.badge = conflicts ? { value: conflicts, tooltip: "contested elements" } : undefined;
    this.conflictsView.message = this.hasStore() && !conflicts ? "No contested elements — every element is governed by one current decision." : undefined;
  }

  decisionsLoaded(result: Decisions): void {
    const narrowed = describeFilter(this.filter);
    void vscode.commands.executeCommand("setContext", "kgai.filtered", narrowed !== "");
    if (result.error) {
      this.decisionsView.message = result.error;
    } else if (narrowed) {
      this.decisionsView.message = `Showing ${result.shown} of ${result.total} · ${narrowed}`;
    } else {
      this.decisionsView.message = undefined;
    }
    this.decisionsView.badge = narrowed ? { value: result.shown, tooltip: `${result.shown} of ${result.total} decisions` } : undefined;
  }

  /** Which of the fold buttons the Elements view shows: Collapse All while something is open, Expand All while something is folded. */
  updateFoldContext(): void {
    const kinds = this.elementsProvider.groups.map((g) => g.group.kind);
    const folded = kinds.filter((k) => this.collapsedKinds.has(k)).length;
    void vscode.commands.executeCommand("setContext", "kgai.elementsCollapsed", folded > 0);
    void vscode.commands.executeCommand("setContext", "kgai.elementsExpanded", folded < kinds.length);
  }

  /** Folds every kind group. */
  async collapseElements(): Promise<void> {
    for (const g of this.elementsProvider.groups) {
      this.collapsedKinds.add(g.group.kind);
    }
    const builtIn = "workbench.actions.treeView.kgai.elements.collapseAll";
    if ((await vscode.commands.getCommands(true)).includes(builtIn)) {
      await vscode.commands.executeCommand(builtIn);
    }
    this.elementsProvider.refresh();
    this.updateFoldContext();
  }

  /** Opens every kind group. */
  async expandElements(): Promise<void> {
    this.collapsedKinds.clear();
    // Last reveal wins the scroll position: open the kinds bottom-up so the tree ends at the top.
    for (const g of [...this.elementsProvider.groups].reverse()) {
      try {
        await this.elementsView.reveal(g, { expand: true, select: false, focus: false });
      } catch (e) {
        this.log(`expand ${g.group.kind}: ${e instanceof Error ? e.message : String(e)}`);
      }
    }
    this.elementsProvider.refresh();
    this.updateFoldContext();
  }

  elementsLoaded(shown: number, total: number): void {
    void vscode.commands.executeCommand("setContext", "kgai.elementsFiltered", this.elementsKind !== "" || this.elementsText !== "");
    this.updateFoldContext();
    const parts: string[] = [];
    if (this.elementsKind) {
      parts.push(`kind: ${this.elementsKind}`);
    }
    if (this.elementsText) {
      parts.push(`name contains "${this.elementsText}"`);
    }
    this.elementsView.message = parts.length ? `Showing ${shown} of ${total} · ${parts.join(", ")}` : undefined;
  }

  // ---- navigation -----------------------------------------------------------------------

  open(kind: string, id: string): void {
    switch (kind) {
      case "decision":
      case "element":
      case "conflict":
        void this.detail.show({ kind, id });
        break;
      case "person":
        void this.detail.show({ kind: "person", id, by: this.peopleBy });
        break;
      case "decisions":
        this.showDecisionsFor(id);
        break;
    }
  }

  /** Narrows the decisions tree to a person ("actor=…" / "author=…") or an element id. */
  private showDecisionsFor(target: string): void {
    const m = /^(actor|author)=(.*)$/.exec(target);
    this.setFilter(m ? { [m[1]]: decodeURIComponent(m[2]) } : { elementId: target });
    void vscode.commands.executeCommand("kgai.decisions.focus");
  }

  setFilter(f: DecisionFilter): void {
    this.filter = f;
    this.decisionsProvider.refresh();
  }

  // ---- commands -------------------------------------------------------------------------

  private registerCommands(): void {
    const cmd = (name: string, fn: (...args: any[]) => unknown) => this.disposables.push(vscode.commands.registerCommand(name, fn));
    cmd("kgai.overview", () => this.detail.show({ kind: "overview" }));
    cmd("kgai.help", () => this.detail.show({ kind: "help" }));
    cmd("kgai.refresh", () => this.refresh());
    cmd("kgai.showDecision", (id: string) => this.detail.show({ kind: "decision", id }));
    cmd("kgai.showElement", (id: string) => this.detail.show({ kind: "element", id }));
    cmd("kgai.showConflict", (id: string) => this.detail.show({ kind: "conflict", id }));
    cmd("kgai.showPerson", (name: string) => this.detail.show({ kind: "person", id: name, by: this.peopleBy }));
    cmd("kgai.search", () => this.search());
    cmd("kgai.filter", () => this.pickFilter());
    cmd("kgai.clearFilters", () => this.setFilter({}));
    cmd("kgai.searchElements", () => this.searchElements());
    cmd("kgai.clearElementSearch", () => this.setElementSearch("", ""));
    cmd("kgai.collapseElements", () => this.collapseElements());
    cmd("kgai.expandElements", () => this.expandElements());
    cmd("kgai.peopleBy", () => this.pickPeopleBy());
    cmd("kgai.openFolder", () => this.openFolder());
  }

  private async refresh(): Promise<void> {
    if (!this.rd) {
      await this.start();
      return;
    }
    try {
      await this.applyStatus(await this.rd.refresh(), true);
    } catch (e) {
      this.log(`refresh: ${e instanceof Error ? e.message : String(e)}`);
      await this.restart();
    }
  }

  /** Type-ahead over title and rationale; picking a decision opens it. */
  private async search(): Promise<void> {
    const rd = this.rd;
    if (!rd || !this.hasStore()) {
      return;
    }
    const qp = vscode.window.createQuickPick<vscode.QuickPickItem & { id?: string; apply?: boolean }>();
    qp.placeholder = "Search decisions by title and rationale (exact substring, newest first)";
    qp.matchOnDescription = true;
    qp.matchOnDetail = true;
    let seq = 0;
    const load = async (text: string) => {
      const mine = ++seq;
      qp.busy = true;
      try {
        const res = await rd.decisions({ ...this.filter, text });
        if (mine !== seq) {
          return;
        }
        const items: (vscode.QuickPickItem & { id?: string; apply?: boolean })[] = res.rows.slice(0, 200).map((r) => ({
          label: r.title,
          description: `${r.day} · ${r.actor} · ${r.state}`,
          id: r.id,
          alwaysShow: true,
        }));
        if (text) {
          items.unshift({ label: `$(filter) Keep "${text}" as the filter`, description: `${res.shown} of ${res.total}`, apply: true, alwaysShow: true });
        }
        qp.items = items;
        qp.title = res.error ?? `${res.shown} of ${res.total} decisions`;
      } finally {
        if (mine === seq) {
          qp.busy = false;
        }
      }
    };
    qp.onDidChangeValue((v) => void load(v));
    qp.onDidAccept(() => {
      const pick = qp.selectedItems[0];
      if (pick?.apply) {
        this.setFilter({ ...this.filter, text: qp.value });
      } else if (pick?.id) {
        void this.detail.show({ kind: "decision", id: pick.id });
      }
      qp.hide();
    });
    qp.onDidHide(() => qp.dispose());
    qp.show();
    void load("");
  }

  /** Two steps: which filter, then its value. */
  private async pickFilter(): Promise<void> {
    const rd = this.rd;
    if (!rd || !this.hasStore()) {
      return;
    }
    const f = this.filter;
    type Step = vscode.QuickPickItem & { key: keyof DecisionFilter | "clear" };
    const current = (v: string | boolean | undefined) => (v === undefined || v === "" || v === false ? "" : `now: ${v}`);
    const steps: Step[] = [
      { key: "state", label: "State", description: current(f.state), detail: "head (in force), superseded (replaced), note (context only)" },
      { key: "actor", label: "Recorded by", description: current(f.actor), detail: "the install identity that wrote the decision" },
      { key: "author", label: "Credited to", description: current(f.author), detail: "the decision's author field" },
      { key: "kind", label: "Element kind", description: current(f.kind) },
      { key: "element", label: "Element name contains", description: current(f.element) },
      { key: "install", label: "Install", description: current(f.install), detail: "one copy of kg on one machine (a log shard)" },
      { key: "from", label: "From date", description: current(f.from), detail: "YYYY-MM-DD, inclusive" },
      { key: "to", label: "To date", description: current(f.to), detail: "YYYY-MM-DD, inclusive" },
      { key: "text", label: "Text", description: current(f.text), detail: "substring of title, rationale or summary" },
      { key: "regex", label: "Regular expression", description: f.regex ? "on" : "off", detail: "treat Text as a (case-insensitive) regular expression" },
      { key: "oldest", label: "Order", description: f.oldest ? "oldest first" : "newest first" },
      { key: "clear", label: "$(clear-all) Clear all filters", description: describeFilter(f) },
    ];
    const step = await vscode.window.showQuickPick(steps, { placeHolder: "Narrow the decisions by…", title: describeFilter(f) || "All decisions" });
    if (!step) {
      return;
    }
    const next: DecisionFilter = { ...f };
    const pickValue = async (values: { key: string; n: number }[], placeHolder: string): Promise<string | undefined> => {
      const items: (vscode.QuickPickItem & { value: string })[] = [{ label: "$(circle-slash) any", value: "" }, ...values.map((v) => ({ label: v.key === "" ? "(none)" : v.key, description: String(v.n), value: v.key }))];
      const pick = await vscode.window.showQuickPick(items, { placeHolder });
      return pick?.value;
    };
    switch (step.key) {
      case "clear":
        this.setFilter({});
        return;
      case "state": {
        const v = await pickValue([{ key: "head", n: 0 }, { key: "superseded", n: 0 }, { key: "note", n: 0 }].map((x) => ({ key: x.key, n: x.n })), "State");
        if (v === undefined) {
          return;
        }
        next.state = v as State | "";
        break;
      }
      case "actor":
      case "author":
      case "kind":
      case "install": {
        const filters = await rd.filters();
        const values = step.key === "actor" ? filters.actors : step.key === "author" ? filters.authors : step.key === "kind" ? filters.kinds : filters.installs;
        const v = await pickValue(values, step.label);
        if (v === undefined) {
          return;
        }
        next[step.key] = v;
        break;
      }
      case "regex":
        next.regex = !f.regex;
        break;
      case "oldest":
        next.oldest = !f.oldest;
        break;
      case "from":
      case "to":
      case "element":
      case "text": {
        const v = await vscode.window.showInputBox({
          prompt: step.label,
          value: (f[step.key] as string | undefined) ?? "",
          placeHolder: step.key === "from" || step.key === "to" ? "YYYY-MM-DD (empty: no bound)" : "empty: any",
          validateInput: (s) => (step.key === "from" || step.key === "to") && s && !/^\d{4}-\d{2}-\d{2}$/.test(s) ? "Use YYYY-MM-DD" : undefined,
        });
        if (v === undefined) {
          return;
        }
        next[step.key] = v;
        break;
      }
    }
    this.setFilter(next);
  }

  setElementSearch(kind: string, text: string): void {
    this.elementsKind = kind;
    this.elementsText = text.trim();
    this.elementsProvider.refresh();
  }

  /** Type-ahead over element names; picking one opens it, or keep the text as the filter. */
  private async searchElements(): Promise<void> {
    const rd = this.rd;
    if (!rd || !this.hasStore()) {
      return;
    }
    type Item = vscode.QuickPickItem & { id?: string; apply?: boolean; clear?: boolean; pickKind?: boolean };
    const qp = vscode.window.createQuickPick<Item>();
    qp.placeholder = this.elementsKind ? `Search ${this.elementsKind} elements by name` : "Search elements by name (exact substring)";
    qp.value = this.elementsText;
    qp.matchOnDescription = true;
    let seq = 0;
    const load = async (text: string) => {
      const mine = ++seq;
      qp.busy = true;
      try {
        const res = await rd.elements(this.elementsKind, text);
        if (mine !== seq) {
          return;
        }
        const items: Item[] = [];
        if (text || this.elementsKind) {
          items.push({ label: `$(filter) Keep "${text}"${this.elementsKind ? ` in ${this.elementsKind}` : ""} as the filter`, description: `${res.shown} of ${res.total}`, apply: true, alwaysShow: true });
        }
        items.push({ label: "$(symbol-class) Only one kind…", description: this.elementsKind ? `now: ${this.elementsKind}` : "", pickKind: true, alwaysShow: true });
        if (this.elementsKind || this.elementsText) {
          items.push({ label: "$(clear-all) Show all elements", clear: true, alwaysShow: true });
        }
        for (const g of res.groups) {
          for (const e of g.elements.slice(0, 200)) {
            items.push({ label: e.name, description: `${g.kind} · ${e.decisions}${e.conflict ? " · conflict" : ""}`, id: e.id, alwaysShow: true });
          }
        }
        qp.items = items;
        qp.title = `${res.shown} of ${res.total} elements`;
      } finally {
        if (mine === seq) {
          qp.busy = false;
        }
      }
    };
    qp.onDidChangeValue((v) => void load(v));
    qp.onDidAccept(() => {
      const pick = qp.selectedItems[0];
      const text = qp.value;
      qp.hide();
      if (pick?.clear) {
        this.setElementSearch("", "");
      } else if (pick?.apply) {
        this.setElementSearch(this.elementsKind, text);
      } else if (pick?.pickKind) {
        void this.pickElementKind(text);
      } else if (pick?.id) {
        void this.detail.show({ kind: "element", id: pick.id });
      }
    });
    qp.onDidHide(() => qp.dispose());
    qp.show();
    void load(qp.value);
  }

  private async pickElementKind(text: string): Promise<void> {
    const rd = this.rd;
    if (!rd) {
      return;
    }
    const filters = await rd.filters();
    const kinds: (vscode.QuickPickItem & { value: string })[] = [{ label: "$(circle-slash) all kinds", value: "" }, ...filters.kinds.map((k) => ({ label: k.key, description: String(k.n), value: k.key }))];
    const kind = await vscode.window.showQuickPick(kinds, { placeHolder: "Show only elements of this kind" });
    if (kind) {
      this.setElementSearch(kind.value, text);
    }
  }

  private async pickPeopleBy(): Promise<void> {
    const pick = await vscode.window.showQuickPick(
      [
        { label: "Recorded by", description: "the install identity that wrote the decision (one per person per machine)", value: "actor" as PeopleBy },
        { label: "Credited to", description: "the decision's author field (an import, a pair, an assistant recording for a person)", value: "author" as PeopleBy },
      ],
      { placeHolder: "Count people by…" },
    );
    if (!pick) {
      return;
    }
    this.peopleBy = pick.value;
    this.refreshTrees();
  }

  private async openFolder(): Promise<void> {
    const picked = await vscode.window.showOpenDialog({ canSelectFiles: false, canSelectFolders: true, canSelectMany: false, openLabel: "Show this project's decisions" });
    const dir = picked?.[0]?.fsPath;
    if (!dir) {
      return;
    }
    const target = vscode.workspace.workspaceFolders?.length ? vscode.ConfigurationTarget.Workspace : vscode.ConfigurationTarget.Global;
    await vscode.workspace.getConfiguration("kgai").update("projectFolder", dir, target); // restarts the reader there
  }

  dispose(): void {
    this.rd?.dispose();
    for (const d of this.disposables) {
      d.dispose();
    }
  }
}

/** The active filters as one phrase for the view's message. */
export function describeFilter(f: DecisionFilter): string {
  const parts: string[] = [];
  if (f.text) {
    parts.push(`${f.regex ? "matches" : "contains"} "${f.text}"`);
  }
  if (f.state) {
    parts.push(`state: ${f.state}`);
  }
  if (f.actor) {
    parts.push(`recorded by ${f.actor}`);
  }
  if (f.author) {
    parts.push(`credited to ${f.author}`);
  }
  if (f.kind) {
    parts.push(`kind: ${f.kind}`);
  }
  if (f.element) {
    parts.push(`element contains "${f.element}"`);
  }
  if (f.elementId) {
    parts.push("one element");
  }
  if (f.install) {
    parts.push(`install ${f.install}`);
  }
  if (f.from || f.to) {
    parts.push(`${f.from || "…"} to ${f.to || "…"}`);
  }
  return parts.join(", ");
}

export type { Page };
