# Local build/test helpers. Releases are cut by pushing a v* tag (see
# .goreleaser.yaml and .github/workflows/release.yml).

# Version stamped into the binary: the current tag if HEAD is tagged, else a
# `-dev` suffix on the last tag + short SHA.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test test-shipped wired hooks real-conformance cover sieve install snapshot changelog changelog-preview

# A TMPDIR of this invocation's own for `test` and `test-shipped`, left alone
# under CI — which already passes a per-job one (.github/workflows/ci.yml) —
# but always made here otherwise: on macOS, launchd sets $TMPDIR to a
# per-login-session directory for every shell, present or not is not the
# question, so checking `$(origin TMPDIR)` never fires where the bug actually
# lives (measured: an ordinary Terminal shell already has one, "environment"
# not "undefined") — two `make test` in the same login session share that
# same value regardless, and that sharing, not an unset TMPDIR, is what made
# `cmd/noleftovers` read one run's still-open t.TempDir() as the other's
# leftover (#1238, seen live: this recipe and a `releasedgate_test.go` sweep,
# same $TMPDIR, each catching the other's temp dirs mid-test). `ifndef CI`
# reads the one variable every CI provider sets and a workstation shell does
# not (GitHub Actions: measured), so it fires for every local run whatever
# $TMPDIR already holds, and never for CI's own — `ifndef` asks only whether
# the variable has a value, not what the value says, so a hypothetical
# `CI=false` would not fire this either; no CI provider is known to set it
# that way, so this is not chased further. $(shell echo $$PPID) is this make
# process's own pid — one subshell, asked once per invocation, not per
# recipe line, so both `go test` lines below (and, for test-shipped, its one
# line) share the same directory. Short, not content-addressed: a socket path
# under it can already run into the 104-108 byte unix-socket limit
# (orchestrator tests have hit this). A run this kills or that panics before
# reaching the closing `rm -rf` leaves its directory behind — the next run
# gets a different pid and does not collide with it, so this is left for a
# human to notice rather than guarded with a trap.
ifndef CI
test: export TMPDIR := /tmp/opossum-test-$(shell echo $$PPID)
test-shipped: export TMPDIR := /tmp/opossum-test-$(shell echo $$PPID)
endif

# `go test` stops a package's run at ten minutes unless it is told otherwise, and
# the orchestrator package is the longest of them under -race: 393 s and 405 s in
# the two lanes of a green CI run (the 971 tests are 368 s without -race, 112 s of
# them the 46 rows of one table that each wait for a real follow to end). It was
# stopped at 600 s twice (#1798, #1813) — and not because it is slow: the machine
# was, by about four times for the whole run. The same job's `go vet` took 21-31 s
# where it takes 8, and `cmd/opossum`, `internal/compose` and the rest took 3.4 to
# 4.8 times what they take when the run is green, so four times the orchestrator's
# 405 s is 27 minutes. Thirty minutes is that, and a test that hangs is still
# stopped, only later. It is given as GOFLAGS, which `go test` reads and every other
# go command ignores, so the gate's lines above keep the flags they are pinned to.
# Away from CI as well: the gate that is shorter than CI's is the one that passes
# where CI fails.
test: export GOFLAGS := $(GOFLAGS) -timeout=30m
test-shipped: export GOFLAGS := $(GOFLAGS) -timeout=30m

build: ## build the opossum binary with the version stamped in
	go build -ldflags "$(LDFLAGS)" -o opossum ./cmd/opossum

# -race and -cover because CI runs them, and a gate that is weaker than CI is a
# gate that lets through exactly the faults CI will stop.
#
# The cost does not show up in the wall clock. Three runs each, -count=1, same
# machine: 90/73/73s without these flags and 79/91/76s with. The two ranges
# overlap, and the spread inside a single setting is larger than any difference
# between the settings, so nothing about these flags can be read off the clock.
#
# The work is real and shows in the cpu: user+sys goes from about 81s to about
# 93s. It lands in the waiting rather than the clock because the suite is
# already limited by how much of it runs at once.
#
# The first run after picking these up is not in those numbers: the tree is
# recompiled with race instrumentation once, and that is paid by whoever pulls.
#
# An earlier version of this comment said "99s to 102s" from one sample each — a
# number inside the noise, reported as a difference.
#
# -count=1 because a cached "ok" is a report about a previous run. The cache is
# sound about code, but a test that fails on environment — a tool gone missing,
# a config drifted — passes by not running at all, and the gate's green reads
# the same either way. Measured before this flag: a one-file change left 12 of
# these packages unrun locally, and CI restored 2 packages from a cache frozen
# weeks earlier. The cost was measured once each way — 66s without, 92s with,
# warm — so read the direction, not the width: the direction is mechanical,
# twelve packages going from skipped to run. For iterating there is `go test`
# on the package at hand; the gate is the one place where "ran" must mean ran.
# cmd/busy runs last, alone. It measures how much CPU the machine can give —
# four workers must out-burn one by 1.6x — and `go test ./...` runs packages
# in parallel, so its neighbours are its competitors: on a two-core runner
# the arithmetic leaves the rest of the suite a 20% allowance before the
# ratio's ceiling drops under the line, and cmd/mutate alone spends fifty
# seconds of CPU beside it. Measured, three CI runs in a row. Splitting the
# recipe gives the measuring package the quiet it is measuring; both lines
# run under the leftovers check, and the gate is red if either is.
test: wired ## run the full test suite (the regression gate)
	@case "$$TMPDIR" in /tmp/opossum-test-*) mkdir -p "$$TMPDIR" ;; esac
	go run ./cmd/noleftovers go test $$(go list ./... | grep -v '/cmd/busy$$') -race -cover -count=1
	go run ./cmd/noleftovers go test ./cmd/busy -race -cover -count=1
	@case "$$TMPDIR" in /tmp/opossum-test-*) rm -rf "$$TMPDIR" ;; esac

# The gate for a copy of this repository that carries the product and not the
# workshop: the packages the released binary links, and nothing else. Asked
# of the compiler rather than listed here, so that a package added to the
# binary is tested without anyone remembering to add it. The gate command
# itself is the whole claim and is pinned as one line (the mkdir/rm around
# it, #1238, are this run's own housekeeping, not the claim), because a grep
# narrowed by a character tests a fraction of the binary and still prints
# `ok` for each package it did run.
#
# What is left out is this repository's tooling — the checks that read
# CONTRIBUTING.md, the changelog fragments and the notes from real-runtime
# review. A published copy has none of those three: the release that writes
# it folds the fragments into a version's section, and the contributor
# guidance and the review notes live where the work happens. (The release
# config is published, so it is not one of them.) Running those checks there
# asks whether files that were never meant to be shipped are in order.
test-shipped: wired ## run the tests of the packages the released binary links
	@case "$$TMPDIR" in /tmp/opossum-test-*) mkdir -p "$$TMPDIR" ;; esac
	go run ./cmd/noleftovers go test $$(go list -deps ./cmd/opossum | grep '^github.com/suruseas/opossum') -race -cover -count=1
	@case "$$TMPDIR" in /tmp/opossum-test-*) rm -rf "$$TMPDIR" ;; esac

# Opt-in: measures, against the real `container` runtime, the facts the socket
# guidance stands on. Not part of the gate — the daily gate is the fake's — and
# the flag makes missing preconditions a failure rather than a skip, so a run
# of this target either measured or is red.
real-conformance: ## run the real-runtime conformance measurements (needs `container` running, and a Docker daemon for the docker.sock one)
	OPOSSUM_REAL_RUNTIME=1 go run ./cmd/noleftovers go test ./internal/orchestrator -run 'TestAReal' -count=1 -v

cover: ## run tests with coverage
	go test ./... -cover

# The push-time sieve: the same gate, in a container that starts clean every
# time. An empty $HOME, an empty /tmp, no processes left over — the properties
# CI's disposable runner used to be the only source of, a fresh container
# provides on this machine, per push, off the metered minutes.
#
# The tree goes in as a bundle over stdin and is cloned inside — no bind
# mount, on two grounds that turned out to agree. What a push carries is
# commits, so the sieve reads HEAD as committed, exactly what the remote is
# about to receive — an uncommitted fix in the working tree cannot make the
# sieve green for a push that does not include it. And a copy inside the
# container is a tree no write survives: a run that would have left something
# in the repository leaves it in a filesystem that is about to not exist.
# (The bind mount this replaced also proved unreliable under Docker Desktop's
# file sharing — a freshly created file was intermittently invisible in the
# container. The bundle goes through a pipe and has no such layer to lie.)
#
# The two named volumes hold Go's build and module caches — content-addressed,
# and the gate's -count=1 keeps cached test results out of the question either
# way. What the sieve does not see: the current Go release. It runs the
# version go.mod asks for; the second compiler is half of what the pull
# request's CI run exists to attest.
# Named, not refused — see sieve/wired.sh. A prerequisite of `test` so that the
# one command everyone runs is where an unwired clone is heard about.
wired: ## say so when this clone's push-time sieve is not wired
	@sh sieve/wired.sh

hooks: ## wire the commit and push hooks into this clone (once per clone)
	git config core.hooksPath .githooks

# The image is built only when it is not there — and that is asked with
# `image ls`, not `image inspect`. Docker Desktop's Resource Saver stops the
# VM after a few idle minutes; asked then, `docker info` still answers (the
# backend does), `image inspect` answers "No such image" without waking the
# VM, and the build that followed was what woke it — a registry round-trip,
# which is where this machine's pushes went red, for an image that was there
# all along (#706). `image ls` woke the daemon and answered once it was up,
# the one time it was watched; should it ever answer empty while the daemon
# is still coming up, the build follows, which is what happened before.
sieve: ## run the regression gate in a clean Linux container (the push-time sieve)
	@[ -n "$$(docker image ls -q opossum-sieve:latest 2>/dev/null)" ] || \
		docker build -t opossum-sieve -f sieve/Dockerfile sieve
	git bundle create - HEAD | docker run --rm -i \
		-v opossum-sieve-gomod:/home/sieve/go/pkg/mod \
		-v opossum-sieve-gocache:/home/sieve/.cache/go-build \
		opossum-sieve sh -c 'cat > /tmp/head.bundle \
			&& git clone -q /tmp/head.bundle ~/work \
			&& cd ~/work && sh sieve/run.sh'

install: ## build and install onto GOBIN/PATH
	go install -ldflags "$(LDFLAGS)" ./cmd/opossum

snapshot: ## build release artifacts locally without publishing (needs goreleaser)
	goreleaser release --snapshot --clean

changelog: ## regenerate CHANGELOG.md's [Unreleased] from changelog.d/ fragments
	go run ./cmd/changelog sync

changelog-preview: ## print what [Unreleased] would contain, without writing
	go run ./cmd/changelog preview
