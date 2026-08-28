// Bundles the extension (vscode stays external), the protocol client and the page
// builders on their own (the plain node tests load them without an editor), the editor
// test suite — and, for the browser, the graph page's script.
import * as esbuild from "esbuild";

const watch = process.argv.includes("--watch");
const node = await esbuild.context({
  entryPoints: ["src/extension.ts", "src/reader.ts", "src/html.ts", "test/vscode/suite.ts"],
  outdir: "dist",
  outbase: ".",
  bundle: true,
  platform: "node",
  target: "node18",
  format: "cjs",
  external: ["vscode", "mocha"],
  sourcemap: true,
  minify: false,
  logLevel: "info",
});
const browser = await esbuild.context({
  entryPoints: ["src/webview/graph.ts"],
  outdir: "dist",
  outbase: ".",
  bundle: true,
  platform: "browser",
  target: "es2020",
  format: "iife",
  sourcemap: true,
  minify: false,
  logLevel: "info",
});
if (watch) {
  await Promise.all([node.watch(), browser.watch()]);
} else {
  await Promise.all([node.rebuild(), browser.rebuild()]);
  await Promise.all([node.dispose(), browser.dispose()]);
}
