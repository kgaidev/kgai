// Bundles the extension (vscode stays external), the protocol client on its own (the
// plain node tests load it without an editor), and the editor test suite.
import * as esbuild from "esbuild";

const watch = process.argv.includes("--watch");
const ctx = await esbuild.context({
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
if (watch) {
  await ctx.watch();
} else {
  await ctx.rebuild();
  await ctx.dispose();
}
