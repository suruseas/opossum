#!/bin/bash
# Sync this repository's tree into the public repository checkout as a single
# commit, excluding content that is maintained separately.
#
# Usage:
#   scripts/sync-public.sh <version> <path-to-public-checkout> [ref]
#
#   <version>  label for the sync commit (e.g. v0.6.2 — the tag is then cut
#              on the public repo as usual)
#   [ref]      lab ref to export (default: HEAD)
#
# What it does:
#   1. Exports the tree at [ref] (git archive — never touches your worktree)
#   2. Removes excluded paths (articles/: external post drafts; they must not
#      appear in the public repo before the posts go live)
#   3. Mirrors the result into the public checkout (rsync --delete)
#   4. Creates a single commit named "opossum <version>"
#   5. Leaves `git push` (and tagging) to you — review the diff first
set -euo pipefail

VERSION="${1:?usage: scripts/sync-public.sh <version> <path-to-public-checkout> [ref]}"
DEST="${2:?usage: scripts/sync-public.sh <version> <path-to-public-checkout> [ref]}"
REF="${3:-HEAD}"

git rev-parse -q --verify "$REF^{commit}" >/dev/null || {
  echo "error: ref '$REF' not found in this repository" >&2; exit 1; }
git -C "$DEST" rev-parse --is-inside-work-tree >/dev/null || {
  echo "error: '$DEST' is not a git checkout" >&2; exit 1; }

SRC=$(mktemp -d)
trap 'rm -rf "$SRC"' EXIT
git archive "$REF" | tar -x -C "$SRC"

# --- exclusions -------------------------------------------------------------
# External post drafts live in articles/ and are published on their own
# platforms; never ship them with a release sync.
rm -rf "$SRC/articles"
# -----------------------------------------------------------------------------

rsync -a --delete --exclude ".git" "$SRC"/ "$DEST"/

# Guard: fail loudly if an excluded path somehow survived.
if [ -e "$DEST/articles" ]; then
  echo "error: articles/ still present in the public checkout after sync" >&2
  exit 1
fi

cd "$DEST"
git add -A
if git diff --cached --quiet; then
  echo "public checkout already matches $REF — nothing to commit"
  exit 0
fi
git commit -m "opossum $VERSION"
echo
git show --stat --oneline HEAD | head -20
echo
echo "Committed. Review above, then publish with:"
echo "  cd $DEST && git push"
