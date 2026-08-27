// The four sidebar trees: decisions (newest first, filtered), elements grouped by
// kind, contested elements with their competing decisions, and people. Each row opens
// its detail in the panel; nothing here decides anything the reader did not.

import * as vscode from "vscode";
import type { Conflict, ConflictHead, DecisionFilter, Decisions, ElementGroup, ElementRow, PeopleBy, PersonRow, Reader, Row, State } from "./reader";

/** What the trees need from the extension. */
export interface Host {
  reader(): Reader | undefined;
  hasStore(): boolean;
  filter: DecisionFilter;
  peopleBy: PeopleBy;
  elementsText: string;
  elementsKind: string;
  /** Groups the reader collapsed by hand — kept across every refresh of the tree. */
  collapsedKinds: Set<string>;
  collapsedConflicts: Set<string>;
  decisionsLoaded(result: Decisions): void;
  elementsLoaded(shown: number, total: number): void;
  log(line: string): void;
}

const stateColor: Record<State, string> = {
  head: "charts.green",
  superseded: "disabledForeground",
  note: "charts.blue",
};

const stateWord: Record<State, string> = {
  head: "head — still governs an element",
  superseded: "superseded — replaced by a later decision",
  note: "note — context only, governs nothing",
};

export function stateIcon(state: State): vscode.ThemeIcon {
  return new vscode.ThemeIcon("circle-filled", new vscode.ThemeColor(stateColor[state]));
}

export class DecisionItem extends vscode.TreeItem {
  constructor(public readonly row: Row) {
    super(row.title, vscode.TreeItemCollapsibleState.None);
    this.id = "d:" + row.id;
    this.description = `${row.day} · ${row.actor}`;
    this.iconPath = stateIcon(row.state);
    const md = new vscode.MarkdownString();
    md.appendMarkdown(`**${row.title}**\n\n${row.time} · recorded by ${row.actor}`);
    if (row.author !== row.actor) {
      md.appendMarkdown(`, credited to ${row.author}`);
    }
    md.appendMarkdown(`\n\n${stateWord[row.state]}`);
    this.tooltip = md;
    this.contextValue = "decision";
    this.command = { command: "kgai.showDecision", title: "Show Decision", arguments: [row.id] };
  }
}

abstract class Provider<T extends vscode.TreeItem> implements vscode.TreeDataProvider<T> {
  private readonly changed = new vscode.EventEmitter<T | undefined>();
  readonly onDidChangeTreeData = this.changed.event;
  constructor(protected readonly host: Host) {}
  refresh(): void {
    this.changed.fire(undefined);
  }
  getTreeItem(item: T): vscode.TreeItem {
    return item;
  }
  abstract getChildren(item?: T): Promise<T[]>;
  protected async ask<R>(f: (rd: Reader) => Promise<R>, fallback: R): Promise<R> {
    const rd = this.host.reader();
    if (!rd || !this.host.hasStore()) {
      return fallback;
    }
    try {
      return await f(rd);
    } catch (e) {
      this.host.log(`tree: ${e instanceof Error ? e.message : String(e)}`);
      return fallback;
    }
  }
}

export class DecisionsProvider extends Provider<DecisionItem> {
  async getChildren(): Promise<DecisionItem[]> {
    const result = await this.ask((rd) => rd.decisions(this.host.filter), { rows: [], total: 0, shown: 0 } as Decisions);
    this.host.decisionsLoaded(result);
    return result.rows.map((r) => new DecisionItem(r));
  }
}

export class GroupItem extends vscode.TreeItem {
  constructor(
    public readonly group: ElementGroup,
    collapsed = false,
  ) {
    super(group.kind, collapsed ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.Expanded);
    this.id = "k:" + group.kind;
    this.description = String(group.count);
    this.iconPath = new vscode.ThemeIcon("symbol-class");
    this.contextValue = "kind";
  }
}

export class ElementItem extends vscode.TreeItem {
  constructor(public readonly row: ElementRow) {
    super(row.name, vscode.TreeItemCollapsibleState.None);
    this.id = "e:" + row.id;
    this.description = row.conflict ? `${row.decisions} · conflict` : String(row.decisions);
    this.iconPath = row.conflict
      ? new vscode.ThemeIcon("warning", new vscode.ThemeColor("charts.red"))
      : new vscode.ThemeIcon("symbol-field");
    this.tooltip = `${row.kind}: ${row.name} — shaped by ${row.decisions} decision${row.decisions === 1 ? "" : "s"}${row.conflict ? ", contested" : ""}`;
    this.contextValue = "element";
    this.command = { command: "kgai.showElement", title: "Show Element", arguments: [row.id] };
  }
}

export class ElementsProvider extends Provider<GroupItem | ElementItem> {
  async getChildren(item?: GroupItem | ElementItem): Promise<(GroupItem | ElementItem)[]> {
    if (item instanceof GroupItem) {
      return item.group.elements.map((e) => new ElementItem(e));
    }
    if (item) {
      return [];
    }
    const result = await this.ask((rd) => rd.elements(this.host.elementsKind, this.host.elementsText), { groups: [], shown: 0, total: 0 });
    this.host.elementsLoaded(result.shown, result.total);
    return result.groups.map((g) => new GroupItem(g, this.host.collapsedKinds.has(g.kind)));
  }
}

export class ConflictItem extends vscode.TreeItem {
  constructor(
    public readonly conflict: Conflict,
    collapsed = false,
  ) {
    super(conflict.name, collapsed ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.Expanded);
    this.id = "c:" + conflict.elementId;
    this.description = `${conflict.kind} · ${conflict.heads.length} heads`;
    this.iconPath = new vscode.ThemeIcon("warning", new vscode.ThemeColor("charts.red"));
    this.tooltip = `${conflict.kind}: ${conflict.name} is governed by ${conflict.heads.length} decisions at once`;
    this.contextValue = "conflict";
    this.command = { command: "kgai.showConflict", title: "Show Conflict", arguments: [conflict.elementId] };
  }
}

function headRow(h: ConflictHead): Row {
  return { id: h.id, title: h.title, day: h.day, time: h.time, actor: h.actor, author: h.actor, state: "head" };
}

export class ConflictsProvider extends Provider<ConflictItem | DecisionItem> {
  async getChildren(item?: ConflictItem | DecisionItem): Promise<(ConflictItem | DecisionItem)[]> {
    if (item instanceof ConflictItem) {
      // One decision can be a head on two contested elements; ids must stay unique.
      return item.conflict.heads.map((h) => {
        const d = new DecisionItem(headRow(h));
        d.id = `c:${item.conflict.elementId}:${h.id}`;
        return d;
      });
    }
    if (item) {
      return [];
    }
    const conflicts = await this.ask((rd) => rd.conflicts(), [] as Conflict[]);
    return conflicts.map((c) => new ConflictItem(c, this.host.collapsedConflicts.has(c.elementId)));
  }
}

export class PersonItem extends vscode.TreeItem {
  constructor(public readonly row: PersonRow) {
    super(row.name, vscode.TreeItemCollapsibleState.None);
    this.id = "p:" + row.name;
    this.description = `${row.decisions} · ${row.heads} in force · ${row.superseded} replaced · ${row.notes} notes`;
    this.iconPath = new vscode.ThemeIcon("account");
    this.tooltip = `${row.name}: ${row.decisions} decisions (${row.heads} heads, ${row.superseded} superseded, ${row.notes} notes), ${row.elements} elements, latest ${row.last}, ${row.last30} in the last 30 days`;
    this.contextValue = "person";
    this.command = { command: "kgai.showPerson", title: "Show Person", arguments: [row.name] };
  }
}

export class PeopleProvider extends Provider<PersonItem> {
  async getChildren(): Promise<PersonItem[]> {
    const rows = await this.ask((rd) => rd.people(this.host.peopleBy), [] as PersonRow[]);
    return rows.map((r) => new PersonItem(r));
  }
}
