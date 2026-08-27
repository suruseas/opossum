# The push-time sieve's container. A fresh instance of this image is what
# gives every `make sieve` an empty $HOME, an empty /tmp and a process space
# with nothing left over in it — the properties the CI runner used to be the
# only source of.
#
# The tag tracks the version go.mod asks for. Nothing enforces that from here
# — a Dockerfile cannot read go.mod — so the container itself checks, first
# thing (sieve/run.sh), and refuses to sieve with a compiler the
# module does not name. When go.mod moves, this line and a rebuild move with
# it, and until they do the sieve says so instead of quietly measuring the
# old one.
FROM golang:1.25-bookworm

# procps: the leftovers check (cmd/noleftovers) looks for processes a test
# run left behind, and without pgrep it refuses to run blind — every sieve
# would be red for the same non-answer.
# jq: the sieve makes the same go.mod-version assertion the CI job makes,
# with the same tools, so the two cannot drift into checking differently.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends procps jq \
	&& rm -rf /var/lib/apt/lists/*

# Not root, and measured rather than on principle: the first full run of this
# sieve passed two fixtures vacuously that every other environment fails —
# their premise is "creating a directory under /dev fails", and for root it
# does not. CI's runner is an unprivileged user; a sieve that runs as root is
# sieving a different machine than the one the tests were written about.
RUN useradd --create-home sieve \
	&& mkdir -p /home/sieve/go/pkg/mod /home/sieve/.cache/go-build \
	&& chown -R sieve:sieve /home/sieve
USER sieve
ENV GOPATH=/home/sieve/go GOCACHE=/home/sieve/.cache/go-build
