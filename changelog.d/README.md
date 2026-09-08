# changelog.d

One file per change, instead of everyone editing `CHANGELOG.md`.

Editing a shared `## [Unreleased]` section means every branch touches the same
lines: they conflict on merge, and when one branch is released before another
lands, the late entry ends up inside an already-published version. Adding a file
nobody else touches has neither problem.

## Adding an entry

Create `changelog.d/<number>-<slug>.<type>.md`, where `<number>` is the PR or
issue it belongs to:

```
changelog.d/318-suggestion-entries.added.md
```

`<type>` is one of `added`, `changed`, `deprecated`, `removed`, `fixed`,
`security` — the Keep a Changelog sections.

The body is the entry itself, exactly as it would read in the changelog: one
bullet starting with `- `, written for the person upgrading, not for the reviewer.
It may run to several lines.

A file carries one type, so a change with something to say under two of them
needs two files. This is easy to get wrong when a fix improves something on the
way past: a message that stopped printing a password and started naming the field
is a `security` entry and a `fixed` entry, and writing both into the `security`
file publishes the second one under Security. Somebody reading that section to
decide whether they have a credential to rotate then reads about wording. It has
happened; both entries were moved after release. **Write it in English** — the fragment is published
into `CHANGELOG.md` verbatim, and that file is read by people who don't share the
language the work around it is discussed in. A check refuses a fragment that isn't.

```markdown
- A `ports:` entry that names only a container port no longer fails when the
  matching host port is taken: opossum publishes on a free one and says which.
```

Then run `make changelog` so `CHANGELOG.md`'s `[Unreleased]` reflects it. A test
checks the two agree, so a forgotten regeneration fails the build rather than
drifting quietly.

## If a test says your entry states something unmeasured

A check reads these fragments and reports a sentence that says a container did
something — a host path works at this mount, an image starts and leaves a
directory alone, something answered on the far side of a mount, a round trip was
measured — with nothing saying where that was seen. It looks like this:

```
485-postgres18-layout.added.md: "…a host path works there…" says "a host path
works" without naming the run it came from
```

There are three ways to answer it, and they are all short:

- **Name the run.** A file under `testdata/` by path, or by name if it is a
  `.txt` or `.json` kept there: `…a host path works there (see
  pg18-mount-one-level-up.txt)`.
- **Say where you saw it.** A changelog is read by people who do not have this
  repository, so a version or a runtime works as well as a file does: `…a host
  path works there, measured on Postgres 18`.
- **Say you did not measure it.** `…whether it works on 19 is not measured`.

What is not an answer is rewording the sentence until the check goes quiet. If
the claim is real, one of the three above costs a handful of words; if it is not,
that is what the check is for.

The check reads only phrases it knows, so plenty of unmeasured claims will get
past it. A green run means nothing it recognises was unsupported — not that
everything in the entry was measured.

## When not to add one

If the change isn't visible to someone using opossum — tests, internal
refactoring, docs — there's no entry to write. That's the same bar the changelog
has always had. Changes to this changelog process itself are the canonical
example: nobody upgrading opossum can see them, so they get no entry.

## Releasing

`go run ./cmd/changelog release X.Y.Z` folds the fragments into a
`## [X.Y.Z] - <date>` section and deletes them. Releases are cut by a human.

One thing the tool does not do: on the very first release of a changelog — one
with no published `## [x.y.z]` section yet — it drops the link-definition block
at the end of the file and writes no link for the new version. Write that block
by hand once (`[Unreleased]: …/compare/vX.Y.Z...HEAD` and `[X.Y.Z]: …/releases/tag/vX.Y.Z`);
every later release carries it forward. This repository is long past that point.

Before folding, hold each fragment up against the version people are upgrading
from — the section below `[Unreleased]` in `CHANGELOG.md`. A fragment says what
changed; what a reader can see is the difference from there. To read forward from
that point, find the commit that wrote the section:

```sh
git log --oneline -S'## [0.24.1]' -- CHANGELOG.md | tail -1
```

Searching for the heading rather than taking the newest commit that touched the
file: every fragment added since then touched it too, by way of `make changelog`.
The fragments are written against whatever main held on the day, not against what
shipped.

One cycle produced three sentences that failed this, in two shapes.

Two described something that was made and unmade inside the cycle. One took away
a special case that had been added a few commits earlier, so nothing about it had
ever been released — that one got out, and is in `## [0.24.1]`. The other said a
count no longer misread `1 things`, where the version below had never printed it
wrong; that one was caught. Neither has a second fragment to contradict: the
fragment is alone, and true on the day it was written.

The third said a warning was unchanged while another fragment in the same set
described changing it. That is the only pair that cancels, and the only one of the
three that reading the fragments side by side would have found.

No test asks this question. The ones nearest to it check that `[Unreleased]`
matches the fragments, that a fragment is well formed and written in English, that
a sentence reporting a measurement names its run, and that a fragment's entry is
not already sitting in a published section — that last one reads the sections
below, but for the same words appearing twice, not for whether a change can be
seen from down there. `CONTRIBUTING.md` has the mechanics of cutting a release;
this is the reading to do before running them.
