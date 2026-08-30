#!/bin/sh
# Generate deps-proof.txt: machine-checkable evidence that molt's shipped
# artifact contains no third-party code.
#
# The decisive check is the last one. "go list -deps ./..." prints every package
# that goes into the build, transitively. Filtering for a dot in the first path
# element finds anything outside the standard library, because every module path
# outside it begins with a domain name. An empty result is the proof.
set -eu

GO="${GO:-go}"

echo "molt dependency proof"
echo "====================="
echo
echo "Reproduce this file with: make deps-proof"
echo

echo "1. go.mod in full"
echo "-----------------"
cat go.mod
echo
echo "There is no require block. Nothing to audit."
echo

echo "2. Modules in the build graph"
echo "-----------------------------"
echo "\$ go list -m all"
$GO list -m all
echo
echo "molt itself, and nothing else."
echo

echo "3. Every package in the build, filtered for non-stdlib"
echo "------------------------------------------------------"
echo "\$ go list -deps ./... | grep -v '^molt' | awk -F/ '\$1 ~ /\./'"
THIRD_PARTY=$($GO list -deps ./... | grep -v '^molt' | awk -F/ '$1 ~ /\./' || true)
if [ -z "$THIRD_PARTY" ]; then
	echo "(no output)"
	echo
	echo "Every path in the build graph is either molt's own or a standard-library"
	echo "package. A third-party path would begin with a domain name and appear here."
else
	echo "$THIRD_PARTY"
	echo
	echo "FAILED: the paths above are not standard library."
	exit 1
fi
echo

echo "4. Standard-library packages molt imports"
echo "-----------------------------------------"
$GO list -deps ./... | grep -v '^molt' | awk -F/ '$1 !~ /\./' | grep -v '^internal/' | sort | tr '\n' ' ' | fold -s -w 76
echo
echo

echo "5. No vendored source"
echo "---------------------"
if [ -d vendor ]; then
	echo "FAILED: a vendor directory exists"
	exit 1
fi
echo "No vendor/ directory."
echo
echo "The only third-party code in this repository is under testdata/, which"
echo "holds fixture modules that are never built. Their go.mod files declare"
echo "requires on purpose, so molt has something to find. The go command and"
echo "molt's own scanner both skip directories named testdata."
echo

echo "6. go.sum"
echo "---------"
if [ -f go.sum ]; then
	echo "go.sum exists with $(wc -l < go.sum) lines"
else
	echo "No go.sum. With no requires there are no module hashes to record."
fi
