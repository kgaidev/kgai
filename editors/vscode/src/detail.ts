// The detail panel: one webview that shows a decision, an element, a person, a
// contested element, the overview or Help, with a history so Back returns from every
// jump. The panel asks the reader for fresh data on every show, and re-renders when
// the log changes.

import { randomBytes } from "node:crypto";
import * as vscode from "vscode";
import { conflictPage, decisionPage, document, elementPage, emptyPage, esc, helpPage, overviewPage, personPage } from "./html";
import type { PeopleBy, Reader, Store } from "./reader";

export type PageKind = "decision" | "element" | "person" | "conflict" | "overview" | "help";

export interface Page {
  kind: PageKind;
  id?: string;
  by?: PeopleBy;
}

export interface DetailHost {
  reader(): Reader | undefined;
  store(): Store | undefined;
  peopleBy: PeopleBy;
  open(kind: string, id: string): void;
  log(line: string): void;
}

export class DetailPanel implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined;
  private history: Page[] = [];
  private current: Page | undefined;

  constructor(private readonly host: DetailHost) {}

  currentPage(): Page | undefined {
    return this.current;
  }

  /** Shows a page; push=false replaces the current one without a history entry. */
  async show(page: Page, push = true): Promise<void> {
    if (push && this.current && !samePage(this.current, page)) {
      this.history.push(this.current);
      if (this.history.length > 50) {
        this.history.shift();
      }
    }
    this.current = page;
    const panel = this.ensurePanel();
    panel.title = titleOf(page);
    panel.webview.html = await this.render(page, panel.webview);
    panel.reveal(undefined, true);
  }

  async back(): Promise<void> {
    const prev = this.history.pop();
    if (prev) {
      await this.show(prev, false);
    }
  }

  /** Re-renders the current page (the log changed). */
  async refresh(): Promise<void> {
    if (this.panel && this.current) {
      this.panel.webview.html = await this.render(this.current, this.panel.webview);
    }
  }

  private ensurePanel(): vscode.WebviewPanel {
    if (this.panel) {
      return this.panel;
    }
    const panel = vscode.window.createWebviewPanel("kgai.detail", "kgai", { viewColumn: vscode.ViewColumn.Beside, preserveFocus: true }, { enableScripts: true, retainContextWhenHidden: true });
    panel.iconPath = new vscode.ThemeIcon("book");
    panel.onDidDispose(() => {
      this.panel = undefined;
      this.history = [];
      this.current = undefined;
    });
    panel.webview.onDidReceiveMessage((msg: { type: string; kind?: string; id?: string; text?: string }) => {
      switch (msg.type) {
        case "open":
          if (msg.kind && msg.id !== undefined) {
            this.host.open(msg.kind, msg.id);
          }
          break;
        case "back":
          void this.back();
          break;
        case "help":
          void this.show({ kind: "help" });
          break;
        case "copy":
          if (msg.text) {
            void vscode.env.clipboard.writeText(msg.text);
          }
          break;
      }
    });
    this.panel = panel;
    return panel;
  }

  private async render(page: Page, webview: vscode.Webview): Promise<string> {
    const nonce = randomBytes(16).toString("hex");
    const wrap = (body: string) => document(body, titleOf(page), nonce, webview.cspSource, this.history.length > 0);
    if (page.kind === "help") {
      return wrap(helpPage());
    }
    const rd = this.host.reader();
    const store = this.host.store();
    if (!rd || !store) {
      return wrap(`<h1>kgai</h1><p class="muted">The reader is not running.</p>`);
    }
    if (store.error) {
      return wrap(emptyPage(store));
    }
    try {
      switch (page.kind) {
        case "overview":
          return wrap(overviewPage(await rd.overview()));
        case "decision":
          return wrap(decisionPage(await rd.decision(page.id ?? "")));
        case "element":
          return wrap(elementPage(await rd.element(page.id ?? "")));
        case "person": {
          const by = page.by ?? this.host.peopleBy;
          return wrap(personPage(await rd.person(page.id ?? "", by), by));
        }
        case "conflict": {
          const c = (await rd.conflicts()).find((x) => x.elementId === page.id);
          if (!c) {
            return wrap(`<h1>No longer contested</h1><p class="muted">This element is not in conflict any more — a later decision resolved it.</p>`);
          }
          return wrap(conflictPage(c));
        }
      }
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      this.host.log(`detail: ${msg}`);
      return wrap(`<h1>Not in this log</h1><p class="muted">${esc(msg)}</p>`);
    }
    return wrap("");
  }

  dispose(): void {
    this.panel?.dispose();
  }
}

function samePage(a: Page, b: Page): boolean {
  return a.kind === b.kind && a.id === b.id && a.by === b.by;
}

function titleOf(page: Page): string {
  switch (page.kind) {
    case "overview":
      return "kgai: Overview";
    case "help":
      return "kgai: Help";
    case "decision":
      return "kgai: Decision";
    case "element":
      return "kgai: Element";
    case "person":
      return `kgai: ${page.id ?? "Person"}`;
    case "conflict":
      return "kgai: Conflict";
  }
}
