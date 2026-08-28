// The detail panel's HTML must work under the webview's Content Security Policy,
// which allows only the nonced stylesheet: an inline style attribute is dropped
// silently. The charts once sized their bars that way and every bar came out at full
// width. Guard: bars carry no style attribute and are proportional to their counts.
import { test } from "node:test";
import assert from "node:assert/strict";
import * as html from "../dist/src/html.js";

test("bars are proportional without inline styles", () => {
  const out = html.bars([
    { key: "feature", n: 20 },
    { key: "component", n: 10 },
    { key: "concept", n: 1 },
  ]);
  assert.ok(!/style=/.test(out), "no inline style attribute (blocked by the webview CSP)");
  const widths = [...out.matchAll(/<rect[^>]*width="(\d+)%"/g)].map((m) => Number(m[1]));
  assert.deepEqual(widths, [100, 50, 5]);
  assert.match(out, /class="bar-n">20</);
});

test("the whole document keeps its CSP without unsafe-inline", () => {
  const doc = html.document(html.bars([{ key: "a", n: 1 }]), "t", "abc", "vscode-resource:", false);
  assert.match(doc, /style-src vscode-resource: 'nonce-abc'/);
  assert.ok(!/unsafe-inline/.test(doc));
  assert.ok(!/ style="/.test(doc), "no inline style anywhere in a rendered page");
});
