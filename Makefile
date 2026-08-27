# Local build/test helpers. Releases are cut by pushing a v* tag (see
# .goreleaser.yaml and .github/workflows/release.yml).

# Version stamped into the binary: the current tag if HEAD is tagged, else a
# `-dev` suffix on the last tag + short SHA.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test real-conformance cover sieve install snapshot changelog changelog-preview

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
test: ## run the full test suite (the regression gate)
	go run ./cmd/noleftovers go test $$(go list ./... | grep -v '/cmd/busy$$') -race -cover -count=1
	go run ./cmd/noleftovers go test ./cmd/busy -race -cover -count=1

# Opt-in: measures, against the real `container` runtime, the facts the socket
# guidance stands on. Not part of the gate — the daily gate is the fake's — and
# the flag makes missing preconditions a failure rather than a skip, so a run
# of this target either measured or is red.
real-conformance: ## run the real-runtime conformance measurements (needs `container` running)
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
# request's one CI run exists to attest.
sieve: ## run the regression gate in a clean Linux container (the push-time sieve)
	@docker image inspect opossum-sieve >/dev/null 2>&1 || \
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
