# Local build/test helpers. Releases are cut by pushing a v* tag (see
# .goreleaser.yaml and .github/workflows/release.yml).

# Version stamped into the binary: the current tag if HEAD is tagged, else a
# `-dev` suffix on the last tag + short SHA.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test cover install snapshot

build: ## build the opossum binary with the version stamped in
	go build -ldflags "$(LDFLAGS)" -o opossum ./cmd/opossum

test: ## run the full test suite (the regression gate)
	go test ./...

cover: ## run tests with coverage
	go test ./... -cover

install: ## build and install onto GOBIN/PATH
	go install -ldflags "$(LDFLAGS)" ./cmd/opossum

snapshot: ## build release artifacts locally without publishing (needs goreleaser)
	goreleaser release --snapshot --clean
