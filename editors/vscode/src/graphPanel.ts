// The graph panel: one webview that draws the live graph (elements, links, and the
// decisions behind them) from the reader's data. It sends the graph on open and again
// whenever the log changes; clicks come back as "open" and land in the detail panel.

import { randomBytes } from "node:crypto";
import * as path from "node:path";
import * as vscode from "vscode";
import { graphDocument } from "./html";
import type { Graph, Reader, Store } from "./reader";

export interface GraphHost {
  reader(): Reader | undefined;
  store(): Store | undefined;
  open(kind: string, id: string): void;
  help(): void;
  log(line: string): void;
}

const emptyGraph: Graph = { nodes: [], links: [], decisions: [], kinds: [] };

export class GraphPanel implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined;
  /** The graph last sent to the page (tests read it). */
  lastGraph: Graph | undefined;

  constructor(
    private readonly host: GraphHost,
    private readonly extensionUri: vscode.Uri,
  ) {}

  isOpen(): boolean {
    return this.panel !== undefined;
  }

  /** Opens the graph in the active editor group, or brings it back. */
  show(): void {
    if (this.panel) {
      this.panel.reveal();
      return;
    }
    const panel = vscode.window.createWebviewPanel(
      "kgai.graph",
      "kgai: Graph",
      { viewColumn: vscode.ViewColumn.Active, preserveFocus: false },
      { enableScripts: true, retainContextWhenHidden: true, localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, "dist")] },
    );
    panel.iconPath = new vscode.ThemeIcon("type-hierarchy");
    panel.onDidDispose(() => {
      this.panel = undefined;
      this.lastGraph = undefined;
    });
    panel.webview.onDidReceiveMessage((msg: { type: string; kind?: string; id?: string }) => {
      switch (msg.type) {
        case "ready":
          void this.send();
          break;
        case "open":
          if (msg.kind && msg.id !== undefined) {
            this.host.open(msg.kind, msg.id);
          }
          break;
        case "help":
          this.host.help();
          break;
      }
    });
    const nonce = randomBytes(16).toString("hex");
    const script = panel.webview.asWebviewUri(vscode.Uri.joinPath(this.extensionUri, "dist", "src", "webview", "graph.js"));
    panel.webview.html = graphDocument(nonce, panel.webview.cspSource, script.toString());
    this.panel = panel;
  }

  /** Sends the graph again (the log changed). */
  async refresh(): Promise<void> {
    if (this.panel) {
      await this.send();
    }
  }

  private async send(): Promise<void> {
    const panel = this.panel;
    if (!panel) {
      return;
    }
    const rd = this.host.reader();
    const st = this.host.store();
    if (!rd || !st || st.error) {
      this.lastGraph = emptyGraph;
      await panel.webview.postMessage({ type: "graph", graph: emptyGraph, note: st?.hint ?? st?.error ?? "The reader is not running." });
      return;
    }
    try {
      const graph = await rd.graph();
      this.lastGraph = graph;
      await panel.webview.postMessage({ type: "graph", graph, project: path.basename(st.project) || st.project });
    } catch (e) {
      this.host.log(`graph: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  dispose(): void {
    this.panel?.dispose();
  }
}
