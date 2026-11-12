#!/usr/bin/env bash
# A plain build (no `test-support`) knows only the real engines (ruling 52):
# the mock engine and the mock-* test engines are compiled and registered only
# under the `test-support` feature the acceptance tests build with. This runs
# the binary a plain `cargo build` made, in a temporary home, and refuses one
# whose `engine list` names a mock, or whose `engine show` of a mock (the
# library's list) or `manage install --engine mock` (the surface registry) is
# not the unknown-engine refusal.
#
# The unit tests cannot see this: a workspace test build unifies features, so
# every crate there has `test-support` on. Only a build without the
# dev-dependencies (`just build`) makes the binary that ships.
#
# Usage: plain-engines-check.sh <ctxloom binary>
# Silent on a pass; on a failure the first line is the summary, then what the
# binary said; exit 1.
set -euo pipefail
bin=${1:?usage: plain-engines-check.sh <ctxloom binary>}
# Windows names the binary <name>.exe (cargo's target/debug/ctxloom.exe).
[ -f "$bin.exe" ] && bin=$bin.exe
bin=$(realpath "$bin")
home=$(mktemp -d)
trap 'rm -rf "$home"' EXIT
# On Windows (SYSTEMROOT set; never on unix, where nothing is added) the
# binary's home is USERPROFILE and its temporary directory TEMP or TMP, in
# Windows' own form, and a Windows program needs SYSTEMROOT and WINDIR.
windows=()
root=${SYSTEMROOT:-${SystemRoot:-}}
if [ -n "$root" ]; then
    native=$(cygpath -w "$home")
    windows=(USERPROFILE="$native" TEMP="$native" TMP="$native" SYSTEMROOT="$root" WINDIR="$root")
fi
# An empty array is spelled ${a[@]+"${a[@]}"}: macOS's bash 3.2 calls a bare
# "${a[@]}" of an empty one unbound under set -u.
# A Windows jq ends its lines CRLF; its answers here are compared as text.
jq() { command jq "$@" | tr -d '\r'; }
ctxloom() { (cd "$home" && env -i PATH="$PATH" HOME="$home" TMPDIR="$home" ${windows[@]+"${windows[@]}"} "$bin" "$@"); }

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
    code=$(jq -r '.code? // empty' <<< "$said" 2>/dev/null || true)
    if [ "$rc" -eq 0 ] || [ "$code" != library-application-unknown-engine ]; then
        echo "plain build: engine show $engine is not refused as an unknown engine (exit $rc)"
        echo "$said"
        exit 1
    fi
done
# The surface registry: an engine it does not hold is refused by name.
rc=0
said=$(ctxloom manage install --engine mock --format json 2>&1) || rc=$?
code=$(jq -r '.code? // empty' <<< "$said" 2>/dev/null || true)
if [ "$rc" -eq 0 ] || [ "$code" != setup-application-unknown-engine ]; then
    echo "plain build: manage install --engine mock is not refused as an unknown engine (exit $rc)"
    echo "$said"
    exit 1
fi
