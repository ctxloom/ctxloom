#!/usr/bin/env bash
# A plain build (no `test-support`) knows only the real engines (ruling 52):
# the mock engine and the mock-* test engines are compiled and registered only
# under the `test-support` feature the acceptance tests build with. This runs
# the binary a plain `cargo build` made, in a temporary home, and refuses one
# whose `engine list` names a mock, or whose `engine show mock` answers.
#
# The unit tests cannot see this: a workspace test build unifies features, so
# every crate there has `test-support` on. Only a build without the
# dev-dependencies (`just build`) makes the binary that ships.
#
# Usage: plain-engines-check.sh <ctxloom binary>
# Silent on a pass; on a failure the first line is the summary, then what the
# binary said; exit 1.
set -euo pipefail
bin=$(realpath "${1:?usage: plain-engines-check.sh <ctxloom binary>}")
home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT
ctxloom() { (cd "$home" && env -i PATH="$PATH" HOME="$home" TMPDIR="$home" "$bin" "$@"); }

listed=$(ctxloom engine list --format json) || {
    echo "plain build: engine list failed"; echo "$listed"; exit 1; }
mocks=$(jq -r '.[].name | select(startswith("mock"))' <<< "$listed")
if [ -n "$mocks" ]; then
    echo "plain build: engine list names a test engine: $(tr '\n' ' ' <<< "$mocks")"
    exit 1
fi
for engine in mock mock-launch mock-lossy mock-noskills; do
    rc=0
    said=$(ctxloom engine show "$engine" --format json 2>&1) || rc=$?
    if [ "$rc" -ne 2 ]; then
        echo "plain build: engine show $engine exited $rc, not 2 (unknown engine)"
        echo "$said"
        exit 1
    fi
done
