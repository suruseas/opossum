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
It may run to several lines. **Write it in English** — the fragment is published
into `CHANGELOG.md` verbatim, and that file is read by people who don't share the
language the work around it is discussed in. A check refuses a fragment that isn't.

```markdown
- A `ports:` entry that names only a container port no longer fails when the
  matching host port is taken: opossum publishes on a free one and says which.
```

Then run `make changelog` so `CHANGELOG.md`'s `[Unreleased]` reflects it. A test
checks the two agree, so a forgotten regeneration fails the build rather than
drifting quietly.

## When not to add one

If the change isn't visible to someone using opossum — tests, internal
refactoring, docs — there's no entry to write. That's the same bar the changelog
has always had. Changes to this changelog process itself are the canonical
example: nobody upgrading opossum can see them, so they get no entry.

## Releasing

`go run ./cmd/changelog release X.Y.Z` folds the fragments into a
`## [X.Y.Z] - <date>` section and deletes them. Releases are cut by a human.
