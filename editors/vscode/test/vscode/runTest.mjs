// Runs the editor suite in a downloaded VS Code with the fixture project open.
// KGREAD names the reader binary; the suite's reader path is passed through the
// kgai.readerPath setting written into the fixture's .vscode/settings.json.
import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { runTests } from "@vscode/test-electron";

const here = path.dirname(fileURLToPath(import.meta.url));
const extensionDevelopmentPath = path.resolve(here, "..", "..");
const extensionTestsPath = path.resolve(extensionDevelopmentPath, "dist", "test", "vscode", "suite.js");

let kgread = process.env.KGREAD;
if (!kgread) {
  kgread = path.join(os.tmpdir(), `kgread-vscode-${process.pid}${process.platform === "win32" ? ".exe" : ""}`);
  execFileSync("go", ["build", "-o", kgread, "./cmd/kgread"], { cwd: path.resolve(extensionDevelopmentPath, "..", "..", "src"), stdio: "inherit" });
}

const project = fs.mkdtempSync(path.join(os.tmpdir(), "kgai-vscode-suite-"));
fs.cpSync(path.join(here, "..", "fixture"), project, { recursive: true });
fs.mkdirSync(path.join(project, ".vscode"), { recursive: true });
fs.writeFileSync(path.join(project, ".vscode", "settings.json"), JSON.stringify({ "kgai.readerPath": kgread }));

const home = fs.mkdtempSync(path.join(os.tmpdir(), "kgai-home-"));
// When this runs from a terminal inside VS Code, the environment tells Electron to be
// plain node — the downloaded VS Code would then try to run the workspace as a script.
delete process.env.ELECTRON_RUN_AS_NODE;
try {
  await runTests({
    extensionDevelopmentPath,
    extensionTestsPath,
    launchArgs: [project, "--disable-extensions", "--disable-workspace-trust"],
    extensionTestsEnv: { KGAI_STORE: "", KGAI_PROJECT: "", KGAI_HOME: home, KGAI_FIXTURE_MORE: path.join(here, "..", "fixture", "more.ndjson") },
  });
} catch (e) {
  console.error("editor tests failed:", e);
  process.exit(1);
}
