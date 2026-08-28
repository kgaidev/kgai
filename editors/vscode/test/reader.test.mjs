// The protocol client against the real kgread binary and the fixture store — no
// editor involved. KGREAD names the binary; without it, one is built from ../../src.

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import { createRequire } from "node:module";
import * as os from "node:os";
import * as path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const { Reader } = require("../dist/src/reader.js");

const here = path.dirname(fileURLToPath(import.meta.url));
const fixture = path.join(here, "fixture");

function readerBinary() {
  if (process.env.KGREAD) {
    return process.env.KGREAD;
  }
  const out = path.join(os.tmpdir(), `kgread-test-${process.pid}${process.platform === "win32" ? ".exe" : ""}`);
  execFileSync("go", ["build", "-o", out, "./cmd/kgread"], { cwd: path.join(here, "..", "..", "..", "src"), stdio: "inherit" });
  return out;
}

const exe = readerBinary();

/** A private copy of the fixture project, so a test may append to its log. */
function project() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "kgai-vscode-"));
  fs.cpSync(fixture, dir, { recursive: true });
  return dir;
}

// The machine's own configuration must not decide which store the tests see.
process.env.KGAI_STORE = "";
process.env.KGAI_PROJECT = "";
process.env.KGAI_HOME = fs.mkdtempSync(path.join(os.tmpdir(), "kgai-home-"));

test("answers about the fixture store", async () => {
  const rd = new Reader(exe, project());
  await rd.start();
  try {
    const st = await rd.status();
    assert.equal(st.error, undefined);
    assert.deepEqual(st.counts, { decisions: 4, elements: 2, links: 1, conflicts: 1, people: 2, installs: 1 });
    assert.equal(st.rule, "default location");

    const notes = await rd.decisions({ state: "note" });
    assert.equal(notes.shown, 1);
    assert.equal(notes.rows[0].title, "Considered PDF-only invoices, rejected");

    const all = await rd.decisions({});
    assert.equal(all.shown, 4);
    const d = await rd.decision(all.rows[1].id);
    assert.equal(d.title, "Draft invoices stay visible");
    assert.equal(d.state, "head");
    assert.equal(d.elements[0].role, "governs");
    assert.equal(d.supersedes.length, 1);

    const els = await rd.elements("", "inv");
    assert.equal(els.shown, 1);
    assert.equal(els.total, 2);
    const invoice = await rd.element(els.groups[0].elements[0].id);
    assert.equal(invoice.name, "Invoice");
    assert.equal(invoice.heads, 2);
    assert.equal(invoice.history.length, 4);

    const conflicts = await rd.conflicts();
    assert.equal(conflicts.length, 1);
    assert.equal(conflicts[0].heads.length, 2);

    const graph = await rd.graph();
    assert.deepEqual(
      graph.nodes.map((n) => [n.name, n.decisions, n.heads]),
      [
        ["Invoice", 4, 2],
        ["Pricing", 1, 0],
      ],
    );
    assert.deepEqual(graph.links, [{ from: graph.nodes[0].id, to: graph.nodes[1].id, kind: "PART_OF" }]);
    assert.equal(graph.decisions.length, 4);
    assert.deepEqual(graph.kinds, [{ key: "feature", n: 2 }]);

    const people = await rd.people("actor");
    assert.deepEqual(
      people.map((p) => p.name),
      ["alice", "bob"],
    );
    const bob = await rd.person("bob", "actor");
    assert.equal(bob.share, 50);
    assert.equal(bob.recent.length, 2);

    const filters = await rd.filters();
    assert.equal(filters.states.length, 3);
    const overview = await rd.overview();
    assert.equal(overview.mostShaped[0].name, "Invoice");

    await assert.rejects(rd.decision("d_nothing"), /no decision/);
    const bad = await rd.decisions({ text: "(", regex: true });
    assert.match(bad.error, /not valid/);
  } finally {
    rd.dispose();
  }
});

test("reports a change of the log and answers from the new state", async () => {
  const dir = project();
  const rd = new Reader(exe, dir, () => {}, ["--watch", "50ms"]);
  await rd.start();
  try {
    assert.equal((await rd.status()).counts.decisions, 4);
    const changed = new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error("no changed notification within 10s")), 10000);
      rd.once("changed", (st) => {
        clearTimeout(t);
        resolve(st);
      });
    });
    fs.appendFileSync(path.join(dir, ".kgai", "store", "log", "i-alice.ndjson"), fs.readFileSync(path.join(fixture, "more.ndjson")));
    const st = await changed;
    assert.equal(st.counts.decisions, 5);
    assert.equal((await rd.decisions({})).shown, 5);
  } finally {
    rd.dispose();
  }
});

test("opens another folder", async () => {
  const empty = fs.mkdtempSync(path.join(os.tmpdir(), "kgai-empty-"));
  const rd = new Reader(exe, empty);
  await rd.start();
  try {
    const st = await rd.status();
    assert.match(st.hint, /No knowledge graph here yet/);
    const opened = await rd.open(project());
    assert.equal(opened.error, undefined);
    assert.equal(opened.counts.decisions, 4);
  } finally {
    rd.dispose();
  }
});

test("fails to start when the binary is missing", async () => {
  const rd = new Reader(path.join(os.tmpdir(), "no-such-kgread"), project());
  await assert.rejects(rd.start());
  await assert.rejects(rd.status(), /not running|ENOENT/);
});
