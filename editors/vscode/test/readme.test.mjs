import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// The README is what the Marketplace and Open VSX show; vsce turns its relative image
// paths into GitHub URLs under editors/vscode/, so each one must be a file that lives there.
const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const readme = readFileSync(join(root, "README.md"), "utf8");

test("every README picture exists next to the manifest", () => {
  const images = [...readme.matchAll(/!\[[^\]]*\]\(([^)\s]+)\)/g)].map((m) => m[1]);
  assert.ok(images.length >= 8, `expected the gallery, found ${images.length} pictures`);
  for (const img of images) {
    assert.ok(!/^https?:/.test(img), `${img}: keep pictures relative so vsce rewrites them`);
    assert.ok(!img.startsWith("../") && !img.startsWith("/"), `${img}: must stay inside editors/vscode`);
    assert.ok(existsSync(join(root, img)), `${img}: file is missing`);
  }
});
