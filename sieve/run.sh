#!/bin/sh
# What runs inside the sieve's container: the same regression gate as
# everywhere else, behind the same version assertion the CI job makes.
#
# The assertion is not a formality here either. The container image pins a Go
# version in a Dockerfile that cannot read go.mod, so the day go.mod moves,
# this is what says "rebuild the image" instead of letting every sieve quietly
# measure the compiler the module no longer names.
set -eu

# Not root — asserted, not assumed, because assuming it is how the first full
# run went wrong: root can create directories under /dev, and two fixtures
# whose premise is "this creation fails" passed without testing anything. The
# image sets an unprivileged user, but the image cannot stop someone running
# it with --user 0, and this line is what says so instead of sieving quietly
# with the wrong hands.
if [ "$(id -u)" -eq 0 ]; then
	echo "The sieve is running as root, and root passes fixtures whose premise is" >&2
	echo "\"this operation is not permitted\" — a sieve for a different machine than" >&2
	echo "the one the tests describe. Run the image as its own user (the default)." >&2
	exit 1
fi

go version
want="$(go mod edit -json | jq -r '.Go // empty' | cut -d. -f1,2)"
here="$(go version | awk '{print $3}' | sed 's/^go//' | cut -d. -f1,2)"
echo "go.mod asks for $want; this sieve is running $here"
if [ -z "$want" ] || [ -z "$here" ]; then
	echo "Could not read one of the two versions, so whether they agree is unknown" >&2
	echo "— and unknown is not the same as fine." >&2
	exit 1
fi
if [ "$want" != "$here" ]; then
	echo "The sieve's image carries Go $here and go.mod asks for $want. Rebuild the" >&2
	echo "image (docker image rm opossum-sieve, then make sieve builds it again)" >&2
	echo "after moving the version in sieve/Dockerfile with go.mod." >&2
	exit 1
fi

exec make test
