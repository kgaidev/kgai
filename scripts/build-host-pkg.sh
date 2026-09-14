#!/usr/bin/env bash
# build-host-pkg.sh — assemble the self-contained kgai extension package for Gemini CLI.
#
#   bash scripts/build-host-pkg.sh              # → dist/gemini/
#   bash scripts/build-host-pkg.sh --out /tmp/x
#
# Why only Gemini. Claude Code and Codex CLI both read `.claude-plugin` plugins and share
# the same lifecycle event names, so THIS repository installs on either as-is
# (`codex plugin marketplace add …` / Claude's plugin system) — no build needed, one
# hooks/hooks.json serves both. Gemini CLI is the one that differs: a different manifest
# (`gemini-extension.json`), commands as TOML not Markdown, a `GEMINI.md` context file,
# and hook timeouts in milliseconds. It also COPIES the directory it is pointed at on
# `gemini extensions install`, so it needs its own self-contained root. Assembling it here
# keeps one source of truth — the hook scripts, installer and skill live once in the repo
# and are copied into the artifact, never duplicated in git.
set -uo pipefail
REPO="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
OUT="$REPO/dist"

while [ $# -gt 0 ]; do
  case "$1" in
    gemini) ;;   # the only host; accepted for backward compatibility
    --out) shift; OUT="${1:?--out needs a directory}" ;;
    -h|--help) sed -n '2,10p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "build-host-pkg.sh: unknown argument '$1'" >&2; exit 2 ;;
  esac
  shift
done

die() { echo "build-host-pkg.sh: $*" >&2; exit 1; }
command -v python3 >/dev/null 2>&1 || die "python3 is required to build (manifest check, command conversion)"

# Every manifest in the artifact must claim the version the plugin actually ships, or a
# host reports one number while the engine reports another.
VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$REPO/.claude-plugin/plugin.json" | head -n1)"
[ -n "$VERSION" ] || die "could not read the version from .claude-plugin/plugin.json"

# `kg-ask.md` + its `description:` frontmatter → `kg-ask.toml` with `description` and
# `prompt`. The body is copied verbatim except for the argument placeholder: Gemini
# interpolates {{args}} where Claude/Codex spell it $ARGUMENTS.
commands_to_toml() { # <dest dir>
  python3 - "$REPO/commands" "$1" <<'PY'
import os, sys, re
src, dest = sys.argv[1], sys.argv[2]
os.makedirs(dest, exist_ok=True)
written = 0
for name in sorted(os.listdir(src)):
    if not name.endswith(".md"):
        continue
    text = open(os.path.join(src, name), encoding="utf-8").read()
    desc = ""
    m = re.match(r"\A---\n(.*?)\n---\n", text, re.S)
    if m:
        text = text[m.end():]
        d = re.search(r"^description:\s*(.+?)\s*$", m.group(1), re.M)
        if d:
            desc = d.group(1)
    body = text.replace("$ARGUMENTS", "{{args}}").strip()
    def toml_basic(s):
        return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'
    body = body.replace('"""', '\\"\\"\\"')   # keep the delimiter out of the payload
    with open(os.path.join(dest, name[:-3] + ".toml"), "w", encoding="utf-8") as fh:
        if desc:
            fh.write("description = %s\n" % toml_basic(desc))
        fh.write('prompt = """\n%s\n"""\n' % body)
    written += 1
print(written)
PY
}

check_json() {
  python3 - "$@" <<'PY'
import json, sys
for p in sys.argv[1:]:
    try:
        json.load(open(p, encoding="utf-8"))
    except Exception as e:
        print("invalid JSON: %s: %s" % (p, e)); sys.exit(1)
PY
}

pkg="$OUT/gemini"
rm -rf "$pkg"; mkdir -p "$pkg/hooks" "$pkg/scripts" "$pkg/skills" || die "cannot create $pkg"
cp "$REPO"/hooks/*.sh "$pkg/hooks/"
cp "$REPO"/scripts/install.sh "$REPO"/scripts/fetch-libs.sh "$pkg/scripts/"
cp -R "$REPO"/skills/. "$pkg/skills/"
cp "$REPO/LICENSE" "$pkg/LICENSE" 2>/dev/null
chmod +x "$pkg"/hooks/*.sh "$pkg"/scripts/*.sh 2>/dev/null

sed "s/\"version\": \"[^\"]*\"/\"version\": \"$VERSION\"/" "$REPO/hosts/gemini/gemini-extension.json" > "$pkg/gemini-extension.json"
cp "$REPO/hosts/gemini/GEMINI.md" "$pkg/GEMINI.md"
cp "$REPO/hosts/gemini/hooks/hooks.json" "$pkg/hooks/hooks.json"
n="$(commands_to_toml "$pkg/commands")" || die "converting commands to TOML failed"
[ "${n:-0}" -gt 0 ] || die "no commands were converted — commands/ is empty?"
check_json "$pkg/gemini-extension.json" "$pkg/hooks/hooks.json" || die "gemini manifests failed the JSON check"

echo "built gemini → $pkg ($VERSION)"
