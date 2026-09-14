#!/usr/bin/env bash
# build-host-pkg.sh — assemble a self-contained kgai package for a non-Claude host.
#
#   bash scripts/build-host-pkg.sh              # both, into dist/
#   bash scripts/build-host-pkg.sh gemini       # just one
#   bash scripts/build-host-pkg.sh codex --out /tmp/x
#
# Why a build step at all. The Claude Code plugin IS this repository — its manifest sits
# in .claude-plugin/ and everything it needs is already at the root. The other two hosts
# COPY the directory they are pointed at (Gemini on `extensions install`, Codex on
# `plugin add`), so each needs its own root: its own manifest, its own hooks.json wired
# to its own event and tool names, and its own copy of the shared scripts. Assembling
# that here keeps one source of truth — the scripts and the skill are never duplicated in
# git, only in the artifact.
#
# What is shared, and what is per host:
#   shared   hooks/*.sh, scripts/install.sh + fetch-libs.sh, skills/, the command bodies
#   codex    hosts/codex/plugin.json + hooks.json   (Agent Plugins manifest, Codex events)
#   gemini   hosts/gemini/*                          (extension manifest, Gemini events,
#                                                     commands as TOML, GEMINI.md)
set -uo pipefail
REPO="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
OUT="$REPO/dist"
HOSTS=""

while [ $# -gt 0 ]; do
  case "$1" in
    gemini|codex) HOSTS="$HOSTS $1" ;;
    all) HOSTS="codex gemini" ;;
    --out) shift; OUT="${1:?--out needs a directory}" ;;
    -h|--help) sed -n '2,12p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "build-host-pkg.sh: unknown argument '$1'" >&2; exit 2 ;;
  esac
  shift
done
[ -n "$HOSTS" ] || HOSTS="codex gemini"

die() { echo "build-host-pkg.sh: $*" >&2; exit 1; }

# Every manifest in the artifact must agree with the version the plugin actually ships,
# or a host will report one number while the engine reports another.
VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$REPO/.claude-plugin/plugin.json" | head -n1)"
[ -n "$VERSION" ] || die "could not read the version from .claude-plugin/plugin.json"

copy_shared() { # <pkg dir>
  local pkg="$1"
  mkdir -p "$pkg/hooks" "$pkg/scripts" "$pkg/skills"
  cp "$REPO"/hooks/*.sh "$pkg/hooks/"
  cp "$REPO"/scripts/install.sh "$REPO"/scripts/fetch-libs.sh "$pkg/scripts/"
  cp -R "$REPO"/skills/. "$pkg/skills/"
  cp "$REPO/LICENSE" "$pkg/LICENSE" 2>/dev/null
  chmod +x "$pkg"/hooks/*.sh "$pkg"/scripts/*.sh 2>/dev/null
}

# `kg-ask.md` + its `description:` frontmatter → `kg-ask.toml` with `description` and
# `prompt`. The body is copied verbatim except for the argument placeholder, which every
# host spells differently.
commands_to_toml() { # <dest dir>
  local dest="$1"
  mkdir -p "$dest"
  python3 - "$REPO/commands" "$dest" <<'PY'
import os, sys, re

src, dest = sys.argv[1], sys.argv[2]
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
    # Gemini interpolates {{args}}; Claude and Codex spell the same thing $ARGUMENTS.
    body = text.replace("$ARGUMENTS", "{{args}}").strip()

    def toml_basic(s):
        return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'

    # A multi-line basic string ends at the first `"""`, and these bodies contain shell
    # and JSON, so the safest thing is to keep the delimiter out of the payload entirely.
    body = body.replace('"""', '\\"\\"\\"')
    with open(os.path.join(dest, name[:-3] + ".toml"), "w", encoding="utf-8") as fh:
        if desc:
            fh.write("description = %s\n" % toml_basic(desc))
        fh.write('prompt = """\n%s\n"""\n' % body)
    written += 1
print(written)
PY
}

check_json() { # <file>…
  python3 - "$@" <<'PY'
import json, sys
for p in sys.argv[1:]:
    try:
        json.load(open(p, encoding="utf-8"))
    except Exception as e:
        print("invalid JSON: %s: %s" % (p, e)); sys.exit(1)
PY
}

command -v python3 >/dev/null 2>&1 || die "python3 is required to build (manifest checks, command conversion)"

for host in $HOSTS; do
  pkg="$OUT/$host"
  rm -rf "$pkg"; mkdir -p "$pkg" || die "cannot create $pkg"
  copy_shared "$pkg"

  case "$host" in
    codex)
      sed "s/\"version\": \"[^\"]*\"/\"version\": \"$VERSION\"/" "$REPO/hosts/codex/plugin.json" > "$pkg/plugin.json"
      cp "$REPO/hosts/codex/hooks.json" "$pkg/hooks/hooks.json"
      # Codex reads Markdown commands, and migrates the argument-free ones into skills.
      mkdir -p "$pkg/commands"; cp "$REPO"/commands/*.md "$pkg/commands/"
      check_json "$pkg/plugin.json" "$pkg/hooks/hooks.json" || die "codex manifests failed the JSON check"
      ;;
    gemini)
      sed "s/\"version\": \"[^\"]*\"/\"version\": \"$VERSION\"/" "$REPO/hosts/gemini/gemini-extension.json" > "$pkg/gemini-extension.json"
      cp "$REPO/hosts/gemini/GEMINI.md" "$pkg/GEMINI.md"
      cp "$REPO/hosts/gemini/hooks/hooks.json" "$pkg/hooks/hooks.json"
      n="$(commands_to_toml "$pkg/commands")" || die "converting commands to TOML failed"
      [ "${n:-0}" -gt 0 ] || die "no commands were converted — commands/ is empty?"
      check_json "$pkg/gemini-extension.json" "$pkg/hooks/hooks.json" || die "gemini manifests failed the JSON check"
      ;;
  esac

  echo "built $host → $pkg ($VERSION)"
done
