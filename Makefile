# Local build/test helpers. Releases are cut by pushing a v* tag (see
# .goreleaser.yaml and .github/workflows/release.yml).

# Version stamped into the binary: the current tag if HEAD is tagged, else a
# `-dev` suffix on the last tag + short SHA.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test cover install snapshot changelog changelog-preview

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
test: ## run the full test suite (the regression gate)
	go run ./cmd/noleftovers go test ./... -race -cover

cover: ## run tests with coverage
	go test ./... -cover

install: ## build and install onto GOBIN/PATH
	go install -ldflags "$(LDFLAGS)" ./cmd/opossum

snapshot: ## build release artifacts locally without publishing (needs goreleaser)
	goreleaser release --snapshot --clean

changelog: ## regenerate CHANGELOG.md's [Unreleased] from changelog.d/ fragments
	go run ./cmd/changelog sync

changelog-preview: ## print what [Unreleased] would contain, without writing
	go run ./cmd/changelog preview
