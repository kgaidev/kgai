// The extension inside a real VS Code: activation against the fixture project, the
// four trees, the filter, the detail panel, and a change of the log.
import * as assert from "assert";
import * as fs from "fs";
import Mocha from "mocha";
import * as path from "path";
import * as vscode from "vscode";
import type { App } from "../../src/extension";
import type { DecisionItem, ElementItem, GroupItem, PersonItem } from "../../src/trees";

export function run(): Promise<void> {
  const mocha = new Mocha({ ui: "tdd", color: true, timeout: 60000 });
  mocha.suite.emit("pre-require", globalThis, "suite", mocha);

  suite("kgai in VS Code", () => {
    let app: App;

    suiteSetup(async () => {
      const ext = vscode.extensions.getExtension("kgaidev.kgai");
      assert.ok(ext, "the extension is installed");
      app = (await ext.activate()) as App;
      await app.ready;
    });

    test("reads the fixture store", async () => {
      const st = app.store();
      assert.ok(st, "a status");
      assert.equal(st.error, undefined, st.hint);
      assert.equal(st.counts.decisions, 4);
      assert.ok(app.hasStore());
    });

    test("lists decisions, newest first, and narrows them", async () => {
      const rows = (await app.decisionsProvider.getChildren()) as DecisionItem[];
      assert.equal(rows.length, 4);
      assert.equal(rows[0].row.title, "Considered PDF-only invoices, rejected");
      app.setFilter({ state: "note" });
      const notes = (await app.decisionsProvider.getChildren()) as DecisionItem[];
      assert.equal(notes.length, 1);
      app.setFilter({});
      assert.equal(((await app.decisionsProvider.getChildren()) as DecisionItem[]).length, 4);
    });

    test("groups elements by kind, lists conflicts and people", async () => {
      const groups = (await app.elementsProvider.getChildren()) as GroupItem[];
      assert.equal(groups.length, 1);
      assert.equal(groups[0].group.kind, "feature");
      const els = (await app.elementsProvider.getChildren(groups[0])) as ElementItem[];
      assert.deepEqual(
        els.map((e) => e.row.name),
        ["Invoice", "Pricing"],
      );
      assert.equal(els[0].row.conflict, true);
      const conflicts = await app.conflictsProvider.getChildren();
      assert.equal(conflicts.length, 1);
      const heads = await app.conflictsProvider.getChildren(conflicts[0]);
      assert.equal(heads.length, 2);
      const people = (await app.peopleProvider.getChildren()) as PersonItem[];
      assert.deepEqual(
        people.map((p) => p.row.name),
        ["alice", "bob"],
      );
    });

    test("keeps a kind folded through a refresh, and clears the element search", async () => {
      app.collapsedKinds.add("feature");
      app.elementsProvider.refresh();
      const groups = (await app.elementsProvider.getChildren()) as GroupItem[];
      assert.equal(groups[0].collapsibleState, vscode.TreeItemCollapsibleState.Collapsed);
      app.collapsedKinds.delete("feature");
      assert.equal(((await app.elementsProvider.getChildren()) as GroupItem[])[0].collapsibleState, vscode.TreeItemCollapsibleState.Expanded);

      app.setElementSearch("", "pric");
      const narrowed = (await app.elementsProvider.getChildren()) as GroupItem[];
      assert.equal(narrowed[0].group.count, 1);
      await vscode.commands.executeCommand("kgai.clearElementSearch");
      assert.equal(app.elementsText, "");
      assert.equal(((await app.elementsProvider.getChildren()) as GroupItem[])[0].group.count, 2);
    });

    test("gives each conflict head its own id", async () => {
      const conflicts = await app.conflictsProvider.getChildren();
      const heads = (await app.conflictsProvider.getChildren(conflicts[0])) as DecisionItem[];
      assert.equal(heads.length, 2);
      assert.ok(heads[0].id?.startsWith("c:"));
      assert.notEqual(heads[0].id, heads[1].id);
    });

    test("opens details in the panel and goes back", async () => {
      const rows = (await app.decisionsProvider.getChildren()) as DecisionItem[];
      await vscode.commands.executeCommand("kgai.showDecision", rows[0].row.id);
      assert.equal(app.detail.currentPage()?.kind, "decision");
      await vscode.commands.executeCommand("kgai.showPerson", "bob");
      assert.equal(app.detail.currentPage()?.kind, "person");
      await app.detail.back();
      assert.equal(app.detail.currentPage()?.kind, "decision");
      await vscode.commands.executeCommand("kgai.overview");
      assert.equal(app.detail.currentPage()?.kind, "overview");
      await vscode.commands.executeCommand("kgai.help");
      assert.equal(app.detail.currentPage()?.kind, "help");
    });

    test("follows the log", async () => {
      const root = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
      assert.ok(root);
      const more = fs.readFileSync(process.env.KGAI_FIXTURE_MORE as string);
      fs.appendFileSync(path.join(root, ".kgai", "store", "log", "i-alice.ndjson"), more);
      const deadline = Date.now() + 15000;
      while (Date.now() < deadline && app.store()?.counts.decisions !== 5) {
        await new Promise((r) => setTimeout(r, 250));
      }
      assert.equal(app.store()?.counts.decisions, 5);
      assert.equal(((await app.decisionsProvider.getChildren()) as DecisionItem[]).length, 5);
    });
  });

  return new Promise((resolve, reject) => {
    mocha.run((failures) => (failures ? reject(new Error(`${failures} test(s) failed`)) : resolve()));
  });
}
