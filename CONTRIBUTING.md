# Contributing

## Recording a change

opossum's changelog is assembled from one file per change, not by editing a
shared section. If your change is visible to someone using opossum, add a
fragment in the same pull request:

```
changelog.d/<PR or issue number>-<short-slug>.<type>.md
```

`<type>` is one of `added`, `changed`, `deprecated`, `removed`, `fixed`,
`security`. The body is the entry as it will read in the changelog — one bullet
starting with `- `, written for the person upgrading:

```markdown
- `opossum doctor` now reports how much disk the runtime could reclaim, so a
  build that fails on space has somewhere to look first.
```

Then run `make changelog`, which regenerates `CHANGELOG.md`'s `[Unreleased]`
section from the fragments.

**Don't edit `CHANGELOG.md` by hand.** `[Unreleased]` is generated, and the
released sections are the published record. A test checks that the file agrees
with `changelog.d/`, so a hand edit or a forgotten `make changelog` fails the
build instead of drifting.

Changes nobody upgrading would notice — tests, refactoring, docs — need no
fragment. CI only asks for one when shipped code under `cmd/` or `internal/`
changed; if such a change genuinely has nothing to announce, put
`[skip changelog]` in the pull request body.

See [`changelog.d/README.md`](changelog.d/README.md) for the format in detail.

## Releasing

Releases are cut by a human:

```sh
go run ./cmd/changelog release X.Y.Z   # folds fragments into a version section
```

This writes `## [X.Y.Z] - <date>` into `CHANGELOG.md`, adds the version's
link-reference definition at the foot and re-points `[Unreleased]` at the new tag,
then deletes the fragments it consumed. The assembled section is byte-identical in
shape to the ones written by hand before this workflow existed — a test rebuilds a
published section from fragments to keep it that way.

Two shapes the assembler deliberately cannot produce, both of which exist in older
sections: prose outside bullets, and headings other than the six Keep a Changelog
types. Entries are bullets under those six sections; anything else belongs in the
release notes or the docs.

## Before pushing

```sh
gofmt -l .        # must be empty
go vet ./...
go test ./...     # the regression gate
```

Behaviour-changing pull requests get an independent review before merge, and the
review's findings are recorded on the PR.
