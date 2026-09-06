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

Releases are cut by a human. Before running the command below, read the fragments
against the version people are upgrading from —
[`changelog.d/README.md`](changelog.d/README.md#releasing) says what that catches,
and why no test does:

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

What an entry may hold follows from that. It starts with `- `, and every line
after the first is indented by at least two spaces — spaces, not a tab — or is
blank; a blank line is a paragraph break inside the entry. The left margin is
where the changelog's own structure lives, so the only thing that may go there is
`- `, starting the next entry — a fragment written with two of them is published,
and read back, as two. Anything else at the margin is refused, a `### ` heading
above all: published as written, it would open a section of its own in the
release. Whatever else Markdown allows is fine as long as it is indented: nested
lists, quotes, tables, and fenced code all survive a release unchanged. Entries
are written in English, inside a code fence as much as outside one.

## Before pushing

```sh
gofmt -l .        # must be empty
go vet ./...
make test         # the regression gate: the exact command CI runs
```

Behaviour-changing pull requests get an independent review before merge, and the
review's findings are recorded on the PR.

### The push runs the gate again, somewhere clean

The pre-push hook feeds a bundle of `HEAD` into a fresh Linux container and
runs `make test` there (`make sieve` runs the same thing by hand). A container
that did not exist a minute ago has an empty `$HOME`, an empty `/tmp` and no
processes left over from anything — so the class of failure that only shows on
a clean machine shows up here, before the push. The tree goes in as committed:
an uncommitted fix in your working tree cannot make the sieve green for a push
that does not include it, and nothing a test writes survives the container.

The hook is wired per clone — `make hooks` sets `core.hooksPath` once, and
that is the whole of it — and a clone where nobody has is the quiet kind of
gap: its pushes go through with no gate and no message, which from outside
looks exactly like a sieve that passed. So `make test` says so, first thing,
whenever the clone it runs in is not wired. It says it and then runs the gate;
the line is there to be seen, not to stop anything.

The sieve needs the Docker daemon running; the hook refuses, and says so,
when it is not. What the sieve does not run is the current Go release — it
uses the version go.mod asks for, and the second compiler is half of what the
pull request's CI run exists to check.

### When CI runs, and what it is for

CI runs when a pull request stops being a draft — opening it ready, or
marking it ready for review — and again on any push made while it is ready,
so the green always describes the head that would merge. That is the run to
point at: a green that someone other than whoever ran the tests can verify.
Pushes to a draft cost nothing and are covered by the sieve; push fixes as a
draft, and treat a push while ready as deliberately spending a run to keep
the attestation current.

Before merging, compare the CI run's commit with the pull request's head all
the same — they must be the same commit, or the green belongs to a tree that
is not the one merging.

There is no CI on pushes to main. A release runs the workflow once by hand
(`gh workflow run ci.yml --ref main`) before its tag, which covers everything
that reached main since the last run.

### A commit the tree cannot be built from

Reading the gate and making the commit are two acts, and nothing joins them: a
tree that does not compile can be committed and pushed, and the first thing that
notices is CI, minutes later — or a reader who takes the red for the answer to
whatever they were measuring at the time.

There is a hook for the half of that which answers in about a second:

```sh
make hooks    # git config core.hooksPath .githooks
```

It runs `gofmt -l .` and `go vet` for both platforms, and refuses the commit if
either has something to say. `git commit --no-verify` still goes through, which
is the point — a hook long enough to be worth skipping is a hook that gets
skipped.

What it does not catch, so that a green commit is not read as more than it is:

- **The suite.** `go test ./...` is minutes; a commit hook that takes minutes
  teaches people to pass `--no-verify` by habit, and then nothing is checked at
  all. So a commit whose tests fail goes through.
- **Half of a change.** The hook reads the working tree; git records the index.
  Stage part of your work and the rest is still on disk, so a commit that does
  not build on its own passes without a word. Splitting a change across commits
  is exactly this shape.
- **Merges that go through on their own.** `git merge` runs `pre-merge-commit`,
  not this, so a merge whose result does not build lands unremarked. Rebase
  replays the same way. (A merge you finish by hand after a conflict ends in a
  `git commit`, and that one does come here.)
- **Anything but the tree.** It reads `.` — including files git is not tracking.
  A scratch `.go` file that does not compile will stop every commit until it is
  moved out or deleted.

`core.hooksPath` replaces `.git/hooks` rather than adding to it: anything already
there stops running. The same goes for a `core.hooksPath` set globally — `make
hooks` sets the clone's own, which wins, so a hooks directory of your own stops
running in this clone (and was not running this repository's sieve before). Nothing is there in this repository today (only the samples
git ships), but a tool that installs its own hooks later — `git lfs install`, for
one — would go quiet.

### A run that ends before it can clean up

The suites build their binaries under `$TMPDIR` and remove them when they
finish, and `cmd/opossum` looks for processes the tests left running before it
returns. Both of those live after `m.Run()` — so a panic, a `-timeout` firing,
or a `^C` skips them. A run that dies that way leaves a directory behind and,
if a test had started a supervisor, a process still running out of it. The
in-suite check is the one thing that could have named that process, and on this
path it never speaks.

`make test` therefore runs the suite through a second look from the outside —
in two steps, as the Makefile writes them (`$$` is make's spelling of `$`):

```sh
go run ./cmd/noleftovers go test $$(go list ./... | grep -v '/cmd/busy$$') -race -cover -count=1
go run ./cmd/noleftovers go test ./cmd/busy -race -cover -count=1
```

cmd/busy runs last, alone, because it measures how much CPU the machine can
give and `go test ./...` runs packages in parallel — beside its neighbours,
the measuring package was measuring them. The second line starts on a machine
the first has just finished with.

`-count=1` keeps the gate from answering out of Go's test cache. The cache is
sound about code, but a test that fails on environment passes by not running,
and a cached `ok` reads exactly like one that ran. On the gate, "green" means
"ran, just now, and passed" — the cache is welcome everywhere else.

Out here the test binary's death is only an exit status. Before and after the
command it takes the set of `opossum-*` entries in `$TMPDIR`, together with the
directories `t.TempDir()` makes there (`Test`, the test's name, digits), and
reports anything new, names
any process still running out of it, and fails a run that would otherwise have
passed. It survives `^C` on purpose — the signal reaches the whole process
group, so without that it would be killed alongside the thing it is watching.
A command a signal killed is reported as killed rather than as having exited,
and the run's status becomes 128 plus the signal, as a shell would write it.

A command that catches `^C` for itself is reported as interrupted too. `go test`
does exactly that: it handles the signal, tidies up, and exits 1 — the same 1 a
run whose tests failed would give. Read from that status alone the run looks
like a failure, so the interrupt is reported from this side, where it was also
received. That is said whether or not anything was left behind, and an
interrupted run is never green: a command that chose 0 after catching `^C`
comes back as 1.

To run the suite without the second look — while bisecting, say — call `go test`
directly.

`pgrep` has to be on PATH. The tool itself does without it — it says it could not
look rather than claiming there was nothing to find — but the tests that check
the `^C` path use it twice: to wait until there is something an interrupt can
reach, and afterwards to see whether anything survived it. The wait fails the
test outright when pgrep is missing, and again when it can be run but never sees
the process — which is what keeps the second use from quietly passing on a
machine that cannot answer. A run that skipped these tests
would still print `ok`, which is the worse of the two.

What it does not catch:

- **Which test leaked.** Out here a directory is new or it is not; nothing
  connects it to a name. That is what the in-suite check is for, and why this is
  a second net rather than a replacement.
- **Anything that was already there.** The difference starts when the command
  does, so a leak from an earlier run is invisible. It also means that running
  again straight after a red one goes green while the leak is still sitting
  there — which is the first thing most people try.
- **Whose a new directory is.** The difference is in time, not in ownership: a
  suite started in another terminal while this one runs appears here too and
  cannot be told apart from a leak. The report says so, and does not hand over
  an unconditional `rm -rf`. Check that nothing is using a directory before
  removing it.
- **Anything named neither `opossum-*` nor the way `t.TempDir()` names
  things** (`Test`, the test's name as Go writes it, digits). `internal/orchestrator`
  makes one temp directory with the prefix `sk`, because a Unix socket path has
  to stay short. No pattern that matches it in a shared `$TMPDIR` would leave
  strangers alone, so a run that leaks only that one passes. So does a run
  leaking a `Benchmark`'s or a `Fuzz` target's `t.TempDir()`; this tree has none.
- **Whose a `t.TempDir()` is.** Its name holds no pid, so any other `go test`
  running at the same time — this repository's own, in another terminal,
  included — leaves one that reads exactly like ours and turns a green run red.
  Nothing runs inside a `t.TempDir()`, so the "still running out of" look has
  nothing to find there either.
- **A leak a concurrent run tidies up.** The difference is taken across the
  command, so something that appears and disappears while it runs is never seen.
- **Any way of being killed but `^C`.** Only an interrupt is survived. `SIGTERM`
  (from `timeout`, a cancelled CI job, a harness) and `SIGHUP` (closing the
  terminal on a run that takes minutes) kill it as silently as they killed the
  suite, and those endings are the same kind. Catching `SIGTERM` would stop
  `kill <pid>` working on it, which is worse.
- **Leftovers below the top of `$TMPDIR`**, or inside somebody else's directory.
- **The difference between a directory and a file.** `cmd/mutate` writes
  `opossum-mutate-*.cover` at the top of `$TMPDIR`, so an interrupted sweep can
  put a file under a heading that talks about directories.
- **Whether the process it names is ours.** `pgrep -f` is handed the whole
  directory path, but reads it as a regular expression rather than as text.
- **The exit status, by the time `make` sees it.** The tool returns the
  command's own status, except that a run which left something behind is never
  green, and neither is an interrupted one — a command that succeeded after
  leaking, or that chose 0 after catching `^C`, comes back as 1. `make test`
  reaches it through `go run`, which collapses every non-zero status to 1. The
  number is printed as well for that reason — but only when something was left
  or the run was interrupted, since those are the only times it prints at all.
  `make` only tells zero from non-zero, so the gate is unaffected — a script
  reading `make test`'s status is not.
- **`make cover` and the commit hook**, which do not go through it. CI does:
  its test job runs `make test` itself, so the runner — where a leaked
  directory is thrown away with the machine and nobody ever sees it — gets the
  same look from outside as a local run.

### Making the machine busy on purpose

Some tests only fail when things are slow, and the way to find out is to load the
machine while they run:

```sh
go build -o /tmp/busy ./cmd/busy
/tmp/busy -n 4 -for 5m -- go test -count=20 -run TestSomethingTiming ./cmd/opossum/
```

Build it rather than `go run ./cmd/busy`. `go run` is a second process holding
the first: kill it — a `^C` that lands there, a harness that reaps it — and the
loaded process is orphaned, measured at 399% of CPU with a ppid of 1. That is the
accident this exists to prevent, reintroduced one layer up. `go run` also
collapses the command's exit status to 1.

Write the loop by hand and you get the same accident, twice recorded: once a
review's shell spawned two dozen spinners and died before its last line, leaving
forty orphans burning 600% of a laptop for five hours; once a flake sweep did the
same and took a machine somebody was using to a load average of 157. Both times
the cleanup line was correct and simply never ran — `kill $(jobs -p)` finds
nothing to kill in a non-interactive shell, and a killed parent never reaches its
last line at all.

`busy` keeps the load inside its own process, so there is nothing to orphan.
Measured by killing the parent mid-run and counting what is left in its process
group:

```
by hand   : 6 processes before → 5 after
cmd/busy  : 2 processes before → 1 after   (the command, which is next)
```

`-for` bounds the load in case the tool is left running rather than killed; it
stops the load, not the command, and says so when it fires. It defaults to five
minutes, which is shorter than most sweeps — pass a longer one deliberately
rather than discovering the load stopped halfway.

**On a machine someone is using, ask for fewer workers than it has cores.** The
default is every core, which is right for a machine you are only measuring on and
wrong for one anybody is typing into.

What it does not do:

- **Keep the command from being orphaned.** Kill `busy` and the command it
  started keeps running — the same limit `cmd/noleftovers` has. Only the load is
  guaranteed to go.
- **Survive being run under something else.** The guarantee is about what `busy`
  starts, not about what starts `busy`: anything that can be killed between you
  and it — `go run`, a wrapper script — is a parent that can leave it behind.
- **Promise a particular amount of contention.** `-n` asks for that many
  spinning threads; the hardware answers. Eight workers on a two-core runner burn
  two cores' worth, not eight. How much each one gets is the scheduler's
  business, and on a machine with other work it is not fixed.
- **Know whether the load is the kind your race needs.** It makes the machine
  busy. Whether that is enough is a question for whoever is hunting.

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

Beside a survivor you may see a note saying that no test the sweep ran appears to
reach what it changed, or that the reach could not be measured. It comes from the
coverage of the unmutated run: the tests the sweep ran are the ones in the
packages it named, which is less than every test there is, so read the note as a
place to start rather than a verdict. A survivor is a survivor either way — the
work is to write the test — and where the note says nothing could be measured, it
is saying only that, not that anything runs the line.
