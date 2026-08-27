// The client of kgread: spawns the reader, sends one JSON request per line, matches
// answers by id, and raises "changed" when the reader reports the log moved. No
// dependency on the vscode module, so the same client is exercised by the plain node
// tests against the real binary.

import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { EventEmitter } from "node:events";
import * as fs from "node:fs";
import * as path from "node:path";
import * as readline from "node:readline";

// ---- what the reader answers (mirrors internal/view) ------------------------------

export interface Count {
  key: string;
  n: number;
}
export interface ElementCount {
  id: string;
  kind: string;
  name: string;
  n: number;
}
export interface Counts {
  decisions: number;
  elements: number;
  links: number;
  conflicts: number;
  people: number;
  installs: number;
}
export interface Store {
  dir: string;
  project: string;
  root: string;
  rule: string;
  source: string;
  pending?: string;
  error?: string;
  hint?: string;
  installId?: string;
  actor?: string;
  remote?: string;
  transport: string;
  sync: string;
  loaded: string;
  counts: Counts;
}
export interface Install {
  id: string;
  decisions: number;
  last: string;
  actors: Count[];
}
export interface Overview {
  store: Store;
  first: string;
  last: string;
  elementsByKind: Count[];
  linksByKind: Count[];
  decisionsByMonth: Count[];
  decisionsByActor: Count[];
  mostShaped: ElementCount[];
  installs: Install[];
  events: number;
  setProps: number;
  retiredLinks: number;
  notes: number;
  stubs: number;
}
export type State = "head" | "superseded" | "note";
export interface Row {
  id: string;
  title: string;
  day: string;
  time: string;
  actor: string;
  author: string;
  state: State;
}
export interface Decisions {
  rows: Row[];
  total: number;
  shown: number;
  error?: string;
}
export interface DecisionFilter {
  text?: string;
  regex?: boolean;
  actor?: string;
  author?: string;
  kind?: string;
  install?: string;
  state?: State | "";
  from?: string;
  to?: string;
  elementId?: string;
  element?: string;
  oldest?: boolean;
}
export interface Ref {
  system: string;
  url?: string;
  text: string;
}
export interface Mutation {
  op: string;
  label: string;
  text: string;
}
export interface Shaped {
  id: string;
  kind: string;
  name: string;
  role: "governs" | "superseded" | "mentioned";
}
export interface DecisionRef {
  id: string;
  title: string;
  day: string;
  actor: string;
  inLog: boolean;
}
export interface Decision extends Row {
  stateSentence: string;
  rationale: string;
  summary: string;
  refs: Ref[];
  mutations: Mutation[];
  elements: Shaped[];
  supersedes: DecisionRef[];
  supersededBy: DecisionRef[];
  installId: string;
  lamport: number;
  eventHash: string;
}
export interface ElementRow {
  id: string;
  kind: string;
  name: string;
  decisions: number;
  conflict: boolean;
}
export interface ElementGroup {
  kind: string;
  count: number;
  elements: ElementRow[];
}
export interface Elements {
  groups: ElementGroup[];
  shown: number;
  total: number;
}
export interface KV {
  key: string;
  value: string;
}
export interface ElementRef {
  id: string;
  kind: string;
  name: string;
}
export interface ElementLink {
  kind: string;
  direction: "out" | "in";
  other: ElementRef;
}
export interface HistoryRow {
  id: string;
  title: string;
  day: string;
  actor: string;
  state: State;
}
export interface Element {
  id: string;
  kind: string;
  name: string;
  standing: string;
  heads: number;
  shapedBy: number;
  props: KV[];
  links: ElementLink[];
  history: HistoryRow[];
}
export interface ConflictHead {
  id: string;
  title: string;
  time: string;
  day: string;
  actor: string;
  rationale: string;
  mutations: Mutation[];
}
export interface Conflict {
  elementId: string;
  kind: string;
  name: string;
  heads: ConflictHead[];
}
export interface PersonRow {
  name: string;
  decisions: number;
  heads: number;
  superseded: number;
  notes: number;
  elements: number;
  last: string;
  last30: number;
}
export interface Recent {
  id: string;
  title: string;
  day: string;
  elements: string[];
}
export interface Person extends PersonRow {
  share: number;
  first: string;
  activeDays: number;
  last90: number;
  conflicts: number;
  byMonth: Count[];
  kinds: Count[];
  changes: Count[];
  installs: Count[];
  overrode: Count[];
  overriddenBy: Count[];
  creditedAs: Count[];
  mostShaped: ElementCount[];
  recent: Recent[];
}
export interface Filters {
  actors: Count[];
  authors: Count[];
  kinds: Count[];
  installs: Count[];
  states: State[];
}
export type PeopleBy = "actor" | "author";

// ---- the wire -------------------------------------------------------------------------

interface Pending {
  resolve: (v: unknown) => void;
  reject: (e: Error) => void;
}

/** Where the reader binary is: the configured path, the one shipped in the extension, or PATH. */
export function readerPath(extensionRoot: string, configured: string | undefined): string {
  if (configured && configured.trim() !== "") {
    return configured.trim();
  }
  const exe = process.platform === "win32" ? "kgread.exe" : "kgread";
  const bundled = path.join(extensionRoot, "bin", exe);
  if (fs.existsSync(bundled)) {
    return bundled;
  }
  return exe;
}

export class Reader extends EventEmitter {
  private proc: ChildProcessWithoutNullStreams | undefined;
  private next = 1;
  private readonly pending = new Map<number, Pending>();
  private exited: string | undefined;

  constructor(
    private readonly exe: string,
    private readonly dir: string,
    private readonly log: (line: string) => void = () => {},
    private readonly extraArgs: string[] = [],
  ) {
    super();
  }

  /** Spawns the reader. Rejects when the binary cannot be started. */
  start(): Promise<void> {
    return new Promise((resolve, reject) => {
      const args = ["--dir", this.dir, ...this.extraArgs];
      let proc: ChildProcessWithoutNullStreams;
      try {
        proc = spawn(this.exe, args, { stdio: ["pipe", "pipe", "pipe"], windowsHide: true });
      } catch (e) {
        reject(e instanceof Error ? e : new Error(String(e)));
        return;
      }
      this.proc = proc;
      let started = false;
      proc.once("spawn", () => {
        started = true;
        resolve();
      });
      proc.once("error", (e) => {
        this.exited = e.message;
        this.failAll(e);
        if (!started) {
          reject(e);
        }
        this.emit("exit", e.message);
      });
      proc.once("exit", (code, signal) => {
        this.exited = `kgread exited (${signal ?? code})`;
        this.failAll(new Error(this.exited));
        this.emit("exit", this.exited);
      });
      readline.createInterface({ input: proc.stdout }).on("line", (line) => this.receive(line));
      readline.createInterface({ input: proc.stderr }).on("line", (line) => this.log(line));
    });
  }

  private receive(line: string): void {
    if (line.trim() === "") {
      return;
    }
    let msg: { id?: number; result?: unknown; error?: string; method?: string; params?: unknown };
    try {
      msg = JSON.parse(line);
    } catch {
      this.log(`unreadable line from kgread: ${line}`);
      return;
    }
    if (msg.method) {
      this.emit(msg.method, msg.params);
      return;
    }
    if (msg.id === undefined) {
      this.log(`kgread: ${msg.error ?? line}`);
      return;
    }
    const p = this.pending.get(msg.id);
    if (!p) {
      return;
    }
    this.pending.delete(msg.id);
    if (msg.error) {
      p.reject(new Error(msg.error));
    } else {
      p.resolve(msg.result);
    }
  }

  private failAll(e: Error): void {
    for (const p of this.pending.values()) {
      p.reject(e);
    }
    this.pending.clear();
  }

  /** Sends one request and returns its result. */
  request<T>(method: string, params?: unknown): Promise<T> {
    const proc = this.proc;
    if (!proc || this.exited) {
      return Promise.reject(new Error(this.exited ?? "kgread is not running"));
    }
    const id = this.next++;
    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve: (v) => resolve(v as T), reject });
      const line = JSON.stringify(params === undefined ? { id, method } : { id, method, params });
      proc.stdin.write(line + "\n", (err) => {
        if (err) {
          this.pending.delete(id);
          reject(err);
        }
      });
    });
  }

  status(): Promise<Store> {
    return this.request("status");
  }
  open(dir: string): Promise<Store> {
    return this.request("open", { dir });
  }
  refresh(): Promise<Store> {
    return this.request("refresh");
  }
  overview(): Promise<Overview> {
    return this.request("overview");
  }
  filters(): Promise<Filters> {
    return this.request("filters");
  }
  decisions(filter: DecisionFilter): Promise<Decisions> {
    return this.request("decisions", filter);
  }
  decision(id: string): Promise<Decision> {
    return this.request("decision", { id });
  }
  elements(kind: string, text: string): Promise<Elements> {
    return this.request("elements", { kind, text });
  }
  element(id: string): Promise<Element> {
    return this.request("element", { id });
  }
  conflicts(): Promise<Conflict[]> {
    return this.request("conflicts");
  }
  people(by: PeopleBy): Promise<PersonRow[]> {
    return this.request("people", { by });
  }
  person(name: string, by: PeopleBy): Promise<Person> {
    return this.request("person", { name, by });
  }

  dispose(): void {
    const proc = this.proc;
    this.proc = undefined;
    if (proc && proc.exitCode === null) {
      proc.stdin.end();
      setTimeout(() => {
        if (proc.exitCode === null) {
          proc.kill();
        }
      }, 500).unref();
    }
  }
}
