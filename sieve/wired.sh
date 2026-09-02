#!/bin/sh
# Says so when this clone's push-time sieve is not wired.
#
# core.hooksPath is set per clone, once, by hand — and a clone where nobody set
# it pushes in silence: the pre-push hook never runs, the push succeeds, and
# from outside that is indistinguishable from a sieve that passed. The one
# place a developer looks every day is the top of `make test`, so that is
# where an unwired clone gets named. Named, not refused: the gate itself is
# still worth running here, and a check that stops the gate teaches people to
# skip the check.
#
# Quiet where the hook has no job: outside a git checkout, in CI (which
# pushes nothing), and in the sieve's own throwaway clone (sieve/run.sh wires
# it before running the gate).
set -u
[ -n "${CI:-}" ] && exit 0
top="$(git rev-parse --show-toplevel 2>/dev/null)" || exit 0
[ -f "$top/.githooks/pre-push" ] || exit 0
# Ask git where it will look for hooks, and compare directories rather than
# spellings: `core.hooksPath` may be relative, absolute, trailing-slashed, or
# set globally to somebody's own ~/.githooks — which is a directory called
# .githooks that runs their hooks, not this repository's. Two resolved paths
# are the same place or they are not.
want="$(cd "$top/.githooks" 2>/dev/null && pwd -P)"
have="$(cd "$top" && cd "$(git rev-parse --git-path hooks)" 2>/dev/null && pwd -P)"
[ "$have" = "$want" ] && exit 0
echo "make: this clone's pre-push sieve is not wired (core.hooksPath does not point at" >&2
echo "  this clone's .githooks), so a push from here runs no gate and says nothing" >&2
echo "  about it. Wire it once with:" >&2
echo "    make hooks" >&2
exit 0
