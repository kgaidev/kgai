#!/usr/bin/env bash
# publish-gemini.sh — regenerate the standalone Gemini CLI extension repo from this one and
# push it, so `gemini extensions install https://github.com/kgaidev/kgai-gemini` serves the
# current release.
#
# Gemini needs a repo whose ROOT is the extension (gemini-extension.json at top level),
# while this repo's root is the Claude/Codex .claude-plugin. So the Gemini extension lives
# in a SEPARATE repo that is a pure build output of this one — never edited by hand. Run
# this after cutting a release (the version comes from .claude-plugin/plugin.json).
#
#   bash scripts/publish-gemini.sh            # build, commit, push
#   bash scripts/publish-gemini.sh --dry-run  # build + show what would change, no push
#
# Assumes the git remote `kgai-gemini` (or the SSH alias below) can push to
# kgaidev/kgai-gemini. Override the target with KGAI_GEMINI_REMOTE.
set -euo pipefail
REPO="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)"
REMOTE="${KGAI_GEMINI_REMOTE:-git@github-kgai:kgaidev/kgai-gemini.git}"
DRY=""
[ "${1:-}" = "--dry-run" ] && DRY=1

VERSION="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$REPO/.claude-plugin/plugin.json" | head -n1)"
[ -n "$VERSION" ] || { echo "publish-gemini: could not read version" >&2; exit 1; }

WORK="$(mktemp -d)"; trap 'rm -rf "$WORK"' EXIT
echo "publish-gemini: cloning $REMOTE"
git clone -q "$REMOTE" "$WORK/repo"

# Rebuild the package and replace the repo's tracked content with it (keep .git, README,
# .gitignore — those are the repo's own, not build output).
bash "$REPO/scripts/build-host-pkg.sh" --out "$WORK/dist" >/dev/null
cd "$WORK/repo"
find . -mindepth 1 -maxdepth 1 ! -name .git ! -name README.md ! -name .gitignore -exec rm -rf {} +
cp -r "$WORK/dist/gemini/." .

git add -A
if git diff --cached --quiet; then
  echo "publish-gemini: already up to date ($VERSION), nothing to push"
  exit 0
fi
if [ -n "$DRY" ]; then
  echo "publish-gemini: --dry-run, would commit:"; git diff --cached --stat
  exit 0
fi
git -c user.name="kgai maintainers" -c user.email="team@kgai.dev" \
  commit -q -m "kgai Gemini CLI extension $VERSION (generated from kgaidev/kgai)"
git push -q origin HEAD
echo "publish-gemini: pushed $VERSION to $REMOTE"
