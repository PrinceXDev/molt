#!/bin/sh
# Build molt twice from a clean cache and prove the artifacts are byte-identical.
#
# Determinism is not automatic in Go. Three things break it by default:
#
#   1. Absolute source paths are embedded, so the build directory leaks in.
#      -trimpath removes them.
#   2. Since Go 1.24 the toolchain stamps VCS information into the binary, so
#      the commit hash and the dirty flag change the bytes. -buildvcs=false
#      turns that off.
#   3. The build id varies. -ldflags "-buildid=" clears it.
#
# CGO_ENABLED=0 removes the host C toolchain from the equation, and pinning
# GOTOOLCHAIN means a different Go version cannot silently change the output.
set -eu

GOTOOLCHAIN="${GOTOOLCHAIN:-go1.27.0}"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
export GOTOOLCHAIN GOOS GOARCH CGO_ENABLED=0

FLAGS='-trimpath -buildvcs=false'
LDFLAGS='-s -w -buildid='

echo "molt reproducible build"
echo
echo "toolchain   $GOTOOLCHAIN"
echo "target      $GOOS/$GOARCH"
echo "flags       $FLAGS -ldflags \"$LDFLAGS\""
echo

# The second build clears the build cache first, so a cached object cannot be
# the reason the two agree.
go build $FLAGS -ldflags "$LDFLAGS" -o molt-build-1 ./cmd/molt
go clean -cache
go build $FLAGS -ldflags "$LDFLAGS" -o molt-build-2 ./cmd/molt

if command -v sha256sum >/dev/null 2>&1; then
	HASH1=$(sha256sum molt-build-1 | cut -d' ' -f1)
	HASH2=$(sha256sum molt-build-2 | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
	HASH1=$(shasum -a 256 molt-build-1 | cut -d' ' -f1)
	HASH2=$(shasum -a 256 molt-build-2 | cut -d' ' -f1)
else
	echo "no sha256sum or shasum on PATH" >&2
	exit 1
fi

echo "build 1     $HASH1"
echo "build 2     $HASH2"
echo

if [ "$HASH1" = "$HASH2" ]; then
	echo "IDENTICAL - the two builds produced the same bytes"
	rm -f molt-build-2
	mv molt-build-1 molt
	exit 0
fi

echo "DIFFERENT - the build is not reproducible" >&2
exit 1
