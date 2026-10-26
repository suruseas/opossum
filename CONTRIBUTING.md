# Contributing to opossum

Thanks for your interest in opossum. Bug reports, compose files that behave
differently than under docker compose, documentation fixes and code are all
welcome.

## Reporting a problem

Open an issue with:

- The versions involved: `opossum --version`, `container --version`, and your
  macOS version.
- The smallest compose file that shows the problem, and the command you ran.
- What happened (the output, trimmed to the relevant part) and what you
  expected. If docker compose behaves differently with the same file, say how.

`opossum doctor` checks the runtime and the host setup, and its output is often
worth including.

## Setting up

You need Go (the version in [`go.mod`](go.mod)), `make` and `git`. The test
suite runs against a fake `container` runtime, so it runs on Linux as well as
on macOS, and does not need Apple `container` installed.

```sh
git clone https://github.com/suruseas/opossum.git
cd opossum
make build   # builds ./opossum
make test    # the full test suite
```

To try a change against the fake runtime without touching a real one:

```sh
OPOSSUM_CONTAINER_BIN="$PWD/testdata/fake-container.sh" \
  go run ./cmd/opossum -f examples/compose.yaml up
```

To run against the real runtime, you need a Mac with Apple silicon and Apple
`container` installed and running. `make real-conformance` runs the
measurements that only the real runtime can answer.

`make hooks` (optional, once per clone) wires a commit hook that checks the tree
builds and is formatted, and a push hook that runs the test suite in a clean
Linux container (this one needs Docker).

## Making a change

- Keep a pull request to one change. A bug fix and an unrelated cleanup are
  easier to review as two.
- Add or change a test that fails without your change and passes with it. Most
  behaviour can be tested against the fake runtime; see the existing tests in
  `internal/orchestrator` and `cmd/opossum` for the patterns.
- If the change is about matching docker compose, say in the pull request what
  docker compose does with the same file, and how you checked it.
- Write code comments, commit messages and documentation in English.
- Run `make test` before opening the pull request. CI runs the same suite.

### Changelog fragments

If your change is something a user of opossum would notice, add a changelog
fragment in the same pull request. Don't edit `CHANGELOG.md` by hand.

1. Create `changelog.d/<number>-<short-slug>.<type>.md`, where `<number>` is the
   pull request or issue number and `<type>` is one of `added`, `changed`,
   `deprecated`, `removed`, `fixed` or `security`.
2. Write one entry starting with `- `, for someone upgrading:

   ```markdown
   - `opossum up` now names the service whose healthcheck timed out.
   ```

   Put names like `<service>` inside backticks; outside them they are read as
   HTML and disappear from the release page.
3. Run `make changelog`, which regenerates the `[Unreleased]` section of
   `CHANGELOG.md` from the fragments, and commit both.

Tests, refactoring and documentation-only changes need no fragment. CI asks for
one when code that ships changes; if such a change really has nothing to
announce, put `[skip changelog]` in the pull request description.

## Pull requests

- CI runs on every pull request. A first-time contributor's run waits for a
  maintainer to approve it.
- A maintainer reviews the change and merges it once CI is green. Pull requests
  are squash-merged, so the commits on your branch don't need tidying.
- If the change needs more than review comments can carry, we may follow up with
  changes of our own on top of yours; your authorship is kept.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
