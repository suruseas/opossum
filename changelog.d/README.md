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
directory alone — with nothing saying where that was seen. It looks like this:

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
