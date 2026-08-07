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

## Showing that a test guards what it claims

A green suite says the code passes its tests. It does not say the tests would
notice if the code stopped working — and that gap is where most of the defects
found in review have lived: an assertion that holds whatever the code does, a
helper nobody calls any more, a wait removed while everything still passes.

The way to tell is to break the thing on purpose and see which test says so.
`cmd/mutate` does the bookkeeping:

```sh
cat > /tmp/sweep.json <<'JSON'
[
  {
    "name": "the second look never happens",
    "file": "internal/orchestrator/orchestrator.go",
    "from": "if len(watching) == 0 || look > 0 {",
    "to":   "if true {",
    "packages": ["./internal/orchestrator/"]
  }
]
JSON
go run ./cmd/mutate /tmp/sweep.json
```

It prints a markdown table naming the tests that failed, which goes in the pull
request. Each mutation is one an author chose to express a specific worry, not
one a tool generated: the question is "is *this* guarded, and by what", not "what
fraction of the code is covered".

What it checks rather than assumes, each of which produced a confident and false
claim when the same job was done by hand:

- the pattern matches **exactly once**, and changes something — a replacement
  that matches nothing changes nothing, and the resulting green run reads as "the
  mutation survived"
- the tree still **builds** — a mutation that doesn't compile runs no tests, so a
  red package proves nothing
- the failing tests are collected **by name**, from `go test -json` — "the package
  went red" can be someone else's test, and a test that prints go test's own
  output is not a test that failed
- a run that ends red **without naming anyone** — a panic, a timeout — is reported
  as inconclusive rather than as a survivor
- the file is **restored and compared byte for byte** afterwards, including when
  the run is interrupted. It never runs `git checkout`, which would throw away the
  uncommitted work the sweep is usually there to test

A mutation nothing catches is the finding worth having: it says so in the table
and exits 1. A sweep that could not run exits 2 — "it found something" and "it
never ran" must not look alike.
